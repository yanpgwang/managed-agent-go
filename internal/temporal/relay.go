package temporal

import (
	"context"
	"log"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

// WakeupDeliverer delivers one wakeup to Temporal. *Signaler implements it; a
// fake implements it in tests to simulate a temporarily-unavailable Worker or
// Temporal service.
type WakeupDeliverer interface {
	Wake(ctx context.Context, sessionID string, maxEventSeq int64) error
}

// OutboxStore is the relay's view of the PostgreSQL outbox. *pg.Store implements
// it; the narrow interface keeps the relay testable.
type OutboxStore interface {
	ClaimWakeups(ctx context.Context, limit int) ([]pg.OutboxWakeup, error)
	DeleteWakeupIfUnchanged(ctx context.Context, sessionID string, maxSeq int64) (bool, error)
	RecordAttempt(ctx context.Context, sessionID string, cause string) error
}

// RelayConfig tunes the relay loop.
type RelayConfig struct {
	// PollInterval is how often the relay scans the outbox when the last scan
	// found no pending wakeups.
	PollInterval time.Duration
	// BatchSize bounds how many wakeups one scan claims.
	BatchSize int
}

func (c RelayConfig) withDefaults() RelayConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	return c
}

// Relay drains the PostgreSQL orchestration outbox and delivers each wakeup to
// Temporal with Signal-With-Start, retrying until the service accepts it.
//
// Correctness rests on the outbox, not on any post-commit fast path:
//   - A wakeup is deleted only after a successful signal AND only if no later
//     admission raised its sequence in the meantime (DeleteWakeupIfUnchanged).
//   - A crash after signaling but before the delete leaves the row; the next
//     cycle re-delivers it, which the workflow treats as a harmless duplicate.
//   - A delivery failure (Worker/Temporal unavailable) records the attempt and
//     leaves the row for retry, so admission can outrun execution-plane outages.
type Relay struct {
	store     OutboxStore
	deliverer WakeupDeliverer
	cfg       RelayConfig
}

func NewRelay(store OutboxStore, deliverer WakeupDeliverer, cfg RelayConfig) *Relay {
	return &Relay{store: store, deliverer: deliverer, cfg: cfg.withDefaults()}
}

// Run drives the relay until ctx is canceled.
func (r *Relay) Run(ctx context.Context) error {
	timer := time.NewTimer(r.cfg.PollInterval)
	defer timer.Stop()
	for {
		delivered, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("relay: cycle error: %v", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// When a scan delivered work there may be more waiting, so poll again
		// promptly; otherwise back off to the poll interval.
		wait := r.cfg.PollInterval
		if delivered > 0 {
			wait = 0
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// RunOnce claims a batch of pending wakeups, delivers each, and reconciles the
// outbox. It returns the number of wakeups successfully delivered-and-removed.
// It is the unit the tests drive directly to exercise crash boundaries.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	wakeups, err := r.store.ClaimWakeups(ctx, r.cfg.BatchSize)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, w := range wakeups {
		if err := r.deliverer.Wake(ctx, w.SessionID, w.MaxEventSeq); err != nil {
			// Worker/Temporal unavailable or a transient service error: record the
			// attempt and leave the wakeup for the next cycle. This is exactly the
			// property that lets the control plane accept input while execution
			// workers are down.
			if recErr := r.store.RecordAttempt(ctx, w.SessionID, err.Error()); recErr != nil {
				log.Printf("relay: record attempt failed session_id=%s: %v", w.SessionID, recErr)
			}
			continue
		}
		// The signal was durably accepted. Delete the wakeup only if no later
		// admission coalesced a higher sequence into the row after we read it; a
		// mismatch leaves the row so the newer sequence is delivered next cycle.
		ok, err := r.store.DeleteWakeupIfUnchanged(ctx, w.SessionID, w.MaxEventSeq)
		if err != nil {
			log.Printf("relay: delete wakeup failed session_id=%s: %v", w.SessionID, err)
			continue
		}
		if ok {
			removed++
		}
	}
	return removed, nil
}
