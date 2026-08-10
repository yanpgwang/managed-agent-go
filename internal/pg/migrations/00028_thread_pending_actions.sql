-- +goose Up
-- +goose StatementBegin

-- A requires-action barrier belongs to the Thread whose model emitted the
-- tool call. Child requests are projected onto the primary stream with a new
-- public event id, so retain both the Thread-local action id used by provider
-- context and the client-facing cross-post id used to route the response.
ALTER TABLE pending_actions
    ADD COLUMN thread_id text,
    ADD COLUMN client_action_event_id text;

UPDATE pending_actions AS pending
SET thread_id = action.thread_id,
    client_action_event_id = pending.action_event_id
FROM events AS action
WHERE action.session_id = pending.session_id
  AND action.id = pending.action_event_id;

ALTER TABLE pending_actions
    ALTER COLUMN thread_id SET NOT NULL,
    ALTER COLUMN client_action_event_id SET NOT NULL,
    ADD CONSTRAINT pending_actions_thread_fk
        FOREIGN KEY (session_id, thread_id)
        REFERENCES session_threads (session_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT pending_actions_client_event_fk
        FOREIGN KEY (client_action_event_id)
        REFERENCES events (id) ON DELETE CASCADE,
    ADD CONSTRAINT pending_actions_client_event_unique
        UNIQUE (session_id, client_action_event_id);

DROP INDEX pending_actions_unresolved_idx;
CREATE INDEX pending_actions_unresolved_idx
    ON pending_actions (session_id, thread_id, created_at, id)
    WHERE resolved_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX pending_actions_unresolved_idx;

ALTER TABLE pending_actions
    DROP CONSTRAINT pending_actions_client_event_unique,
    DROP CONSTRAINT pending_actions_client_event_fk,
    DROP CONSTRAINT pending_actions_thread_fk,
    DROP COLUMN client_action_event_id,
    DROP COLUMN thread_id;

CREATE INDEX pending_actions_unresolved_idx
    ON pending_actions (session_id, created_at, id)
    WHERE resolved_at IS NULL;

-- +goose StatementEnd
