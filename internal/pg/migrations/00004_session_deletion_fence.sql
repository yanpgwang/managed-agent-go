-- +goose Up
-- +goose StatementBegin

-- DELETE is a two-phase lifecycle operation: first fence new admission, then
-- terminate the Temporal Workflow, then remove the projection. Keeping the
-- fence relational (rather than in the public JSON body) makes it an internal
-- control-plane fact and lets admission check it under the same row lock.
ALTER TABLE sessions
    ADD COLUMN deleting_at timestamptz;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions
    DROP COLUMN IF EXISTS deleting_at;
-- +goose StatementEnd
