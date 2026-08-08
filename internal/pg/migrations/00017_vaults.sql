-- +goose Up
-- +goose StatementBegin

CREATE TABLE vaults (
    id           text        PRIMARY KEY,
    display_name text        NOT NULL,
    metadata     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    archived_at  timestamptz
);

CREATE INDEX vaults_list_idx
    ON vaults (created_at DESC, id DESC);

CREATE TABLE vault_credentials (
    id                text        PRIMARY KEY,
    vault_id          text        NOT NULL REFERENCES vaults (id) ON DELETE CASCADE,
    display_name      text,
    metadata          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    auth_type         text        NOT NULL CHECK (auth_type IN ('mcp_oauth', 'static_bearer')),
    credential_key    text        NOT NULL,
    public_auth       jsonb       NOT NULL,
    secret_version    integer,
    secret_algorithm  text,
    secret_key_id     text,
    secret_nonce      bytea,
    secret_ciphertext bytea,
    version           bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    archived_at       timestamptz,
    CHECK (
        (archived_at IS NULL
            AND secret_version IS NOT NULL
            AND secret_algorithm IS NOT NULL
            AND secret_key_id IS NOT NULL
            AND secret_nonce IS NOT NULL
            AND secret_ciphertext IS NOT NULL)
        OR
        (archived_at IS NOT NULL
            AND secret_version IS NULL
            AND secret_algorithm IS NULL
            AND secret_key_id IS NULL
            AND secret_nonce IS NULL
            AND secret_ciphertext IS NULL)
    )
);

CREATE UNIQUE INDEX vault_credentials_active_key_idx
    ON vault_credentials (vault_id, credential_key)
    WHERE archived_at IS NULL;

CREATE INDEX vault_credentials_list_idx
    ON vault_credentials (vault_id, created_at DESC, id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS vault_credentials;
DROP TABLE IF EXISTS vaults;
-- +goose StatementEnd
