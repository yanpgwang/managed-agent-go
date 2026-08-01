-- +goose Up
-- +goose StatementBegin

-- A provisioning intent closes the provider-create / binding-commit crash
-- window. It is written before the worker calls Provider.Create and removed in
-- the same transaction that commits session_sandboxes. If the worker exits in
-- between, another worker can use the session-scoped provider identity and the
-- saved non-secret Spec to recover the resource and finish the binding.
--
-- The foreign key intentionally does not cascade. Session deletion must either
-- finish the binding or recover-and-destroy the possible provider resource
-- before PostgreSQL may discard the last reconciliation record.
CREATE TABLE sandbox_provisioning_intents (
    session_id text        PRIMARY KEY REFERENCES sessions (id),
    provider   text        NOT NULL,
    spec       jsonb       NOT NULL,
    spec_hash  text        NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX sandbox_provisioning_intents_scan_idx
    ON sandbox_provisioning_intents (provider, created_at, session_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS sandbox_provisioning_intents;
-- +goose StatementEnd
