package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// BeginAttempt creates the next immutable attempt number for a running logical
// run. At most one attempt may be active for a run at a time.
func (s *RunStore) BeginAttempt(ctx context.Context, runID string) (domain.RunAttempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunAttempt{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, runID)
	if err == sql.ErrNoRows {
		return domain.RunAttempt{}, domain.NotFound("run not found")
	}
	if err != nil {
		return domain.RunAttempt{}, err
	}
	if run.State != domain.RunRunning {
		return domain.RunAttempt{}, domain.Conflict("attempt requires a running run")
	}

	var active bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM run_attempts WHERE run_id=? AND state=?
)`, runID, string(domain.RunAttemptActive)).Scan(&active); err != nil {
		return domain.RunAttempt{}, err
	}
	if active {
		return domain.RunAttempt{}, domain.Conflict("run already has an active attempt")
	}
	var completed bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM run_attempts WHERE run_id=? AND state=?
)`, runID, string(domain.RunAttemptCompleted)).Scan(&completed); err != nil {
		return domain.RunAttempt{}, err
	}
	if completed {
		return domain.RunAttempt{}, domain.Conflict("run already has a completed attempt")
	}
	var executed bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM tool_steps AS step
  JOIN run_attempts AS attempt ON attempt.id=step.attempt_id
  WHERE attempt.run_id=? AND step.state IN (?, ?)
)`, runID, string(domain.ToolStepCompleted), string(domain.ToolStepAmbiguous)).Scan(&executed); err != nil {
		return domain.RunAttempt{}, err
	}
	if executed {
		return domain.RunAttempt{}, domain.Conflict("run has prior tool execution that requires recovery")
	}

	var attemptNo int
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(attempt_no), 0) + 1
FROM run_attempts
WHERE run_id=?`, runID).Scan(&attemptNo); err != nil {
		return domain.RunAttempt{}, err
	}
	now := s.clock.Now().UTC()
	attempt := domain.RunAttempt{
		ID:        s.ids.NewID(domain.PrefixRunAttempt),
		RunID:     runID,
		AttemptNo: attemptNo,
		State:     domain.RunAttemptActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO run_attempts
  (id, run_id, attempt_no, state, error, created_at, updated_at, finished_at)
VALUES (?, ?, ?, ?, NULL, ?, ?, NULL)`,
		attempt.ID, attempt.RunID, attempt.AttemptNo, string(attempt.State),
		timeVal(attempt.CreatedAt), timeVal(attempt.UpdatedAt)); err != nil {
		return domain.RunAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunAttempt{}, err
	}
	return attempt, nil
}

// FinishAttempt closes an active attempt without erasing its tool-step facts.
// A successful attempt requires every prepared step to have a durable result.
// No terminal attempt may retain a started step: recovery must first classify
// such a step completed or ambiguous.
func (s *RunStore) FinishAttempt(
	ctx context.Context,
	attemptID string,
	state domain.RunAttemptState,
	attemptError *string,
) (domain.RunAttempt, error) {
	switch state {
	case domain.RunAttemptCompleted:
		if attemptError != nil {
			return domain.RunAttempt{}, domain.Validation("completed attempt cannot carry an error")
		}
	case domain.RunAttemptFailed:
		if attemptError == nil || *attemptError == "" {
			return domain.RunAttempt{}, domain.Validation("failed attempt requires an error")
		}
	case domain.RunAttemptInterrupted:
		// An interrupt is not necessarily an error.
	default:
		return domain.RunAttempt{}, domain.Validation("invalid terminal attempt state")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunAttempt{}, err
	}
	defer tx.Rollback()

	attempt, err := getRunAttemptTx(ctx, tx, attemptID)
	if err == sql.ErrNoRows {
		return domain.RunAttempt{}, domain.NotFound("run attempt not found")
	}
	if err != nil {
		return domain.RunAttempt{}, err
	}
	if attempt.State != domain.RunAttemptActive {
		return domain.RunAttempt{}, domain.Conflict("run attempt is not active")
	}

	var started int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tool_steps WHERE attempt_id=? AND state=?`,
		attemptID, string(domain.ToolStepStarted)).Scan(&started); err != nil {
		return domain.RunAttempt{}, err
	}
	if started > 0 {
		return domain.RunAttempt{}, domain.Conflict("run attempt has unclassified started tool steps")
	}
	if state == domain.RunAttemptCompleted {
		var incomplete int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tool_steps WHERE attempt_id=? AND state<>?`,
			attemptID, string(domain.ToolStepCompleted)).Scan(&incomplete); err != nil {
			return domain.RunAttempt{}, err
		}
		if incomplete > 0 {
			return domain.RunAttempt{}, domain.Conflict("completed attempt has non-completed tool steps")
		}
	}

	now := s.clock.Now().UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE run_attempts
SET state=?, error=?, updated_at=?, finished_at=?
WHERE id=? AND state=?`,
		string(state), nullableString(attemptError), timeVal(now), timeVal(now),
		attemptID, string(domain.RunAttemptActive))
	if err != nil {
		return domain.RunAttempt{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.RunAttempt{}, err
	}
	if affected != 1 {
		return domain.RunAttempt{}, domain.Conflict("run attempt is not active")
	}
	if err := tx.Commit(); err != nil {
		return domain.RunAttempt{}, err
	}
	attempt.State = state
	attempt.Error = attemptError
	attempt.UpdatedAt = now
	attempt.FinishedAt = &now
	return attempt, nil
}

// PrepareToolStep persists the model's tool request before an executor is
// invoked. ordinal is stable within the attempt and ToolUseEventID is allocated
// from the public event ID space for later event correlation.
func (s *RunStore) PrepareToolStep(
	ctx context.Context,
	attemptID string,
	ordinal int,
	toolUseEventID string,
	toolName string,
	input map[string]any,
) (domain.ToolStep, error) {
	if ordinal < 0 {
		return domain.ToolStep{}, domain.Validation("tool step ordinal must be non-negative")
	}
	if toolUseEventID == "" {
		return domain.ToolStep{}, domain.Validation("tool step event id is required")
	}
	if toolName == "" {
		return domain.ToolStep{}, domain.Validation("tool step name is required")
	}
	if input == nil {
		return domain.ToolStep{}, domain.Validation("tool step input is required")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return domain.ToolStep{}, fmt.Errorf("store: encode tool step input: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ToolStep{}, err
	}
	defer tx.Rollback()

	attempt, err := getRunAttemptTx(ctx, tx, attemptID)
	if err == sql.ErrNoRows {
		return domain.ToolStep{}, domain.NotFound("run attempt not found")
	}
	if err != nil {
		return domain.ToolStep{}, err
	}
	if attempt.State != domain.RunAttemptActive {
		return domain.ToolStep{}, domain.Conflict("tool step requires an active attempt")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM tool_steps
  WHERE (attempt_id=? AND ordinal=?) OR tool_use_event_id=?
)`, attemptID, ordinal, toolUseEventID).Scan(&exists); err != nil {
		return domain.ToolStep{}, err
	}
	if exists {
		return domain.ToolStep{}, domain.Conflict("tool step ordinal or event id already exists")
	}

	now := s.clock.Now().UTC()
	step := domain.ToolStep{
		ID:             s.ids.NewID(domain.PrefixToolStep),
		AttemptID:      attemptID,
		Ordinal:        ordinal,
		ToolUseEventID: toolUseEventID,
		ToolName:       toolName,
		Input:          input,
		State:          domain.ToolStepPrepared,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tool_steps
  (id, attempt_id, ordinal, tool_use_event_id, tool_name, input, state, result,
   created_at, updated_at, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, NULL, NULL)`,
		step.ID, step.AttemptID, step.Ordinal, step.ToolUseEventID, step.ToolName,
		string(inputJSON), string(step.State), timeVal(step.CreatedAt), timeVal(step.UpdatedAt)); err != nil {
		return domain.ToolStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ToolStep{}, err
	}
	return step, nil
}

func (s *RunStore) StartToolStep(ctx context.Context, stepID string) (domain.ToolStep, error) {
	return s.transitionToolStep(ctx, stepID, domain.ToolStepPrepared, domain.ToolStepStarted, nil)
}

func (s *RunStore) CompleteToolStep(
	ctx context.Context,
	stepID string,
	result domain.ToolStepResult,
) (domain.ToolStep, error) {
	return s.transitionToolStep(ctx, stepID, domain.ToolStepStarted, domain.ToolStepCompleted, &result)
}

func (s *RunStore) MarkToolStepAmbiguous(ctx context.Context, stepID string) (domain.ToolStep, error) {
	return s.transitionToolStep(ctx, stepID, domain.ToolStepStarted, domain.ToolStepAmbiguous, nil)
}

func (s *RunStore) transitionToolStep(
	ctx context.Context,
	stepID string,
	from domain.ToolStepState,
	to domain.ToolStepState,
	stepResult *domain.ToolStepResult,
) (domain.ToolStep, error) {
	var resultJSON any
	if stepResult != nil {
		encoded, err := json.Marshal(stepResult)
		if err != nil {
			return domain.ToolStep{}, fmt.Errorf("store: encode tool step result: %w", err)
		}
		resultJSON = string(encoded)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ToolStep{}, err
	}
	defer tx.Rollback()

	step, err := getToolStepTx(ctx, tx, stepID)
	if err == sql.ErrNoRows {
		return domain.ToolStep{}, domain.NotFound("tool step not found")
	}
	if err != nil {
		return domain.ToolStep{}, err
	}
	if step.State != from {
		return domain.ToolStep{}, domain.Conflict("invalid tool step transition")
	}

	now := s.clock.Now().UTC()
	startedAt := nullableTime(step.StartedAt)
	finishedAt := nullableTime(step.FinishedAt)
	if to == domain.ToolStepStarted {
		startedAt = timeVal(now)
	}
	if to == domain.ToolStepCompleted || to == domain.ToolStepAmbiguous {
		finishedAt = timeVal(now)
	}
	updateResult := any(nil)
	if to == domain.ToolStepCompleted {
		updateResult = resultJSON
	}
	result, err := tx.ExecContext(ctx, `
UPDATE tool_steps
SET state=?, result=?, updated_at=?, started_at=?, finished_at=?
WHERE id=? AND state=?`,
		string(to), updateResult, timeVal(now), startedAt, finishedAt, stepID, string(from))
	if err != nil {
		return domain.ToolStep{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.ToolStep{}, err
	}
	if affected != 1 {
		return domain.ToolStep{}, domain.Conflict("invalid tool step transition")
	}
	if err := tx.Commit(); err != nil {
		return domain.ToolStep{}, err
	}
	step.State = to
	step.Result = stepResult
	step.UpdatedAt = now
	if to == domain.ToolStepStarted {
		step.StartedAt = &now
	}
	if to == domain.ToolStepCompleted || to == domain.ToolStepAmbiguous {
		step.FinishedAt = &now
	}
	return step, nil
}

func (s *RunStore) GetAttempt(ctx context.Context, attemptID string) (domain.RunAttempt, error) {
	attempt, err := getRunAttemptTx(ctx, s.db, attemptID)
	if err == sql.ErrNoRows {
		return domain.RunAttempt{}, domain.NotFound("run attempt not found")
	}
	return attempt, err
}

func (s *RunStore) ListAttempts(ctx context.Context, runID string) ([]domain.RunAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, attempt_no, state, error, created_at, updated_at, finished_at
FROM run_attempts
WHERE run_id=?
ORDER BY attempt_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []domain.RunAttempt
	for rows.Next() {
		attempt, err := scanRunAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (s *RunStore) GetToolStep(ctx context.Context, stepID string) (domain.ToolStep, error) {
	step, err := getToolStepTx(ctx, s.db, stepID)
	if err == sql.ErrNoRows {
		return domain.ToolStep{}, domain.NotFound("tool step not found")
	}
	return step, err
}

func (s *RunStore) ListToolSteps(ctx context.Context, attemptID string) ([]domain.ToolStep, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, attempt_id, ordinal, tool_use_event_id, tool_name, input, state, result,
       created_at, updated_at, started_at, finished_at
FROM tool_steps
WHERE attempt_id=?
ORDER BY ordinal`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []domain.ToolStep
	for rows.Next() {
		step, err := scanToolStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func getRunAttemptTx(ctx context.Context, q runQueryRower, attemptID string) (domain.RunAttempt, error) {
	return scanRunAttempt(q.QueryRowContext(ctx, `
SELECT id, run_id, attempt_no, state, error, created_at, updated_at, finished_at
FROM run_attempts
WHERE id=?`, attemptID))
}

func scanRunAttempt(row rowScanner) (domain.RunAttempt, error) {
	var (
		attempt                     domain.RunAttempt
		state, createdAt, updatedAt string
		attemptError, finishedAt    sql.NullString
	)
	if err := row.Scan(
		&attempt.ID, &attempt.RunID, &attempt.AttemptNo, &state, &attemptError,
		&createdAt, &updatedAt, &finishedAt,
	); err != nil {
		return domain.RunAttempt{}, err
	}
	attempt.State = domain.RunAttemptState(state)
	if attemptError.Valid {
		attempt.Error = &attemptError.String
	}
	var err error
	attempt.CreatedAt, err = parseRFC3339(createdAt)
	if err != nil {
		return domain.RunAttempt{}, err
	}
	attempt.UpdatedAt, err = parseRFC3339(updatedAt)
	if err != nil {
		return domain.RunAttempt{}, err
	}
	attempt.FinishedAt, err = parseNullableRFC3339(finishedAt)
	return attempt, err
}

func getToolStepTx(ctx context.Context, q runQueryRower, stepID string) (domain.ToolStep, error) {
	return scanToolStep(q.QueryRowContext(ctx, `
SELECT id, attempt_id, ordinal, tool_use_event_id, tool_name, input, state, result,
       created_at, updated_at, started_at, finished_at
FROM tool_steps
WHERE id=?`, stepID))
}

func scanToolStep(row rowScanner) (domain.ToolStep, error) {
	var (
		step                  domain.ToolStep
		inputJSON, state      string
		resultJSON            sql.NullString
		createdAt, updatedAt  string
		startedAt, finishedAt sql.NullString
	)
	if err := row.Scan(
		&step.ID, &step.AttemptID, &step.Ordinal, &step.ToolUseEventID,
		&step.ToolName, &inputJSON, &state, &resultJSON, &createdAt, &updatedAt,
		&startedAt, &finishedAt,
	); err != nil {
		return domain.ToolStep{}, err
	}
	if err := json.Unmarshal([]byte(inputJSON), &step.Input); err != nil {
		return domain.ToolStep{}, fmt.Errorf("store: decode tool step input: %w", err)
	}
	step.State = domain.ToolStepState(state)
	if resultJSON.Valid {
		var result domain.ToolStepResult
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return domain.ToolStep{}, fmt.Errorf("store: decode tool step result: %w", err)
		}
		step.Result = &result
	}
	var err error
	step.CreatedAt, err = parseRFC3339(createdAt)
	if err != nil {
		return domain.ToolStep{}, err
	}
	step.UpdatedAt, err = parseRFC3339(updatedAt)
	if err != nil {
		return domain.ToolStep{}, err
	}
	step.StartedAt, err = parseNullableRFC3339(startedAt)
	if err != nil {
		return domain.ToolStep{}, err
	}
	step.FinishedAt, err = parseNullableRFC3339(finishedAt)
	return step, err
}

func parseNullableRFC3339(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseRFC3339(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
