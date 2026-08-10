package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// OutboxWakeup is one coalescible orchestration wakeup: the session to wake and
// the highest known receipt sequence at enqueue time. It is not an executable
// job — the SessionWorkflow loads authoritative events from PostgreSQL after its
// own durable cursor and ignores sequences it has already observed.
type OutboxWakeup struct {
	SessionID string
	// ThreadID is empty for the primary SessionWorkflow and names an
	// independent child SessionThreadWorkflow otherwise.
	ThreadID    string
	MaxEventSeq int64
	EnqueuedAt  time.Time
	Attempts    int
}

// ListWakeupsForDelivery reads up to limit pending wakeups, oldest first. It is
// a plain read, NOT a lease or claim: it takes no row lock that outlives the
// query, so two relay instances scanning concurrently can both read the same row
// and both deliver a Signal for it. That is deliberate — delivery is
// at-least-once and duplicates are harmless because the SessionWorkflow
// deduplicates by receipt sequence (a wakeup at or below its cursor is a no-op).
// A delivered row is removed only by DeleteWakeupIfUnchanged, itself guarded by
// the sequence, so a duplicate delete is also safe.
func (s *Store) ListWakeupsForDelivery(ctx context.Context, limit int) ([]OutboxWakeup, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ListOrchestrationWakeups(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]OutboxWakeup, 0, len(rows))
	for _, row := range rows {
		out = append(out, OutboxWakeup{
			SessionID: row.SessionID, ThreadID: row.ThreadID,
			MaxEventSeq: row.MaxEventSeq, EnqueuedAt: row.EnqueuedAt.Time.UTC(),
			Attempts: int(row.Attempts),
		})
	}
	return out, nil
}

// DeleteWakeupIfUnchanged removes a delivered wakeup, but only if no later
// admission raised its sequence since it was read. It reports whether a row was
// actually deleted: false means either new work coalesced into the row after the
// signal was sent (it remains and is re-delivered with the higher sequence) or
// another relay already deleted it — both harmless.
func (s *Store) DeleteWakeupIfUnchanged(
	ctx context.Context,
	sessionID string,
	maxSeq int64,
) (bool, error) {
	affected, err := s.q.DeleteOutboxIfSeq(ctx, pgstore.DeleteOutboxIfSeqParams{
		SessionID:   sessionID,
		MaxEventSeq: maxSeq,
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) DeleteThreadWakeupIfUnchanged(
	ctx context.Context,
	sessionID string,
	threadID string,
	maxSeq int64,
) (bool, error) {
	affected, err := s.q.DeleteThreadOutboxIfSeq(
		ctx,
		pgstore.DeleteThreadOutboxIfSeqParams{
			SessionID: sessionID, ThreadID: threadID, MaxEventSeq: maxSeq,
		},
	)
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// RecordAttempt bumps the failed-delivery counter and records the last error for
// backoff and observability. The wakeup is left in place for retry.
func (s *Store) RecordAttempt(
	ctx context.Context,
	sessionID string,
	cause string,
) error {
	return s.q.MarkOutboxAttempt(ctx, pgstore.MarkOutboxAttemptParams{
		LastAttemptAt: tsUTC(s.clock.Now().UTC()),
		LastError:     &cause,
		SessionID:     sessionID,
	})
}

func (s *Store) RecordThreadAttempt(
	ctx context.Context,
	sessionID string,
	threadID string,
	cause string,
) error {
	return s.q.MarkThreadOutboxAttempt(
		ctx,
		pgstore.MarkThreadOutboxAttemptParams{
			LastAttemptAt: tsUTC(s.clock.Now().UTC()), LastError: &cause,
			SessionID: sessionID, ThreadID: threadID,
		},
	)
}

// PendingWakeup returns the current outbox wakeup for a session and whether one
// exists. Used by tests to assert coalescing behavior.
func (s *Store) PendingWakeup(ctx context.Context, sessionID string) (OutboxWakeup, bool, error) {
	row, err := s.q.GetOutbox(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboxWakeup{}, false, nil
	}
	if err != nil {
		return OutboxWakeup{}, false, err
	}
	return OutboxWakeup{
		SessionID:   row.SessionID,
		MaxEventSeq: row.MaxEventSeq,
		EnqueuedAt:  row.EnqueuedAt.Time.UTC(),
		Attempts:    int(row.Attempts),
	}, true, nil
}
