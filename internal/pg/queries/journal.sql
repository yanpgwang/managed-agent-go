-- Typed queries for the Temporal-path tool-execution journal (turn_attempts,
-- tool_steps). Transitions keep an explicit from-state in the WHERE clause and
-- the Go layer checks rows-affected, mirroring the SQLite execution store's
-- guard semantics.

-- name: NextAttemptNo :one
SELECT (COALESCE(MAX(attempt_no), 0) + 1)::int AS next_no
FROM turn_attempts
WHERE session_id = @session_id AND trigger_event_id = @trigger_event_id;

-- name: InsertTurnAttempt :exec
INSERT INTO turn_attempts (id, session_id, trigger_event_id, attempt_no, state, created_at, updated_at)
VALUES (@id, @session_id, @trigger_event_id, @attempt_no, @state, @created_at, @updated_at);

-- name: GetTurnAttempt :one
SELECT id, session_id, trigger_event_id, attempt_no, state, error, created_at, updated_at, finished_at
FROM turn_attempts
WHERE id = @id
FOR UPDATE;

-- ActiveAttemptForTurn returns the id of the turn's active attempt, if any.
-- name: ActiveAttemptForTurn :one
SELECT id
FROM turn_attempts
WHERE session_id = @session_id AND trigger_event_id = @trigger_event_id AND state = 'active'
LIMIT 1
FOR UPDATE;

-- FinishTurnAttempt closes an active attempt. The from-state guard makes the
-- transition safe under concurrency; the Go caller checks rows-affected.
-- name: FinishTurnAttempt :execrows
UPDATE turn_attempts
SET state = @state, error = @error, updated_at = @updated_at, finished_at = @finished_at
WHERE id = @id AND state = 'active';

-- name: InsertToolStep :exec
INSERT INTO tool_steps
  (id, attempt_id, ordinal, tool_use_event_id, tool_name, input, state, created_at, updated_at)
VALUES (@id, @attempt_id, @ordinal, @tool_use_event_id, @tool_name, @input, 'prepared', @created_at, @updated_at);

-- ToolStepConflict reports whether a step with the same (attempt, ordinal) or the
-- same tool_use_event_id already exists.
-- name: ToolStepConflict :one
SELECT EXISTS(
  SELECT 1 FROM tool_steps
  WHERE (attempt_id = @attempt_id AND ordinal = @ordinal) OR tool_use_event_id = @tool_use_event_id
) AS conflict;

-- name: GetToolStep :one
SELECT id, attempt_id, ordinal, tool_use_event_id, tool_name, input, state, result,
       created_at, updated_at, started_at, finished_at
FROM tool_steps
WHERE id = @id;

-- StartToolStep advances prepared -> started, stamping started_at. The from-state
-- guard prevents re-starting, and the parent-attempt guard fences an overlapping
-- stale Activity after recovery has failed its attempt. The caller checks
-- rows-affected.
-- name: StartToolStep :execrows
UPDATE tool_steps AS ts
SET state = 'started', started_at = @started_at, updated_at = @updated_at
FROM turn_attempts AS ta
WHERE ts.id = @id
  AND ts.state = 'prepared'
  AND ta.id = ts.attempt_id
  AND ta.state = 'active';

-- CompleteToolStep advances started -> completed with a durable result.
-- name: CompleteToolStep :execrows
UPDATE tool_steps
SET state = 'completed', result = @result, finished_at = @finished_at, updated_at = @updated_at
WHERE id = @id AND state = 'started';

-- MarkToolStepAmbiguous advances started -> ambiguous (no trustworthy result).
-- name: MarkToolStepAmbiguous :execrows
UPDATE tool_steps
SET state = 'ambiguous', finished_at = @finished_at, updated_at = @updated_at
WHERE id = @id AND state = 'started';

-- CountStartedStepsForAttempt counts steps still in the started (unclassified)
-- state for an attempt — a terminal attempt must have none.
-- name: CountStartedStepsForAttempt :one
SELECT COUNT(*)::int AS n FROM tool_steps WHERE attempt_id = @attempt_id AND state = 'started';

-- CountNonCompletedStepsForAttempt counts steps not in completed state — a
-- completed attempt requires every step to carry a durable result.
-- name: CountNonCompletedStepsForAttempt :one
SELECT COUNT(*)::int AS n FROM tool_steps WHERE attempt_id = @attempt_id AND state <> 'completed';

-- StartedStepsForTurn returns the ids of every started (unclassified) tool step
-- across all attempts of a turn — the recovery input.
-- name: StartedStepsForTurn :many
SELECT ts.id
FROM tool_steps ts
JOIN turn_attempts ta ON ta.id = ts.attempt_id
WHERE ta.session_id = @session_id AND ta.trigger_event_id = @trigger_event_id AND ts.state = 'started';

-- PriorToolExecutionForTurn reports whether any tool step across the turn's
-- attempts has crossed the side-effect boundary (started/completed/ambiguous),
-- i.e. the turn cannot be freshly re-run without risking a silent replay.
-- name: PriorToolExecutionForTurn :one
SELECT EXISTS(
  SELECT 1
  FROM tool_steps ts
  JOIN turn_attempts ta ON ta.id = ts.attempt_id
  WHERE ta.session_id = @session_id AND ta.trigger_event_id = @trigger_event_id
    AND ts.state IN ('started', 'completed', 'ambiguous')
) AS prior;
