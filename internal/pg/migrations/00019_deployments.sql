-- +goose Up
-- +goose StatementBegin

CREATE TABLE deployments (
    id                  text        PRIMARY KEY,
    agent_id            text        NOT NULL,
    agent_version       integer     NOT NULL,
    environment_id      text        NOT NULL REFERENCES environments (id),
    status              text        NOT NULL CHECK (status IN ('active', 'paused')),
    body                jsonb       NOT NULL,
    next_run_at         timestamptz,
    schedule_claimed_at timestamptz,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    archived_at         timestamptz,
    FOREIGN KEY (agent_id, agent_version) REFERENCES agents (id, version)
);

CREATE INDEX deployments_list_idx ON deployments (created_at, id);
CREATE INDEX deployments_agent_idx ON deployments (agent_id, created_at, id);
CREATE INDEX deployments_due_idx ON deployments (next_run_at, id)
    WHERE archived_at IS NULL AND status = 'active' AND next_run_at IS NOT NULL;

ALTER TABLE sessions
    ADD COLUMN deployment_id text REFERENCES deployments (id);
CREATE INDEX sessions_deployment_idx ON sessions (deployment_id, created_at, id)
    WHERE deployment_id IS NOT NULL;

CREATE TABLE deployment_runs (
    id             text        PRIMARY KEY,
    deployment_id  text        NOT NULL REFERENCES deployments (id),
    -- A Run is an append-only creation audit. Keep the successful Session ID
    -- after that Session is independently deleted.
    session_id     text,
    error_type     text,
    trigger_type   text        NOT NULL CHECK (trigger_type IN ('manual', 'schedule')),
    scheduled_at   timestamptz,
    body           jsonb       NOT NULL,
    created_at     timestamptz NOT NULL,
    CHECK ((session_id IS NOT NULL) <> (error_type IS NOT NULL)),
    CHECK ((trigger_type = 'schedule') = (scheduled_at IS NOT NULL))
);

CREATE UNIQUE INDEX deployment_runs_scheduled_once_idx
    ON deployment_runs (deployment_id, scheduled_at)
    WHERE trigger_type = 'schedule';
CREATE INDEX deployment_runs_list_idx ON deployment_runs (created_at DESC, id DESC);
CREATE INDEX deployment_runs_deployment_idx
    ON deployment_runs (deployment_id, created_at DESC, id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS deployment_runs;
DROP INDEX IF EXISTS sessions_deployment_idx;
ALTER TABLE sessions DROP COLUMN IF EXISTS deployment_id;
DROP TABLE IF EXISTS deployments;
-- +goose StatementEnd
