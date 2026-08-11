-- +goose Up
-- +goose StatementBegin

-- Immutable compacted-context records owned by one Session Thread. Public
-- events remain the client-visible ledger; these records preserve the exact
-- private message projection recovered by a retried PrepareTurn Activity.
--
-- The trigger uniqueness makes Activity retries idempotent. The parent link is
-- constrained to the same Session Thread so independent child contexts can
-- never be joined accidentally.
ALTER TABLE events
    ADD CONSTRAINT events_session_thread_id_key
        UNIQUE (session_id, thread_id, id);

CREATE TABLE thread_context_snapshots (
    id                           text        PRIMARY KEY,
    session_id                   text        NOT NULL,
    thread_id                    text        NOT NULL,
    trigger_event_id             text        NOT NULL,
    parent_snapshot_id           text,
    snapshot_ordinal             bigint      GENERATED ALWAYS AS IDENTITY,
    transcript_trigger_event_ids jsonb       NOT NULL,
    messages                     jsonb       NOT NULL,
    projection                   jsonb       NOT NULL,
    context_policy_version       integer     NOT NULL CHECK (context_policy_version > 0),
    created_at                   timestamptz NOT NULL,
    UNIQUE (session_id, thread_id, trigger_event_id),
    UNIQUE (session_id, thread_id, id),
    FOREIGN KEY (session_id, thread_id)
        REFERENCES session_threads (session_id, id) ON DELETE CASCADE,
    FOREIGN KEY (session_id, thread_id, trigger_event_id)
        REFERENCES events (session_id, thread_id, id) ON DELETE CASCADE,
    FOREIGN KEY (session_id, thread_id, parent_snapshot_id)
        REFERENCES thread_context_snapshots (session_id, thread_id, id)
);

CREATE INDEX thread_context_snapshots_order_idx
    ON thread_context_snapshots (session_id, thread_id, snapshot_ordinal DESC);

COMMENT ON TABLE thread_context_snapshots IS
    'Private immutable compacted message projections; never a public Session resource';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS thread_context_snapshots;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_session_thread_id_key;

-- +goose StatementEnd
