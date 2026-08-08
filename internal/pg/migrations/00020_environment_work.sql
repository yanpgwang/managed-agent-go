-- +goose Up
-- +goose StatementBegin

CREATE TABLE environment_work (
    id                  text        PRIMARY KEY,
    environment_id      text        NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    session_id          text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    activation_seq      bigint      NOT NULL,
    state               text        NOT NULL CHECK (state IN ('queued', 'starting', 'active', 'stopping', 'stopped')),
    metadata            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL,
    acknowledged_at     timestamptz,
    started_at          timestamptz,
    latest_heartbeat_at timestamptz,
    ttl_seconds         bigint      NOT NULL DEFAULT 30 CHECK (ttl_seconds > 0),
    stop_requested_at   timestamptz,
    stopped_at          timestamptz,
    polled_at           timestamptz,
    poll_worker_id      text
);

-- A Session has one queued or executable activation at a time. A successor
-- may wait beside a stopping predecessor, but Poll serializes that handoff.
CREATE UNIQUE INDEX environment_work_live_session_idx
    ON environment_work (session_id)
    WHERE state IN ('queued', 'starting', 'active');
CREATE INDEX environment_work_queue_idx
    ON environment_work (environment_id, created_at, id)
    WHERE state = 'queued';
CREATE INDEX environment_work_list_idx
    ON environment_work (environment_id, created_at DESC, id DESC);

CREATE TABLE environment_work_pollers (
    environment_id text        NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    worker_id      text        NOT NULL,
    polled_at      timestamptz NOT NULL,
    PRIMARY KEY (environment_id, worker_id)
);
CREATE INDEX environment_work_pollers_recent_idx
    ON environment_work_pollers (environment_id, polled_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS environment_work_pollers;
DROP TABLE IF EXISTS environment_work;
-- +goose StatementEnd
