-- Typed resource queries for the PostgreSQL HTTP control plane.

-- name: InsertAgentVersion :exec
INSERT INTO agents (id, version, name, body, created_at, updated_at, archived_at)
VALUES (@id, @version, @name, @body, @created_at, @updated_at, @archived_at);

-- name: LockLatestAgent :one
SELECT id, version, name, body, created_at, updated_at, archived_at
FROM agents
WHERE id = @id
ORDER BY version DESC
LIMIT 1
FOR UPDATE;

-- name: GetLatestAgent :one
SELECT id, version, name, body, created_at, updated_at, archived_at
FROM agents
WHERE id = @id
ORDER BY version DESC
LIMIT 1;

-- name: GetAgentVersion :one
SELECT id, version, name, body, created_at, updated_at, archived_at
FROM agents
WHERE id = @id AND version = @version;

-- name: ListAgentVersions :many
SELECT id, version, name, body, created_at, updated_at, archived_at
FROM agents
WHERE id = @id
ORDER BY version;

-- List Agents and List Environments are keyset-paginated with request-dependent
-- filters, so their statements are composed in internal/pg/api_store.go beside
-- List Sessions rather than being pinned here as static queries.

-- name: LockActiveAgentVersion :one
SELECT id
FROM agents
WHERE id = @id AND version = @version AND archived_at IS NULL
FOR SHARE;

-- name: ArchiveAgent :execrows
UPDATE agents
SET archived_at = COALESCE(archived_at, @archived_at)
WHERE id = @id;

-- name: UpsertEnvironment :exec
INSERT INTO environments (
    id, name, config_type, body, created_at, updated_at, archived_at
)
VALUES (
    @id, @name, @config_type, @body, @created_at, @updated_at, @archived_at
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    config_type = EXCLUDED.config_type,
    body = EXCLUDED.body,
    updated_at = EXCLUDED.updated_at,
    archived_at = EXCLUDED.archived_at;

-- name: GetEnvironment :one
SELECT id, name, config_type, body, created_at, updated_at, archived_at
FROM environments
WHERE id = @id;

-- name: LockActiveEnvironment :one
SELECT id
FROM environments
WHERE id = @id AND archived_at IS NULL
FOR SHARE;

-- name: DeleteEnvironmentIfUnreferenced :execrows
DELETE FROM environments AS environment
WHERE environment.id = @id
  AND NOT EXISTS (
      SELECT 1
      FROM sessions
      WHERE sessions.environment_id = environment.id
  );

-- name: EnvironmentExists :one
SELECT EXISTS(SELECT 1 FROM environments WHERE id = @id);

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
