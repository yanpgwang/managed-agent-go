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
