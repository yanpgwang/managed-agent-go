-- +goose Up
-- +goose StatementBegin

-- File-backed Session Resources own a session-scoped File copy and a desired
-- mount path. Deleting rows remain as tombstones until the worker has removed
-- any applied sandbox mount; public reads expose active rows only.
CREATE TABLE session_resources (
    id             text        PRIMARY KEY,
    session_id     text        NOT NULL REFERENCES sessions (id),
    resource_type  text        NOT NULL CHECK (resource_type = 'file'),
    source_file_id text        NOT NULL,
    file_id        text        NOT NULL UNIQUE,
    mount_path     text        NOT NULL,
    state          text        NOT NULL CHECK (state IN ('active', 'deleting')),
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

CREATE INDEX session_resources_active_list_idx
    ON session_resources (session_id, created_at, id)
    WHERE state = 'active';
CREATE INDEX session_resources_deleting_idx
    ON session_resources (session_id, updated_at, id)
    WHERE state = 'deleting';
CREATE INDEX session_resources_session_idx
    ON session_resources (session_id);
CREATE UNIQUE INDEX session_resources_active_mount_idx
    ON session_resources (session_id, mount_path)
    WHERE state = 'active';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS session_resources;
-- +goose StatementEnd
