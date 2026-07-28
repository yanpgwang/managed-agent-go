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
