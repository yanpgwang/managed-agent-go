-- +goose Up
-- +goose StatementBegin

-- Durable tool-execution journal for the Temporal path, mirroring the SQLite
-- prepared/started/completed/ambiguous boundary (internal/store/execution_store.go)
-- so a built-in tool step run under a RunTurn Activity survives worker/activity
-- retries without silently replaying an external side effect.
--
-- The "logical run" here is one turn, identified by (session_id,
-- trigger_event_id) — the public event that caused the turn. Each RunTurn
-- Activity execution is an attempt; a Temporal retry creates a new attempt
-- rather than erasing the facts a prior attempt recorded.

CREATE TABLE turn_attempts (
    id               text        PRIMARY KEY,
    session_id       text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    trigger_event_id text        NOT NULL,
    attempt_no       integer     NOT NULL CHECK (attempt_no > 0),
    state            text        NOT NULL CHECK (state IN ('active', 'completed', 'failed', 'interrupted')),
    error            text,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    finished_at      timestamptz,
    UNIQUE (session_id, trigger_event_id, attempt_no)
);

-- At most one active attempt per turn: a new attempt cannot begin while a prior
-- one is still active (it must be recovered/classified first).
CREATE UNIQUE INDEX turn_attempts_one_active
    ON turn_attempts (session_id, trigger_event_id) WHERE state = 'active';

-- A tool step records the model-requested operation before execution begins and
-- then advances across the side-effect boundary. A started step with no durable
-- result is never folded back to prepared: recovery marks it ambiguous rather
-- than silently executing it again.
CREATE TABLE tool_steps (
    id                text        PRIMARY KEY,
    attempt_id        text        NOT NULL REFERENCES turn_attempts (id) ON DELETE CASCADE,
    ordinal           integer     NOT NULL CHECK (ordinal >= 0),
    tool_use_event_id text        NOT NULL UNIQUE,
    tool_name         text        NOT NULL,
    input             jsonb       NOT NULL,
    state             text        NOT NULL CHECK (state IN ('prepared', 'started', 'completed', 'ambiguous')),
    result            jsonb,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    started_at        timestamptz,
    finished_at       timestamptz,
    UNIQUE (attempt_id, ordinal)
);

CREATE INDEX tool_steps_attempt_idx ON tool_steps (attempt_id, ordinal);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tool_steps;
DROP TABLE IF EXISTS turn_attempts;
-- +goose StatementEnd
