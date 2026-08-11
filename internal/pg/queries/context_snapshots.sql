-- Immutable Thread context snapshot queries.

-- name: InsertThreadContextSnapshot :exec
INSERT INTO thread_context_snapshots (
    id,
    session_id,
    thread_id,
    trigger_event_id,
    parent_snapshot_id,
    transcript_trigger_event_ids,
    messages,
    projection,
    context_policy_version,
    created_at
)
VALUES (
    @id,
    @session_id,
    @thread_id,
    @trigger_event_id,
    @parent_snapshot_id,
    @transcript_trigger_event_ids,
    @messages,
    @projection,
    @context_policy_version,
    @created_at
)
ON CONFLICT (session_id, thread_id, trigger_event_id) DO NOTHING;

-- name: GetThreadContextSnapshotForTrigger :one
SELECT *
FROM thread_context_snapshots
WHERE session_id = @session_id
  AND thread_id = @thread_id
  AND trigger_event_id = @trigger_event_id;

-- name: LatestThreadContextSnapshot :one
SELECT *
FROM thread_context_snapshots
WHERE session_id = @session_id
  AND thread_id = @thread_id
ORDER BY snapshot_ordinal DESC
LIMIT 1;
