package controlplane

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

// SessionThreadService exposes independently persisted execution projections.
// Child creation and execution remain a separate multi-agent runtime slice.
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

// Archive maps only the primary Thread lifecycle to the Session lifecycle. A
// child must never archive the aggregate Session; independent child archival
// is enabled together with child execution.
func (s *SessionThreadService) Archive(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.SessionThread, error) {
	thread, err := s.store.GetSessionThread(ctx, sessionID, threadID)
	if err != nil {
		return domain.SessionThread{}, err
	}
	if thread.ParentThreadID != nil {
		return domain.SessionThread{}, domain.Unsupported(
			"child session-thread archival is unavailable before child execution",
		)
	}
	if _, err := s.store.ArchiveSession(ctx, sessionID); err != nil {
		return domain.SessionThread{}, err
	}
	return s.store.GetSessionThread(ctx, sessionID, threadID)
}
