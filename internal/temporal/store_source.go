package temporal

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

// storeSource adapts *pg.Store to the EventSource interface the Activities
// depend on. It exists so the temporal package depends on a narrow interface
// rather than the pg package's concrete completion type, keeping the domain
// boundary clean (no Temporal wire types leak into pg, no pg types into the
// workflow contract).
type storeSource struct{ store *pg.Store }

// NewStoreSource wraps a PostgreSQL store as an Activity EventSource.
func NewStoreSource(store *pg.Store) EventSource { return storeSource{store: store} }

func (s storeSource) EventsAfter(ctx context.Context, sessionID string, cursor int64, limit int) ([]domain.Event, error) {
	return s.store.EventsAfter(ctx, sessionID, cursor, limit)
}

func (s storeSource) HistoryThrough(ctx context.Context, sessionID string, seq int64, limit int) ([]domain.Event, error) {
	return s.store.HistoryThrough(ctx, sessionID, seq, limit)
}

func (s storeSource) GetSession(ctx context.Context, id string) (domain.Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s storeSource) GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error) {
	return s.store.GetEvent(ctx, sessionID, id)
}

func (s storeSource) CompleteTurn(ctx context.Context, sessionID, triggerEventID string, output []domain.EventDraft, status domain.Status) (TurnCompletionResult, error) {
	res, err := s.store.CompleteTurn(ctx, sessionID, triggerEventID, output, status)
	if err != nil {
		return TurnCompletionResult{}, err
	}
	return TurnCompletionResult{Events: res.Events, Applied: res.Applied}, nil
}

// storeSource also satisfies JournalStore, adapting BeginAttempt's rich return to
// the bare attempt id the Activity needs. The rest delegate directly.

func (s storeSource) RecoverTurn(ctx context.Context, sessionID, triggerEventID string) (bool, error) {
	return s.store.RecoverTurn(ctx, sessionID, triggerEventID)
}

func (s storeSource) BeginAttempt(ctx context.Context, sessionID, triggerEventID string) (string, error) {
	attempt, err := s.store.BeginAttempt(ctx, sessionID, triggerEventID)
	if err != nil {
		return "", err
	}
	return attempt.ID, nil
}

func (s storeSource) FinishAttempt(ctx context.Context, attemptID string, state domain.RunAttemptState, attemptError *string) error {
	return s.store.FinishAttempt(ctx, attemptID, state, attemptError)
}

func (s storeSource) PrepareToolStep(ctx context.Context, attemptID string, ordinal int, toolUseEventID, toolName string, input map[string]any) (string, error) {
	return s.store.PrepareToolStep(ctx, attemptID, ordinal, toolUseEventID, toolName, input)
}

func (s storeSource) StartToolStep(ctx context.Context, stepID string) error {
	return s.store.StartToolStep(ctx, stepID)
}

func (s storeSource) CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	return s.store.CompleteToolStep(ctx, stepID, result)
}
