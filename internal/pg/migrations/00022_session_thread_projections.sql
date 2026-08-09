-- +goose Up
-- +goose StatementBegin

-- A Session is the aggregate resource; each Session Thread owns its execution
-- projection. The primary projection is initially synchronized with the
-- single-thread Session runtime in the same transaction. Child execution can
-- later update one Thread and recompute the Session aggregate without changing
-- the Thread read model again.
ALTER TABLE session_threads
    ADD COLUMN status text,
    ADD COLUMN body jsonb,
    ADD COLUMN updated_at timestamptz;

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_check;

UPDATE session_threads AS thread
SET status = CASE
        WHEN session.archived_at IS NOT NULL THEN 'terminated'
        ELSE session.status
    END,
    body = jsonb_build_object(
        'ID', thread.id,
        'SessionID', thread.session_id,
        'ParentThreadID', thread.parent_thread_id,
        'Agent', session.body->'AgentSnapshot',
        'Status', CASE
            WHEN session.archived_at IS NOT NULL THEN 'terminated'
            ELSE session.status
        END,
        'Usage', COALESCE(session.body->'Usage', '{}'::jsonb),
        'ActiveSeconds', COALESCE(session.body->'ActiveSeconds', '0'::jsonb),
        'RunningSince', session.body->'RunningSince',
        'TerminatedAt', CASE
            WHEN session.archived_at IS NOT NULL THEN to_jsonb(session.archived_at)
            ELSE session.body->'TerminatedAt'
        END,
        'StartupSeconds', 0,
        'CreatedAt', to_jsonb(thread.created_at),
        'UpdatedAt', to_jsonb(session.updated_at),
        'ArchivedAt', to_jsonb(session.archived_at)
    ),
    updated_at = session.updated_at,
    archived_at = session.archived_at
FROM sessions AS session
WHERE session.id = thread.session_id;

ALTER TABLE session_threads
    ALTER COLUMN status SET NOT NULL,
    ALTER COLUMN body SET NOT NULL,
    ALTER COLUMN updated_at SET NOT NULL,
    ADD CONSTRAINT session_threads_status_check
        CHECK (status IN ('idle', 'running', 'rescheduling', 'terminated'));

ALTER TABLE session_threads
    ADD CONSTRAINT session_threads_parent_check CHECK (
        (kind = 'primary' AND parent_thread_id IS NULL) OR
        (kind = 'child' AND parent_thread_id IS NOT NULL)
    );

COMMENT ON COLUMN session_threads.body IS
    'Authoritative per-Thread agent, usage, and timing projection';
COMMENT ON COLUMN session_threads.archived_at IS
    'Independent Thread archive time; primary archive currently follows Session archive';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_parent_check,
    DROP CONSTRAINT session_threads_status_check;

UPDATE session_threads
SET archived_at = NULL
WHERE kind = 'primary';

ALTER TABLE session_threads
    ADD CONSTRAINT session_threads_check CHECK (
        (kind = 'primary' AND parent_thread_id IS NULL AND archived_at IS NULL) OR
        (kind = 'child' AND parent_thread_id IS NOT NULL)
    ),
    DROP COLUMN updated_at,
    DROP COLUMN body,
    DROP COLUMN status;

COMMENT ON COLUMN session_threads.archived_at IS
    'Independent child-thread archive time; primary lifecycle is projected from sessions';

-- +goose StatementEnd
