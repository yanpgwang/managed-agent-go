-- +goose Up
-- +goose StatementBegin

-- A child Workflow wakeup and shutdown share one coalescing key. Termination
-- permanently dominates a stale wake so a relay that raced with archival can
-- never restart durable work after the Thread lifecycle fence committed.
ALTER TABLE thread_orchestration_outbox
    ADD COLUMN intent text NOT NULL DEFAULT 'wake'
        CHECK (intent IN ('wake', 'terminate'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE thread_orchestration_outbox
    DROP COLUMN intent;

-- +goose StatementEnd
