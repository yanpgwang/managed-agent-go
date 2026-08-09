-- +goose Up
-- +goose StatementBegin

-- Child Session Threads run in independent Temporal Workflows. Keep their
-- coalescible wakeups separate from the legacy primary-Session outbox so an
-- upgrade never changes an existing Workflow ID or delivery key.
CREATE TABLE thread_orchestration_outbox (
    session_id      text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    thread_id       text        NOT NULL,
    max_event_seq   bigint      NOT NULL,
    enqueued_at     timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,
    last_error      text,
    PRIMARY KEY (session_id, thread_id),
    FOREIGN KEY (session_id, thread_id)
        REFERENCES session_threads (session_id, id) ON DELETE CASCADE
);

CREATE INDEX thread_orchestration_outbox_enqueued_idx
    ON thread_orchestration_outbox (enqueued_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS thread_orchestration_outbox;

-- +goose StatementEnd
