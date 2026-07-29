-- +goose Up
-- +goose StatementBegin

-- Public API resources move into the same PostgreSQL source of truth as the
-- session event ledger. Agent versions are append-only; archival is projected
-- onto every version because it is lifecycle state for the resource, not
-- mutable versioned configuration.
CREATE TABLE agents (
    id          text        NOT NULL,
    version     integer     NOT NULL CHECK (version > 0),
    name        text        NOT NULL,
    body        jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    archived_at timestamptz,
    PRIMARY KEY (id, version)
);

CREATE INDEX agents_latest_idx ON agents (id, version DESC);

CREATE TABLE environments (
    id          text        PRIMARY KEY,
    name        text        NOT NULL,
    config_type text        NOT NULL,
    body        jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    archived_at timestamptz
);

-- The initial platform-spine migration intentionally stored only a JSON
-- projection. Add relational keys needed by API filtering and dependency
-- checks. They remain nullable so Workflow histories and development databases
-- created by the earlier vertical slice continue to migrate safely. The public
-- API always writes all three values.
ALTER TABLE sessions
    ADD COLUMN agent_id text,
    ADD COLUMN agent_version integer,
    ADD COLUMN environment_id text,
    ADD COLUMN archived_at timestamptz;

CREATE INDEX sessions_agent_idx
    ON sessions (agent_id, agent_version, created_at, id);
CREATE INDEX sessions_status_idx
    ON sessions (status, created_at, id);
CREATE INDEX sessions_active_idx
    ON sessions (created_at, id)
    WHERE archived_at IS NULL;
CREATE INDEX events_processed_idx
    ON events (session_id, processed_at, seq);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS events_processed_idx;
DROP INDEX IF EXISTS sessions_active_idx;
DROP INDEX IF EXISTS sessions_status_idx;
DROP INDEX IF EXISTS sessions_agent_idx;
ALTER TABLE sessions
    DROP COLUMN IF EXISTS archived_at,
    DROP COLUMN IF EXISTS environment_id,
    DROP COLUMN IF EXISTS agent_version,
    DROP COLUMN IF EXISTS agent_id;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS agents;
-- +goose StatementEnd
