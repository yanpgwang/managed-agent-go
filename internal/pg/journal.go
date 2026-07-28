package pg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// The tool-execution journal preserves the same prepared/started/completed/
// ambiguous boundary as the SQLite execution store, but for the Temporal path.
// A turn is identified by (session_id, trigger_event_id); each RunTurn Activity
// execution is an attempt. A Temporal retry creates a new attempt rather than
// erasing the facts a prior attempt recorded.

// TurnAttempt is one durable execution attempt for a turn. It is internal
// bookkeeping and never serialized on the public API.
type TurnAttempt struct {
	ID             string
	SessionID      string
	TriggerEventID string
	AttemptNo      int
	State          domain.RunAttemptState
}

// BeginAttempt creates the next attempt for a turn. It refuses when an attempt is
// already active, or when a prior attempt already crossed the tool side-effect
// boundary (a started/completed/ambiguous step): such a turn must be recovered
// and classified, never freshly re-executed, so a side effect is not silently
// replayed. Call RecoverTurn first to classify leftovers.
func (s *Store) BeginAttempt(ctx context.Context, sessionID, triggerEventID string) (TurnAttempt, error) {
	var attempt TurnAttempt
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
		if _, err := q.ActiveAttemptForTurn(ctx, pgstore.ActiveAttemptForTurnParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		}); err == nil {
			return domain.Conflict("turn already has an active attempt")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		prior, err := q.PriorToolExecutionForTurn(ctx, pgstore.PriorToolExecutionForTurnParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		})
		if err != nil {
			return err
		}
		if prior {
			return domain.Conflict("turn has prior tool execution that requires recovery")
		}
		next, err := q.NextAttemptNo(ctx, pgstore.NextAttemptNoParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		})
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		attempt = TurnAttempt{
			ID:             s.ids.NewID(domain.PrefixRunAttempt),
			SessionID:      sessionID,
			TriggerEventID: triggerEventID,
			AttemptNo:      int(next),
			State:          domain.RunAttemptActive,
		}
		return q.InsertTurnAttempt(ctx, pgstore.InsertTurnAttemptParams{
			ID:             attempt.ID,
			SessionID:      sessionID,
			TriggerEventID: triggerEventID,
			AttemptNo:      next,
			State:          string(domain.RunAttemptActive),
			CreatedAt:      tsUTC(now),
			UpdatedAt:      tsUTC(now),
		})
	})
	if err != nil {
		return TurnAttempt{}, err
	}
	return attempt, nil
}

// FinishAttempt closes an active attempt. A completed attempt requires every step
// to carry a durable result, and no terminal attempt may retain a started step:
// recovery must first classify such a step completed or ambiguous.
func (s *Store) FinishAttempt(ctx context.Context, attemptID string, state domain.RunAttemptState, attemptError *string) error {
	switch state {
	case domain.RunAttemptCompleted:
		if attemptError != nil {
			return domain.Validation("completed attempt cannot carry an error")
		}
	case domain.RunAttemptFailed:
		if attemptError == nil || *attemptError == "" {
			return domain.Validation("failed attempt requires an error")
		}
	case domain.RunAttemptInterrupted:
		// An interrupt is not necessarily an error.
	default:
		return domain.Validation("invalid terminal attempt state")
	}

	return s.withTx(ctx, func(q *pgstore.Queries) error {
		started, err := q.CountStartedStepsForAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		if started > 0 {
			return domain.Conflict("attempt has unclassified started tool steps")
		}
		if state == domain.RunAttemptCompleted {
			incomplete, err := q.CountNonCompletedStepsForAttempt(ctx, attemptID)
			if err != nil {
				return err
			}
			if incomplete > 0 {
				return domain.Conflict("completed attempt has non-completed tool steps")
			}
		}
		now := s.clock.Now().UTC()
		affected, err := q.FinishTurnAttempt(ctx, pgstore.FinishTurnAttemptParams{
			State:      string(state),
			Error:      attemptError,
			UpdatedAt:  tsUTC(now),
			FinishedAt: tsUTC(now),
			ID:         attemptID,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.Conflict("attempt is not active")
		}
		return nil
	})
}

// PrepareToolStep persists the model's tool request before an executor runs.
func (s *Store) PrepareToolStep(ctx context.Context, attemptID string, ordinal int, toolUseEventID, toolName string, input map[string]any) (string, error) {
	if ordinal < 0 {
		return "", domain.Validation("tool step ordinal must be non-negative")
	}
	if toolUseEventID == "" {
		return "", domain.Validation("tool step event id is required")
	}
	if toolName == "" {
		return "", domain.Validation("tool step name is required")
	}
	if input == nil {
		return "", domain.Validation("tool step input is required")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	stepID := s.ids.NewID(domain.PrefixToolStep)
	err = s.withTx(ctx, func(q *pgstore.Queries) error {
		attempt, err := q.GetTurnAttempt(ctx, attemptID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("turn attempt not found")
		}
		if err != nil {
			return err
		}
		if attempt.State != string(domain.RunAttemptActive) {
			return domain.Conflict("tool step requires an active attempt")
		}
		conflict, err := q.ToolStepConflict(ctx, pgstore.ToolStepConflictParams{
			AttemptID: attemptID, Ordinal: int32(ordinal), ToolUseEventID: toolUseEventID,
		})
		if err != nil {
			return err
		}
		if conflict {
			return domain.Conflict("tool step ordinal or event id already exists")
		}
		now := s.clock.Now().UTC()
		return q.InsertToolStep(ctx, pgstore.InsertToolStepParams{
			ID:             stepID,
			AttemptID:      attemptID,
			Ordinal:        int32(ordinal),
			ToolUseEventID: toolUseEventID,
			ToolName:       toolName,
			Input:          inputJSON,
			CreatedAt:      tsUTC(now),
			UpdatedAt:      tsUTC(now),
		})
	})
	if err != nil {
		return "", err
	}
	return stepID, nil
}

// StartToolStep advances prepared -> started: the executor may now change the
// external world.
func (s *Store) StartToolStep(ctx context.Context, stepID string) error {
	return s.withTx(ctx, func(q *pgstore.Queries) error {
		now := s.clock.Now().UTC()
		affected, err := q.StartToolStep(ctx, pgstore.StartToolStepParams{
			StartedAt: tsUTC(now), UpdatedAt: tsUTC(now), ID: stepID,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.Conflict("invalid tool step transition")
		}
		return nil
	})
}

// CompleteToolStep advances started -> completed with a durable result.
func (s *Store) CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(q *pgstore.Queries) error {
		now := s.clock.Now().UTC()
		affected, err := q.CompleteToolStep(ctx, pgstore.CompleteToolStepParams{
			Result: resultJSON, FinishedAt: tsUTC(now), UpdatedAt: tsUTC(now), ID: stepID,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return domain.Conflict("invalid tool step transition")
		}
		return nil
	})
}

// RecoverTurn classifies leftovers from a crashed attempt: every started tool
// step for the turn is marked ambiguous (its side effect may have happened but no
// trustworthy result was recorded), and any still-active attempt is failed. It
// returns whether the turn now carries prior tool execution — in which case the
// turn must not be freshly re-run, only reported.
func (s *Store) RecoverTurn(ctx context.Context, sessionID, triggerEventID string) (hasPriorExecution bool, err error) {
	err = s.withTx(ctx, func(q *pgstore.Queries) error {
		startedIDs, err := q.StartedStepsForTurn(ctx, pgstore.StartedStepsForTurnParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		})
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		for _, id := range startedIDs {
			if _, err := q.MarkToolStepAmbiguous(ctx, pgstore.MarkToolStepAmbiguousParams{
				FinishedAt: tsUTC(now), UpdatedAt: tsUTC(now), ID: id,
			}); err != nil {
				return err
			}
		}
		// Fail any active attempt so a fresh BeginAttempt is not blocked by the
		// one-active guard once the turn is (correctly) refused.
		if activeID, err := q.ActiveAttemptForTurn(ctx, pgstore.ActiveAttemptForTurnParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		}); err == nil {
			msg := "recovered: attempt abandoned before completion"
			if _, err := q.FinishTurnAttempt(ctx, pgstore.FinishTurnAttemptParams{
				State: string(domain.RunAttemptFailed), Error: &msg,
				UpdatedAt: tsUTC(now), FinishedAt: tsUTC(now), ID: activeID,
			}); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		prior, err := q.PriorToolExecutionForTurn(ctx, pgstore.PriorToolExecutionForTurnParams{
			SessionID: sessionID, TriggerEventID: triggerEventID,
		})
		if err != nil {
			return err
		}
		hasPriorExecution = prior
		return nil
	})
	return hasPriorExecution, err
}

// ToolStepStateByEventID returns the state of the tool step correlated to a
// tool_use event id. Used by tests to assert the ambiguous classification.
func (s *Store) ToolStepStateByEventID(ctx context.Context, toolUseEventID string) (domain.ToolStepState, bool, error) {
	var state string
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM tool_steps WHERE tool_use_event_id = $1`, toolUseEventID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return domain.ToolStepState(state), true, nil
}
