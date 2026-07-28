-- +goose Up
-- +goose StatementBegin

-- Platform-spine schema for the Temporal/PostgreSQL session path.
--
-- This is a NEW, parallel path. SQLite (internal/store) remains the default
-- compatibility store; nothing here migrates or replaces it. Only the tables the
-- first end-to-end vertical slice needs are created: a session projection, the
-- append-only public event ledger with a durable per-session receipt sequence,
-- and the coalescible orchestration outbox that wakes the SessionWorkflow.
--
-- Deliberately NOT here (out of milestone scope): agents, environments, runs,
-- run attempts, tool steps, pending actions, multiagent threads, schedules,
-- webhooks. The outbox is a coalescible wakeup, not a run queue.

-- Session projection. body holds the full domain.Session snapshot as authored by
-- the application; status is denormalized for cheap filtering and the admission
-- lock. PostgreSQL — not Temporal — owns this public projection.
CREATE TABLE sessions (
    id         text        PRIMARY KEY,
    status     text        NOT NULL,
    body       jsonb       NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Append-only public event ledger. seq is the durable per-session receipt
-- sequence assigned under the session admission lock; it is the authoritative
-- public ordering and the cursor space the SessionWorkflow consumes.
--
-- turn_event_id correlates a committed agent output event back to the client
-- trigger event that caused it. It is null for client input and status events.
-- It lets the completion commit be idempotent: a retried completion recognizes a
-- trigger already processed and returns the exact events it committed before,
-- instead of appending a second copy.
CREATE TABLE events (
    id            text        PRIMARY KEY,
    session_id    text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq           bigint      NOT NULL,
    type          text        NOT NULL,
    payload       jsonb       NOT NULL,
    turn_event_id text,
    created_at    timestamptz NOT NULL,
    processed_at  timestamptz,
    UNIQUE (session_id, seq)
);

CREATE INDEX events_session_seq_idx ON events (session_id, seq);
CREATE INDEX events_turn_idx ON events (session_id, turn_event_id)
    WHERE turn_event_id IS NOT NULL;

-- Coalescible orchestration outbox. At most one pending wakeup per session
-- (session_id is the primary key). Admitting more events while a wakeup is still
-- pending coalesces into the same row and raises max_event_seq to the newest
-- receipt sequence, so a burst of admissions produces one wakeup carrying the
-- highest known sequence rather than a queue of jobs. The relay delivers the
-- wakeup with Signal-With-Start and deletes the row only if max_event_seq is
-- unchanged, so an admission that raced with delivery is never lost.
CREATE TABLE orchestration_outbox (
    session_id      text        PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    max_event_seq   bigint      NOT NULL,
    enqueued_at     timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,
    last_error      text
);

-- Relay scan order: oldest enqueued wakeup first.
CREATE INDEX orchestration_outbox_enqueued_idx ON orchestration_outbox (enqueued_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS orchestration_outbox;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS sessions;
-- +goose StatementEnd
