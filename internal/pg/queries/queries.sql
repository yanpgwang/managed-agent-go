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

-- ClaimOutboxBatch reads the oldest pending wakeups for delivery. SKIP LOCKED
-- lets multiple relay workers cooperate without blocking on each other; a row
-- claimed by one relay is invisible to another for the duration of its tx.
-- name: ClaimOutboxBatch :many
SELECT session_id, max_event_seq, enqueued_at, attempts, last_attempt_at, last_error
FROM orchestration_outbox
ORDER BY enqueued_at
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

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
