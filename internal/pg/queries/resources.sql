-- Typed resource queries for the PostgreSQL HTTP control plane.

-- name: InsertAgentVersion :exec
INSERT INTO agents (
    id, version, name, body, created_at, updated_at, archived_at, workspace_id
)
VALUES (
    @id, @version, @name, @body, @created_at, @updated_at, @archived_at, @workspace_id
);

-- name: LockLatestAgent :one
SELECT agents.*
FROM agents
WHERE id = @id AND workspace_id = @workspace_id
ORDER BY version DESC
LIMIT 1
FOR UPDATE;

-- name: GetLatestAgent :one
SELECT agents.*
FROM agents
WHERE id = @id AND workspace_id = @workspace_id
ORDER BY version DESC
LIMIT 1;

-- name: GetAgentVersion :one
SELECT agents.*
FROM agents
WHERE id = @id AND version = @version AND workspace_id = @workspace_id;

-- name: ListAgentVersions :many
SELECT agents.*
FROM agents
WHERE id = @id AND version > @after_version AND workspace_id = @workspace_id
ORDER BY version
LIMIT @row_limit;

-- name: ListLatestAgents :many
SELECT DISTINCT ON (id)
    agents.*
FROM agents
ORDER BY id, version DESC;

-- name: LockActiveAgentVersion :one
SELECT id
FROM agents
WHERE id = @id AND version = @version
  AND workspace_id = @workspace_id
  AND archived_at IS NULL
FOR SHARE;

-- name: ArchiveAgent :execrows
UPDATE agents
SET archived_at = COALESCE(archived_at, @archived_at)
WHERE id = @id AND workspace_id = @workspace_id;

-- name: UpsertEnvironment :execrows
INSERT INTO environments (
    id, name, config_type, body, created_at, updated_at, archived_at, workspace_id
)
VALUES (
    @id, @name, @config_type, @body, @created_at, @updated_at, @archived_at, @workspace_id
)
ON CONFLICT (id) DO NOTHING;

-- name: GetEnvironment :one
SELECT environments.*
FROM environments
WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListEnvironments :many
SELECT environments.*
FROM environments
ORDER BY id;

-- name: LockActiveEnvironment :one
SELECT id
FROM environments
WHERE id = @id AND workspace_id = @workspace_id AND archived_at IS NULL
FOR SHARE;

-- name: DeleteEnvironmentIfUnreferenced :execrows
DELETE FROM environments AS environment
WHERE environment.id = @id
  AND environment.workspace_id = @workspace_id
  AND NOT EXISTS (
      SELECT 1
      FROM sessions
      WHERE sessions.environment_id = environment.id
        AND sessions.workspace_id = environment.workspace_id
  )
  AND NOT EXISTS (
      SELECT 1
      FROM deployments
      WHERE deployments.environment_id = environment.id
        AND deployments.workspace_id = environment.workspace_id
  );

-- name: EnvironmentExists :one
SELECT EXISTS(
    SELECT 1 FROM environments
    WHERE id = @id AND workspace_id = @workspace_id
);

-- name: UpdateSessionProjection :exec
UPDATE sessions
SET status = @status,
    body = @body,
    updated_at = @updated_at,
    archived_at = @archived_at
WHERE id = @id;

-- name: MarkSessionDeleting :exec
UPDATE sessions
SET deleting_at = COALESCE(deleting_at, @deleting_at)
WHERE id = @id;

-- name: DeleteMarkedSession :execrows
DELETE FROM sessions
WHERE id = @id AND deleting_at IS NOT NULL;

-- name: ListDeletingSessionIDs :many
SELECT id
FROM sessions
WHERE deleting_at IS NOT NULL
ORDER BY deleting_at, id
LIMIT @row_limit;
