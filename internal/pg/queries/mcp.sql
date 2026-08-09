-- name: GetMCPDiscoverySnapshot :one
SELECT session_id, thread_id, server_name, server_url, tools, created_at
FROM mcp_discovery_snapshots
WHERE session_id = @session_id
  AND thread_id = @thread_id
  AND server_name = @server_name;

-- name: InsertMCPDiscoverySnapshot :exec
INSERT INTO mcp_discovery_snapshots (
    session_id, thread_id, server_name, server_url, tools, created_at
)
VALUES (
    @session_id, @thread_id, @server_name, @server_url, @tools, @created_at
)
ON CONFLICT (session_id, thread_id, server_name) DO NOTHING;

-- name: DeleteMCPDiscoverySnapshotsForThread :exec
DELETE FROM mcp_discovery_snapshots
WHERE session_id = @session_id AND thread_id = @thread_id;
