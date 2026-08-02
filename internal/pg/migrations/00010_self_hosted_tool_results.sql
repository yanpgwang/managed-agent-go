-- +goose Up
-- +goose StatementBegin

ALTER TABLE pending_actions
    DROP CONSTRAINT pending_actions_kind_check;
ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_kind_check
    CHECK (kind IN ('custom_tool_result', 'tool_confirmation', 'tool_result'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE pending_actions
    DROP CONSTRAINT pending_actions_kind_check;
ALTER TABLE pending_actions
    ADD CONSTRAINT pending_actions_kind_check
    CHECK (kind IN ('custom_tool_result', 'tool_confirmation'));

-- +goose StatementEnd
