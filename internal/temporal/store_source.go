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

func (s storeSource) HistoryThrough(ctx context.Context, sessionID, triggerEventID string, limit int) ([]domain.Event, error) {
	return s.store.HistoryThrough(ctx, sessionID, triggerEventID, limit)
}

func (s storeSource) GetSession(ctx context.Context, id string) (domain.Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s storeSource) GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error) {
	return s.store.GetEvent(ctx, sessionID, id)
}

func (s storeSource) UnresolvedPendingActions(
	ctx context.Context,
	sessionID string,
) ([]domain.PendingAction, error) {
	return s.store.UnresolvedPendingActions(ctx, sessionID)
}

func (s storeSource) CompleteWorkflowTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
) (TurnCompletionResult, error) {
	res, err := s.store.CompleteWorkflowTurn(
		ctx,
		sessionID,
		triggerEventID,
		output,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
	)
	if err != nil {
		return TurnCompletionResult{}, err
	}
	return TurnCompletionResult{Events: res.Events, Applied: res.Applied, Status: res.Session.Status}, nil
}

// storeSource also satisfies JournalStore; the methods below delegate directly.

func (s storeSource) EnsureAttempt(ctx context.Context, sessionID, triggerEventID, attemptID string) error {
	_, err := s.store.EnsureAttempt(ctx, sessionID, triggerEventID, attemptID)
	return err
}

func (s storeSource) EnsureToolStep(
	ctx context.Context,
	attemptID string,
	stepID string,
	ordinal int,
	toolUseEventID string,
	toolName string,
	input map[string]any,
) (domain.ToolStep, error) {
	return s.store.EnsureToolStep(ctx, attemptID, stepID, ordinal, toolUseEventID, toolName, input)
}

func (s storeSource) StartToolStep(ctx context.Context, stepID string) error {
	return s.store.StartToolStep(ctx, stepID)
}

func (s storeSource) CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	return s.store.CompleteToolStep(ctx, stepID, result)
}

func (s storeSource) MarkToolStepAmbiguous(ctx context.Context, stepID string) error {
	return s.store.MarkToolStepAmbiguous(ctx, stepID)
}
