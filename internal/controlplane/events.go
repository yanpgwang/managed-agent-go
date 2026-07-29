package controlplane

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
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
	return s.store.QueryEvents(ctx, sessionID, query)
}
