CREATE TABLE IF NOT EXISTS agents (
  id TEXT NOT NULL,
  version INTEGER NOT NULL,
  name TEXT NOT NULL,
  body TEXT NOT NULL,          -- JSON of the full agent at this version
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT,
  PRIMARY KEY (id, version)
);
CREATE TABLE IF NOT EXISTS environments (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  config_type TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT
);
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  agent_version INTEGER NOT NULL,
  environment_id TEXT NOT NULL,
  status TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  archived_at TEXT
);
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  processed_at TEXT,
  UNIQUE (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_events_session_seq ON events(session_id, seq);
CREATE TABLE IF NOT EXISTS session_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  admission_seq INTEGER NOT NULL,
  trigger_event_ids TEXT NOT NULL,
  -- Internal-only. The exact committed output event ids this run appended when
  -- it closed, persisted in the same transaction that closes the run so there is
  -- never a completed run without its output association. Empty until the run
  -- completes. Never serialized onto the public wire.
  output_event_ids TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (session_id, admission_seq)
);
CREATE INDEX IF NOT EXISTS idx_session_runs_session_state_seq
  ON session_runs(session_id, state, admission_seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_runs_one_running
  ON session_runs(session_id) WHERE state = 'running';
-- Internal-only execution attempts for one logical run. A later retry creates a
-- new attempt instead of erasing what an earlier process may have done. The
-- runtime writes these rows around every locally executed built-in tool.
CREATE TABLE IF NOT EXISTS run_attempts (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  attempt_no INTEGER NOT NULL CHECK (attempt_no > 0),
  state TEXT NOT NULL CHECK (state IN ('active', 'completed', 'failed', 'interrupted')),
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT,
  UNIQUE (run_id, attempt_no),
  FOREIGN KEY (run_id) REFERENCES session_runs(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_attempts_one_active
  ON run_attempts(run_id) WHERE state = 'active';
-- A tool step records the model-requested operation before execution begins,
-- then advances through an explicit side-effect boundary. A started step that
-- has no durable result is never folded back into prepared: recovery must mark
-- it ambiguous rather than silently executing it again.
CREATE TABLE IF NOT EXISTS tool_steps (
  id TEXT PRIMARY KEY,
  attempt_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  tool_use_event_id TEXT NOT NULL UNIQUE,
  tool_name TEXT NOT NULL,
  input TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('prepared', 'started', 'completed', 'ambiguous')),
  result TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  UNIQUE (attempt_id, ordinal),
  FOREIGN KEY (attempt_id) REFERENCES run_attempts(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_tool_steps_attempt_ordinal
  ON tool_steps(attempt_id, ordinal);
-- Internal-only durable record of a run that parked awaiting a client response
-- (a custom tool result or an always_ask tool confirmation). While a row is
-- unresolved (resolved_at IS NULL) it gates the session's ordinary queued runs;
-- only a matching resolution trigger may be claimed. Never serialized onto the
-- public wire. action_event_id references the committed agent.custom_tool_use /
-- agent.tool_use event; kind is derived from that event's type AND payload (an
-- agent.tool_use parks only when its evaluated_permission is "ask"), not any
-- caller string. The unique constraint makes a single park emit exactly one
-- pending action per action event.
CREATE TABLE IF NOT EXISTS pending_actions (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  action_event_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  resolving_event_id TEXT,
  created_at TEXT NOT NULL,
  resolved_at TEXT,
  UNIQUE (session_id, action_event_id)
);
CREATE INDEX IF NOT EXISTS idx_pending_actions_unresolved
  ON pending_actions(session_id) WHERE resolved_at IS NULL;
