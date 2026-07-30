-- +goose Up
-- +goose StatementBegin

-- Durable ownership of the provider resource backing one session workspace.
-- Provider credentials stay in worker configuration; PostgreSQL stores only
-- the opaque external identity needed to attach after a worker restart.
--
-- The foreign key intentionally does not cascade. Session deletion must first
-- complete provider teardown and remove this binding, otherwise PostgreSQL
-- refuses to discard the last durable reference to a live sandbox.
CREATE TABLE session_sandboxes (
    session_id  text        PRIMARY KEY REFERENCES sessions (id),
    provider    text        NOT NULL,
    external_id text        NOT NULL,
    spec_hash   text        NOT NULL,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL,
    UNIQUE (provider, external_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS session_sandboxes;
-- +goose StatementEnd
