-- +goose Up
-- +goose StatementBegin

-- MCP configuration belongs to one Agent execution context, not to the
-- Session-wide sandbox. Every existing snapshot was produced by the primary
-- Thread; child Threads get independent discovery surfaces even when two
-- roster members reuse the same server name.
ALTER TABLE mcp_discovery_snapshots
    ADD COLUMN thread_id text;

UPDATE mcp_discovery_snapshots AS snapshot
SET thread_id = thread.id
FROM session_threads AS thread
WHERE thread.session_id = snapshot.session_id
  AND thread.kind = 'primary';

ALTER TABLE mcp_discovery_snapshots
    ALTER COLUMN thread_id SET NOT NULL,
    DROP CONSTRAINT mcp_discovery_snapshots_pkey,
    ADD PRIMARY KEY (session_id, thread_id, server_name),
    ADD CONSTRAINT mcp_discovery_snapshots_thread_fk
        FOREIGN KEY (session_id, thread_id)
        REFERENCES session_threads (session_id, id) ON DELETE CASCADE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The former schema can retain only the primary Thread's snapshot for a given
-- Session/server pair.
DELETE FROM mcp_discovery_snapshots AS snapshot
WHERE NOT EXISTS (
    SELECT 1
    FROM session_threads AS thread
    WHERE thread.session_id = snapshot.session_id
      AND thread.id = snapshot.thread_id
      AND thread.kind = 'primary'
);

ALTER TABLE mcp_discovery_snapshots
    DROP CONSTRAINT mcp_discovery_snapshots_thread_fk,
    DROP CONSTRAINT mcp_discovery_snapshots_pkey,
    DROP COLUMN thread_id,
    ADD PRIMARY KEY (session_id, server_name);

-- +goose StatementEnd
