-- Durable session-to-provider sandbox ownership.

-- name: GetSandboxBinding :one
SELECT session_id, provider, external_id, spec_hash, created_at, updated_at
FROM session_sandboxes
WHERE session_id = @session_id;

-- name: PutSandboxBinding :one
INSERT INTO session_sandboxes (
    session_id, provider, external_id, spec_hash, created_at, updated_at
)
VALUES (
    @session_id, @provider, @external_id, @spec_hash, @created_at, @updated_at
)
ON CONFLICT (session_id) DO UPDATE SET
    updated_at = session_sandboxes.updated_at
RETURNING session_id, provider, external_id, spec_hash, created_at, updated_at;

-- name: DeleteSandboxBinding :execrows
DELETE FROM session_sandboxes
WHERE session_id = @session_id
  AND provider = @provider
  AND external_id = @external_id;
