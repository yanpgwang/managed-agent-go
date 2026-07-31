-- +goose Up
-- +goose StatementBegin

-- MCP discovery is pinned per Session. A remote server adding, removing, or
-- mutating tools must not silently change an already-running agent's tool
-- surface on a later turn.
CREATE TABLE mcp_discovery_snapshots (
    session_id   text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    server_name  text        NOT NULL,
    server_url   text        NOT NULL,
    tools        jsonb       NOT NULL,
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (session_id, server_name)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS mcp_discovery_snapshots;
-- +goose StatementEnd
