package controlplane

import (
	"context"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/pg"
)

// EventService exposes filtered reads from the authoritative PostgreSQL event
// ledger.
type EventService struct {
	store *pg.Store
}

func NewEventService(store *pg.Store) *EventService {
	return &EventService{store: store}
}

func (s *EventService) Query(
	ctx context.Context,
	sessionID string,
	query app.EventQuery,
) ([]domain.Event, error) {
	if err := s.store.AssertSessionWorkspace(ctx, sessionID); err != nil {
		return nil, err
	}
	return s.store.QueryEvents(ctx, sessionID, query)
}
