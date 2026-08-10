package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// RecordThreadWorkflowRetry moves only the child that owns the failing model
// request into rescheduling. The Session projection is recomputed from every
// non-archived Thread in the same transaction.
func (s *Store) RecordThreadWorkflowRetry(
	ctx context.Context,
	sessionID string,
	threadID string,
	triggerEventID string,
	errorEventID string,
	statusEventID string,
	errorPayload map[string]any,
) error {
	if sessionID == "" || threadID == "" || triggerEventID == "" ||
		errorEventID == "" || statusEventID == "" || errorEventID == statusEventID {
		return domain.Validation("session, Thread, trigger, and distinct retry event ids are required")
	}
	applied := false
	err := s.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		session, thread, err := lockChildRetryOwner(
			ctx, tx, q, sessionID, threadID, triggerEventID,
		)
		if err != nil {
			return err
		}
		drafts := []domain.EventDraft{
			{
				ID: errorEventID, Type: domain.EvSessionError,
				Payload: map[string]any{"error": errorPayload},
			},
			{
				ID: statusEventID, Type: domain.EvSessionThreadStatusRescheduled,
				Payload: threadLifecyclePayload(thread),
			},
		}
		existing, err := workflowDraftsExisting(
			ctx, q, sessionID, triggerEventID, drafts,
		)
		if err != nil || existing {
			return err
		}
		if thread.Status != domain.StatusRunning {
			return domain.Conflict("child Thread retry requires a running Thread")
		}
		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		_, maxSeq, err = s.appendThreadDrafts(
			ctx, q, sessionID, threadID, drafts, maxSeq, &triggerEventID,
		)
		if err != nil {
			return err
		}
		_, maxSeq, err = s.appendThreadDrafts(
			ctx, q, sessionID, *thread.ParentThreadID,
			[]domain.EventDraft{{
				Type:    domain.EvSessionThreadStatusRescheduled,
				Payload: threadLifecyclePayload(thread),
			}}, maxSeq, &triggerEventID,
		)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		thread.TransitionStatus(domain.StatusRescheduling, now)
		if err := putSessionThreadTx(ctx, tx, thread); err != nil {
			return err
		}
		if err := s.updateAggregateStatusLocked(
			ctx, tx, q, &session, *thread.ParentThreadID,
			triggerEventID, maxSeq, now,
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return err
	}
	if applied {
		s.notifySession(ctx, sessionID)
	}
	return nil
}

// ResumeThreadWorkflowRetry publishes the child running transition immediately
// before the Workflow starts its next immutable provider request.
func (s *Store) ResumeThreadWorkflowRetry(
	ctx context.Context,
	sessionID string,
	threadID string,
	triggerEventID string,
	statusEventID string,
) error {
	if sessionID == "" || threadID == "" || triggerEventID == "" || statusEventID == "" {
		return domain.Validation("session, Thread, trigger, and retry event id are required")
	}
	applied := false
	err := s.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		session, thread, err := lockChildRetryOwner(
			ctx, tx, q, sessionID, threadID, triggerEventID,
		)
		if err != nil {
			return err
		}
		draft := domain.EventDraft{
			ID: statusEventID, Type: domain.EvSessionThreadStatusRunning,
			Payload: threadLifecyclePayload(thread),
		}
		existing, err := workflowDraftsExisting(
			ctx, q, sessionID, triggerEventID, []domain.EventDraft{draft},
		)
		if err != nil || existing {
			return err
		}
		if thread.Status != domain.StatusRescheduling {
			return domain.Conflict("child Thread retry resume requires a rescheduling Thread")
		}
		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		_, maxSeq, err = s.appendThreadDrafts(
			ctx, q, sessionID, threadID,
			[]domain.EventDraft{draft}, maxSeq, &triggerEventID,
		)
		if err != nil {
			return err
		}
		_, maxSeq, err = s.appendThreadDrafts(
			ctx, q, sessionID, *thread.ParentThreadID,
			[]domain.EventDraft{{
				Type:    domain.EvSessionThreadStatusRunning,
				Payload: threadLifecyclePayload(thread),
			}}, maxSeq, &triggerEventID,
		)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		thread.TransitionStatus(domain.StatusRunning, now)
		if err := putSessionThreadTx(ctx, tx, thread); err != nil {
			return err
		}
		if err := s.updateAggregateStatusLocked(
			ctx, tx, q, &session, *thread.ParentThreadID,
			triggerEventID, maxSeq, now,
		); err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return err
	}
	if applied {
		s.notifySession(ctx, sessionID)
	}
	return nil
}

func lockChildRetryOwner(
	ctx context.Context,
	tx pgx.Tx,
	q *pgstore.Queries,
	sessionID string,
	threadID string,
	triggerEventID string,
) (domain.Session, domain.SessionThread, error) {
	row, err := q.LockSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.SessionThread{}, domain.NotFound("session not found")
	}
	if err != nil {
		return domain.Session{}, domain.SessionThread{}, err
	}
	if row.DeletingAt.Valid {
		return domain.Session{}, domain.SessionThread{}, domain.Conflict("session deletion is in progress")
	}
	session, err := sessionFromLockRow(row)
	if err != nil {
		return domain.Session{}, domain.SessionThread{}, err
	}
	thread, err := loadSessionThreadForUpdate(ctx, tx, sessionID, threadID)
	if err != nil {
		return domain.Session{}, domain.SessionThread{}, err
	}
	if thread.ParentThreadID == nil {
		return domain.Session{}, domain.SessionThread{}, domain.Conflict("Thread retry owner is not a child")
	}
	trigger, err := q.GetEvent(ctx, pgstore.GetEventParams{
		SessionID: sessionID, ID: triggerEventID,
	})
	if err != nil {
		return domain.Session{}, domain.SessionThread{}, err
	}
	if trigger.ThreadID != threadID {
		return domain.Session{}, domain.SessionThread{}, domain.Conflict("retry trigger belongs to another Thread")
	}
	return session, thread, nil
}

func threadLifecyclePayload(thread domain.SessionThread) map[string]any {
	return map[string]any{
		"session_thread_id": thread.ID,
		"agent_name":        thread.Agent.Name,
	}
}

func (s *Store) updateAggregateStatusLocked(
	ctx context.Context,
	tx pgx.Tx,
	q *pgstore.Queries,
	session *domain.Session,
	primaryID string,
	triggerEventID string,
	maxSeq int64,
	now time.Time,
) error {
	aggregated, err := aggregateSessionThreadStatus(ctx, tx, session.ID)
	if err != nil {
		return err
	}
	if session.Status == aggregated {
		return putSessionOnlyTx(ctx, tx, *session)
	}
	session.TransitionStatus(aggregated, now)
	if err := putSessionOnlyTx(ctx, tx, *session); err != nil {
		return err
	}
	var draft domain.EventDraft
	switch aggregated {
	case domain.StatusRunning:
		draft = domain.EventDraft{Type: domain.EvSessionStatusRunning, Payload: map[string]any{}}
	case domain.StatusRescheduling:
		draft = domain.EventDraft{Type: domain.EvSessionStatusRescheduling, Payload: map[string]any{}}
	case domain.StatusIdle:
		draft = domain.EventDraft{
			Type:    domain.EvSessionStatusIdle,
			Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
		}
	default:
		return nil
	}
	_, _, err = s.appendThreadDrafts(
		ctx, q, session.ID, primaryID,
		[]domain.EventDraft{draft}, maxSeq, &triggerEventID,
	)
	return err
}
