-- +goose Up
-- +goose StatementBegin

-- Durable client-action gates for the Temporal/PostgreSQL path.
--
-- A row is created in the same transaction that commits the corresponding
-- agent.custom_tool_use / approval-gated agent.tool_use event and the
-- session.status_idle{requires_action} boundary. Admission later claims the row
-- with the matching client resolution event. resolved_at is set only when that
-- resume turn closes, so ordinary queued work cannot overtake an in-flight
-- resolution.
CREATE TABLE pending_actions (
    id                 text        PRIMARY KEY,
    session_id         text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    action_event_id    text        NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    kind               text        NOT NULL CHECK (kind IN ('custom_tool_result', 'tool_confirmation')),
    resolving_event_id text        REFERENCES events (id),
    created_at         timestamptz NOT NULL,
    resolved_at        timestamptz,
    UNIQUE (session_id, action_event_id),
    UNIQUE (session_id, resolving_event_id),
    CHECK (resolved_at IS NULL OR resolving_event_id IS NOT NULL)
);

CREATE INDEX pending_actions_unresolved_idx
    ON pending_actions (session_id, created_at, id)
    WHERE resolved_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS pending_actions;
-- +goose StatementEnd
