package controlplane

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

// SessionThreadService exposes the durable primary execution identity. Child
// thread creation and delegation remain a separate multi-agent runtime slice.
type SessionThreadService struct {
	store *pg.Store
}

func NewSessionThreadService(store *pg.Store) *SessionThreadService {
	return &SessionThreadService{store: store}
}

func (s *SessionThreadService) Get(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.SessionThread, error) {
	return s.store.GetSessionThread(ctx, sessionID, threadID)
}

func (s *SessionThreadService) List(
	ctx context.Context,
	sessionID string,
	query app.SessionThreadListQuery,
) ([]domain.SessionThread, error) {
	return s.store.ListSessionThreads(ctx, sessionID, query)
}

// Archive maps the primary thread lifecycle to the Session lifecycle. This is
// the complete behavior for today's single-thread runtime and preserves the
// same idle-only admission fence as Archive Session.
func (s *SessionThreadService) Archive(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.SessionThread, error) {
	if _, err := s.store.GetSessionThread(ctx, sessionID, threadID); err != nil {
		return domain.SessionThread{}, err
	}
	if _, err := s.store.ArchiveSession(ctx, sessionID); err != nil {
		return domain.SessionThread{}, err
	}
	return s.store.GetSessionThread(ctx, sessionID, threadID)
}
