-- +goose Up
CREATE TABLE session_vaults (
    session_id text NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position >= 0),
    vault_id text NOT NULL,
    PRIMARY KEY (session_id, position),
    UNIQUE (session_id, vault_id)
);

CREATE INDEX session_vaults_vault_idx ON session_vaults (vault_id);

-- +goose Down
DROP TABLE session_vaults;
