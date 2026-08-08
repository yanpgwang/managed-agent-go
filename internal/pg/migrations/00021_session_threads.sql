-- +goose Up
-- +goose StatementBegin

-- Every Session has a durable primary thread identity. Its mutable execution
-- projection deliberately remains the Session projection: the primary thread
-- and the Session event stream are the same execution according to the public
-- contract. Child-thread runtime state can extend this table without copying
-- the existing Session event ledger.
CREATE TABLE session_threads (
    id               text        PRIMARY KEY,
    session_id       text        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    parent_thread_id text,
    kind             text        NOT NULL CHECK (kind IN ('primary', 'child')),
    created_at       timestamptz NOT NULL,
    archived_at      timestamptz,
    UNIQUE (session_id, id),
    FOREIGN KEY (session_id, parent_thread_id)
        REFERENCES session_threads (session_id, id),
    CHECK (
        (kind = 'primary' AND parent_thread_id IS NULL AND archived_at IS NULL) OR
        (kind = 'child' AND parent_thread_id IS NOT NULL)
    )
);

COMMENT ON COLUMN session_threads.archived_at IS
    'Independent child-thread archive time; primary lifecycle is projected from sessions';

CREATE UNIQUE INDEX session_threads_primary_idx
    ON session_threads (session_id)
    WHERE kind = 'primary';
CREATE INDEX session_threads_order_idx
    ON session_threads (session_id, kind, created_at, id);

-- Existing Sessions gain stable, deterministic primary identities. New rows
-- use the normal random/sequence ID generator in the application transaction.
INSERT INTO session_threads (id, session_id, parent_thread_id, kind, created_at)
SELECT 'sthr_' || substr(md5(id), 1, 24), id, NULL, 'primary', created_at
FROM sessions;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS session_threads;
-- +goose StatementEnd
