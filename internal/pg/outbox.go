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
	SessionID   string
	MaxEventSeq int64
	EnqueuedAt  time.Time
	Attempts    int
}

// ClaimWakeups reads up to limit pending wakeups for delivery, oldest first,
// using FOR UPDATE SKIP LOCKED so concurrent relays never block each other. The
// returned rows are locked for the lifetime of tx; the caller must call the
// returned finish func (commit) once delivery decisions are recorded.
//
// Because delivery to Temporal is an external call, ClaimWakeups deliberately
// does NOT hold the tx across it: it reads within a short tx, commits, and the
// caller delivers and then calls DeleteWakeupIfUnchanged / RecordAttempt in
// separate short transactions. This keeps row locks brief and makes duplicate
// delivery (relay crash after signal, before delete) harmless rather than
// blocking.
func (s *Store) ClaimWakeups(ctx context.Context, limit int) ([]OutboxWakeup, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ClaimOutboxBatch(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]OutboxWakeup, 0, len(rows))
	for _, row := range rows {
		out = append(out, OutboxWakeup{
			SessionID:   row.SessionID,
			MaxEventSeq: row.MaxEventSeq,
			EnqueuedAt:  row.EnqueuedAt.Time.UTC(),
			Attempts:    int(row.Attempts),
		})
	}
	return out, nil
}

// DeleteWakeupIfUnchanged removes a delivered wakeup, but only if no later
// admission raised its sequence since it was claimed. It reports whether a row
// was actually deleted: false means new work coalesced into the row after the
// signal was sent, so the wakeup remains and will be re-delivered with the
// higher sequence — a harmless duplicate.
func (s *Store) DeleteWakeupIfUnchanged(ctx context.Context, sessionID string, maxSeq int64) (bool, error) {
	affected, err := s.q.DeleteOutboxIfSeq(ctx, pgstore.DeleteOutboxIfSeqParams{
		SessionID:   sessionID,
		MaxEventSeq: maxSeq,
	})
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// RecordAttempt bumps the failed-delivery counter and records the last error for
// backoff and observability. The wakeup is left in place for retry.
func (s *Store) RecordAttempt(ctx context.Context, sessionID string, cause string) error {
	return s.q.MarkOutboxAttempt(ctx, pgstore.MarkOutboxAttemptParams{
		LastAttemptAt: tsUTC(s.clock.Now().UTC()),
		LastError:     &cause,
		SessionID:     sessionID,
	})
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
