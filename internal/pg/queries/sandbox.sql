-- Durable session-to-provider sandbox ownership.

-- name: GetSandboxProvisioningIntent :one
SELECT session_id, provider, spec, spec_hash, created_at, updated_at
FROM sandbox_provisioning_intents
WHERE session_id = @session_id;

-- name: PutSandboxProvisioningIntent :one
INSERT INTO sandbox_provisioning_intents (
    session_id, provider, spec, spec_hash, created_at, updated_at
)
SELECT
    @session_id, @provider, @spec, @spec_hash, @created_at, @updated_at
FROM sessions
WHERE id = @session_id AND deleting_at IS NULL
ON CONFLICT (session_id) DO UPDATE SET
    updated_at = sandbox_provisioning_intents.updated_at
RETURNING session_id, provider, spec, spec_hash, created_at, updated_at;

-- name: ListSandboxProvisioningIntents :many
SELECT intent.session_id,
       intent.provider,
       intent.spec,
       intent.spec_hash,
       intent.created_at,
       intent.updated_at,
       session.deleting_at
FROM sandbox_provisioning_intents AS intent
JOIN sessions AS session ON session.id = intent.session_id
WHERE intent.provider = @provider
ORDER BY intent.created_at, intent.session_id
LIMIT @row_limit;

-- name: DeleteSandboxProvisioningIntent :execrows
DELETE FROM sandbox_provisioning_intents
WHERE session_id = @session_id
  AND provider = @provider
  AND spec_hash = @spec_hash;

-- name: DeleteSandboxProvisioningIntentBySession :exec
DELETE FROM sandbox_provisioning_intents
WHERE session_id = @session_id;

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
