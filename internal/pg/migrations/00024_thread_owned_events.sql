-- +goose Up
-- +goose StatementBegin

-- Events retain a Session-wide receipt sequence for deterministic aggregate
-- ordering, but each durable event belongs to exactly one Session Thread. The
-- Session event surface reads the primary Thread ledger; child lifecycle
-- cross-posts are separate events owned by that primary ledger.
ALTER TABLE events
    ADD COLUMN thread_id text;

UPDATE events AS event
SET thread_id = thread.id
FROM session_threads AS thread
WHERE thread.session_id = event.session_id
  AND thread.kind = 'primary';

ALTER TABLE events
    ALTER COLUMN thread_id SET NOT NULL,
    ADD CONSTRAINT events_session_thread_fkey
        FOREIGN KEY (session_id, thread_id)
        REFERENCES session_threads (session_id, id);

CREATE INDEX events_thread_seq_idx
    ON events (session_id, thread_id, seq);

COMMENT ON COLUMN events.thread_id IS
    'Owning Session Thread ledger; Session-wide seq remains the total-order cursor';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS events_thread_seq_idx;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_session_thread_fkey,
    DROP COLUMN IF EXISTS thread_id;

-- +goose StatementEnd
