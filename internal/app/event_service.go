package app

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

type EventService struct {
	es  *store.EventStore
	hub *Hub
}

func NewEventService(es *store.EventStore, hub *Hub) *EventService {
	return &EventService{es: es, hub: hub}
}

func (s *EventService) Append(ctx context.Context, sessionID string, drafts []domain.EventDraft) ([]domain.Event, error) {
	// Persist first (commits inside EventStore.Append).
	out, err := s.es.Append(ctx, sessionID, drafts)
	if err != nil {
		return nil, err
	}
	s.PublishCommitted(out)
	return out, nil
}

// PublishCommitted publishes already-committed events in sequence order.
func (s *EventService) PublishCommitted(events []domain.Event) {
	for _, event := range events {
		s.hub.Publish(event.SessionID, event)
	}
}

// PublishPreview forwards a stream-only preview frame to the hub, which delivers
// it only to subscribers that opted in for its event type. Preview frames are a
// live side channel; they are never persisted and never appear in event history.
func (s *EventService) PublishPreview(sessionID string, f domain.PreviewFrame) {
	s.hub.PublishPreview(sessionID, f)
}

func (s *EventService) History(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]domain.Event, error) {
	return s.es.History(ctx, sessionID, afterSeq, limit)
}

// HistoryTail returns the newest `limit` events in chronological order. Used for
// bounded model projection so an over-limit session carries recent context.
func (s *EventService) HistoryTail(ctx context.Context, sessionID string, limit int) ([]domain.Event, error) {
	return s.es.HistoryTail(ctx, sessionID, limit)
}

func (s *EventService) CloseSession(sessionID string, terminal domain.Event) {
	s.hub.CloseSession(sessionID, terminal)
}

// Query lists events applying the public List Events filters.
func (s *EventService) Query(ctx context.Context, sessionID string, q store.EventQuery) ([]domain.Event, error) {
	return s.es.Query(ctx, sessionID, q)
}
