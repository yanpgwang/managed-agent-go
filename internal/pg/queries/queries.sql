-- Typed queries for the Temporal/PostgreSQL session path, compiled by sqlc into
-- internal/pg/pgstore. Transactions are kept explicit in Go; these are the
-- individual statements the admission, completion, cursor, and relay operations
-- compose under a single tx.

-- name: InsertSession :exec
INSERT INTO sessions (id, status, body, created_at, updated_at)
VALUES (@id, @status, @body, @created_at, @updated_at);

-- name: GetSession :one
SELECT id, status, body, created_at, updated_at
FROM sessions
WHERE id = @id;

-- LockSession takes the per-session admission lock. Every admission and
-- completion for a session serializes on this row, which is what makes receipt
-- sequence assignment and the coalescing outbox upsert race-free.
-- name: LockSession :one
SELECT id, status, body, created_at, updated_at
FROM sessions
WHERE id = @id
FOR UPDATE;

-- name: UpdateSessionStatus :exec
UPDATE sessions
SET status = @status, body = @body, updated_at = @updated_at
WHERE id = @id;

-- MaxEventSeq returns the current highest receipt sequence for a session, or 0
-- when the session has no events yet. Called while holding the admission lock.
-- name: MaxEventSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint AS max_seq
FROM events
WHERE session_id = @session_id;

-- name: InsertEvent :exec
INSERT INTO events (id, session_id, seq, type, payload, turn_event_id, created_at, processed_at)
VALUES (@id, @session_id, @seq, @type, @payload, @turn_event_id, @created_at, @processed_at);

-- ListEventsAfter returns events with seq strictly greater than a cursor, in
-- ascending receipt order. This is how the SessionWorkflow consumes the ledger
-- after its durable cursor; the limit bounds one wakeup's batch.
-- name: ListEventsAfter :many
SELECT id, session_id, seq, type, payload, turn_event_id, created_at, processed_at
FROM events
WHERE session_id = @session_id AND seq > @after_seq
ORDER BY seq
LIMIT @row_limit;

-- ListEventsByTurn returns the output events a completed turn committed,
-- identified by the trigger event id that caused them, in receipt order. The
-- idempotent completion path replays these when a turn is already processed.
-- name: ListEventsByTurn :many
SELECT id, session_id, seq, type, payload, turn_event_id, created_at, processed_at
FROM events
WHERE session_id = @session_id AND turn_event_id = @turn_event_id
ORDER BY seq;

-- name: GetEvent :one
SELECT id, session_id, seq, type, payload, turn_event_id, created_at, processed_at
FROM events
WHERE session_id = @session_id AND id = @id;

-- PriorProcessedUserTriggers returns the processed user.message events before a
-- given sequence, in receipt order. These are the prior turns whose causal
-- history (trigger followed by its committed outputs) is replayed into the model
-- for the current turn.
-- name: PriorProcessedUserTriggers :many
SELECT id, session_id, seq, type, payload, turn_event_id, created_at, processed_at
FROM events
WHERE session_id = @session_id
  AND type = 'user.message'
  AND processed_at IS NOT NULL
  AND seq < @before_seq
ORDER BY seq;

-- CountUnprocessedUserMessages counts user.message events still awaiting a turn,
-- excluding one id (the trigger just processed in the same transaction). It lets
-- CompleteTurn decide whether this turn is the last: only then does the session
-- go idle; otherwise it stays running with no intermediate idle event.
-- name: CountUnprocessedUserMessages :one
SELECT COUNT(*)::int AS n
FROM events
WHERE session_id = @session_id
  AND type = 'user.message'
  AND processed_at IS NULL
  AND id <> @exclude_id;

-- MarkEventProcessed stamps a trigger event processed, but only once
-- (COALESCE keeps the first timestamp). Returns the row so the caller can tell a
-- first processing from a repeat.
-- name: MarkEventProcessed :exec
UPDATE events
SET processed_at = COALESCE(processed_at, @processed_at)
WHERE session_id = @session_id AND id = @id;

-- UpsertOutbox writes or coalesces the pending wakeup for a session. When a
-- wakeup is already pending, it keeps the newer enqueue time and raises
-- max_event_seq to the highest known receipt sequence rather than adding a row.
-- name: UpsertOutbox :exec
INSERT INTO orchestration_outbox (session_id, max_event_seq, enqueued_at)
VALUES (@session_id, @max_event_seq, @enqueued_at)
ON CONFLICT (session_id) DO UPDATE
SET max_event_seq = GREATEST(orchestration_outbox.max_event_seq, EXCLUDED.max_event_seq),
    enqueued_at   = EXCLUDED.enqueued_at;

-- name: GetOutbox :one
SELECT session_id, max_event_seq, enqueued_at, attempts, last_attempt_at, last_error
FROM orchestration_outbox
WHERE session_id = @session_id;

-- ListOutboxBatch reads the oldest pending wakeups for delivery, oldest first.
-- This is a plain read, not a lease/claim: delivery to Temporal happens outside
-- any transaction, so two relay instances can read the same row and both send a
-- Signal. That is deliberately harmless — delivery is at-least-once and the
-- SessionWorkflow deduplicates by receipt sequence (a wakeup at or below its
-- cursor is a no-op). A delivered row is removed only by DeleteOutboxIfSeq, which
-- is itself guarded by the sequence.
-- name: ListOutboxBatch :many
SELECT session_id, max_event_seq, enqueued_at, attempts, last_attempt_at, last_error
FROM orchestration_outbox
ORDER BY enqueued_at
LIMIT @row_limit;

-- DeleteOutboxIfSeq removes a delivered wakeup, but only if no later admission
-- raised its sequence since it was read. A mismatch means new work coalesced
-- into the row after the relay signaled, so the row is left for the next cycle
-- and re-delivered with the higher sequence (a harmless duplicate wakeup).
-- name: DeleteOutboxIfSeq :execrows
DELETE FROM orchestration_outbox
WHERE session_id = @session_id AND max_event_seq = @max_event_seq;

-- MarkOutboxAttempt records a failed delivery attempt for backoff and
-- observability without removing the wakeup.
-- name: MarkOutboxAttempt :exec
UPDATE orchestration_outbox
SET attempts = attempts + 1, last_attempt_at = @last_attempt_at, last_error = @last_error
WHERE session_id = @session_id;
