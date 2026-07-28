package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// TestOutbox_DeleteRespectsCoalescedSequence proves, against real PostgreSQL,
// the delete-if-unchanged guard: once a later admission raises the wakeup's
// sequence, a delete keyed on the older sequence is a no-op, so the newer
// wakeup survives for re-delivery. This is the store-level property the relay's
// crash/coalesce-race safety rests on.
func TestOutbox_DeleteRespectsCoalescedSequence(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_outbox")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	adm1, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "a"}},
	})
	if err != nil {
		t.Fatalf("admit 1: %v", err)
	}
	deliveredSeq := adm1.MaxSeq

	// A second admission coalesces into the same wakeup and raises its sequence.
	adm2, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "b"}},
	})
	if err != nil {
		t.Fatalf("admit 2: %v", err)
	}
	if adm2.MaxSeq <= deliveredSeq {
		t.Fatalf("expected raised sequence, got %d after %d", adm2.MaxSeq, deliveredSeq)
	}

	// The relay signaled the OLD sequence, then tries to delete keyed on it. The
	// row now carries the newer sequence, so the delete must be a no-op.
	removed, err := store.DeleteWakeupIfUnchanged(ctx, sess.ID, deliveredSeq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed {
		t.Fatal("delete keyed on the stale sequence must not remove the coalesced wakeup")
	}
	wakeup, ok, err := store.PendingWakeup(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("wakeup should survive: ok=%v err=%v", ok, err)
	}
	if wakeup.MaxEventSeq != adm2.MaxSeq {
		t.Fatalf("expected surviving wakeup at seq %d, got %d", adm2.MaxSeq, wakeup.MaxEventSeq)
	}

	// Delivering the current sequence and deleting on it now succeeds.
	removed, err = store.DeleteWakeupIfUnchanged(ctx, sess.ID, adm2.MaxSeq)
	if err != nil {
		t.Fatalf("delete 2: %v", err)
	}
	if !removed {
		t.Fatal("delete keyed on the current sequence should remove the wakeup")
	}
	if _, ok, _ := store.PendingWakeup(ctx, sess.ID); ok {
		t.Fatal("wakeup should be gone after matching delete")
	}
}

// TestOutbox_ClaimSkipsLocked proves ClaimWakeups uses FOR UPDATE SKIP LOCKED:
// a wakeup locked by one transaction is invisible to a concurrent claimer, so
// cooperating relays never block on each other or double-deliver within a lock
// window.
func TestOutbox_ClaimSkipsLocked(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_skiplocked")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "x"}},
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	// Open a transaction that claims (and holds a lock on) the wakeup.
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx,
		`SELECT session_id FROM orchestration_outbox ORDER BY enqueued_at LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		t.Fatalf("lock query: %v", err)
	}
	locked := 0
	for rows.Next() {
		locked++
	}
	rows.Close()
	if locked != 1 {
		t.Fatalf("expected to lock 1 row, locked %d", locked)
	}

	// A concurrent claim (separate pool connection) must skip the locked row.
	claimed, err := store.ClaimWakeups(ctx, 10)
	if err != nil {
		t.Fatalf("concurrent claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("expected concurrent claim to skip the locked wakeup, got %d", len(claimed))
	}
}
