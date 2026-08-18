-- +goose Up
-- +goose StatementBegin

-- Workspace is Mango's sole tenant and authorization boundary. The fixed
-- bootstrap Workspace owns every pre-tenancy row so this pre-1.0 migration is
-- deterministic and preserves the existing single-tenant installation.
CREATE TABLE workspaces (
    id         text        PRIMARY KEY,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

INSERT INTO workspaces (id, name, created_at, updated_at)
VALUES (
    'wrkspc_default',
    'Default Workspace',
    '1970-01-01 00:00:00+00',
    '1970-01-01 00:00:00+00'
);

-- Only a SHA-256 digest of the high-entropy API key is persisted. A key is
-- bound to exactly one Workspace and never grants a narrower resource scope.
CREATE TABLE api_keys (
    id           text        PRIMARY KEY,
    workspace_id text        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    secret_hash  bytea       NOT NULL UNIQUE,
    label        text        NOT NULL,
    created_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    last_used_at timestamptz
);

CREATE INDEX api_keys_workspace_idx ON api_keys (workspace_id, created_at, id);

ALTER TABLE agents ADD COLUMN workspace_id text;
ALTER TABLE environments ADD COLUMN workspace_id text;
ALTER TABLE sessions ADD COLUMN workspace_id text;
ALTER TABLE files ADD COLUMN workspace_id text;
ALTER TABLE skills ADD COLUMN workspace_id text;
ALTER TABLE memory_stores ADD COLUMN workspace_id text;
ALTER TABLE vaults ADD COLUMN workspace_id text;
ALTER TABLE deployments ADD COLUMN workspace_id text;

UPDATE agents SET workspace_id = 'wrkspc_default';
UPDATE environments SET workspace_id = 'wrkspc_default';
UPDATE sessions SET workspace_id = 'wrkspc_default';
UPDATE files SET workspace_id = 'wrkspc_default';
UPDATE skills SET workspace_id = 'wrkspc_default';
UPDATE memory_stores SET workspace_id = 'wrkspc_default';
UPDATE vaults SET workspace_id = 'wrkspc_default';
UPDATE deployments SET workspace_id = 'wrkspc_default';

ALTER TABLE agents ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE environments ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE sessions ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE files ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE skills ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE memory_stores ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE vaults ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE deployments ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE agents
    ADD CONSTRAINT agents_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE environments
    ADD CONSTRAINT environments_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE sessions
    ADD CONSTRAINT sessions_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE files
    ADD CONSTRAINT files_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE skills
    ADD CONSTRAINT skills_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE memory_stores
    ADD CONSTRAINT memory_stores_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE vaults
    ADD CONSTRAINT vaults_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;
ALTER TABLE deployments
    ADD CONSTRAINT deployments_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES workspaces (id) ON DELETE RESTRICT;

-- Keep dependency ownership correct even if a future write path bypasses the
-- repository guards. The original global identities remain unchanged; these
-- redundant unique keys only make Workspace part of each relational reference.
ALTER TABLE agents
    ADD CONSTRAINT agents_workspace_identity_unique
    UNIQUE (id, version, workspace_id);
ALTER TABLE environments
    ADD CONSTRAINT environments_workspace_identity_unique
    UNIQUE (id, workspace_id);
ALTER TABLE deployments
    ADD CONSTRAINT deployments_workspace_identity_unique
    UNIQUE (id, workspace_id);

-- Session Agent and Environment columns intentionally remain snapshots rather
-- than foreign keys: the durable execution path can admit a fully resolved
-- Session without mutable API resources. Public Session creation locks and
-- verifies those dependencies in its Workspace before reaching this path.
ALTER TABLE sessions
    ADD CONSTRAINT sessions_deployment_workspace_fk
    FOREIGN KEY (deployment_id, workspace_id)
    REFERENCES deployments (id, workspace_id) NOT VALID;
ALTER TABLE deployments
    ADD CONSTRAINT deployments_agent_workspace_fk
    FOREIGN KEY (agent_id, agent_version, workspace_id)
    REFERENCES agents (id, version, workspace_id) NOT VALID;
ALTER TABLE deployments
    ADD CONSTRAINT deployments_environment_workspace_fk
    FOREIGN KEY (environment_id, workspace_id)
    REFERENCES environments (id, workspace_id) NOT VALID;

-- NOT VALID avoids checking each table while the constraint is installed.
-- The deterministic backfill above makes validation safe in this migration,
-- and validating here ensures pre-tenancy rows receive the same guarantee as
-- all rows written after the migration.
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_deployment_workspace_fk;
ALTER TABLE deployments VALIDATE CONSTRAINT deployments_agent_workspace_fk;
ALTER TABLE deployments VALIDATE CONSTRAINT deployments_environment_workspace_fk;

CREATE INDEX agents_workspace_list_idx
    ON agents (workspace_id, created_at DESC, id, version DESC);
CREATE INDEX environments_workspace_list_idx
    ON environments (workspace_id, created_at DESC, id DESC);
CREATE INDEX sessions_workspace_list_idx
    ON sessions (workspace_id, created_at DESC, id DESC);
CREATE INDEX files_workspace_list_idx
    ON files (workspace_id, created_at DESC, id DESC);
CREATE INDEX skills_workspace_list_idx
    ON skills (workspace_id, created_at DESC, id DESC);
CREATE INDEX memory_stores_workspace_list_idx
    ON memory_stores (workspace_id, created_at DESC, id DESC);
CREATE INDEX vaults_workspace_list_idx
    ON vaults (workspace_id, created_at DESC, id DESC);
CREATE INDEX deployments_workspace_list_idx
    ON deployments (workspace_id, created_at DESC, id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_environment_workspace_fk;
ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_agent_workspace_fk;
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_deployment_workspace_fk;
ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_workspace_identity_unique;
ALTER TABLE environments DROP CONSTRAINT IF EXISTS environments_workspace_identity_unique;
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_workspace_identity_unique;

DROP INDEX IF EXISTS deployments_workspace_list_idx;
DROP INDEX IF EXISTS vaults_workspace_list_idx;
DROP INDEX IF EXISTS memory_stores_workspace_list_idx;
DROP INDEX IF EXISTS skills_workspace_list_idx;
DROP INDEX IF EXISTS files_workspace_list_idx;
DROP INDEX IF EXISTS sessions_workspace_list_idx;
DROP INDEX IF EXISTS environments_workspace_list_idx;
DROP INDEX IF EXISTS agents_workspace_list_idx;

ALTER TABLE deployments DROP COLUMN workspace_id;
ALTER TABLE vaults DROP COLUMN workspace_id;
ALTER TABLE memory_stores DROP COLUMN workspace_id;
ALTER TABLE skills DROP COLUMN workspace_id;
ALTER TABLE files DROP COLUMN workspace_id;
ALTER TABLE sessions DROP COLUMN workspace_id;
ALTER TABLE environments DROP COLUMN workspace_id;
ALTER TABLE agents DROP COLUMN workspace_id;

DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS workspaces;
-- +goose StatementEnd
