package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
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

// TestOutbox_ListIsAtLeastOnce proves the honest delivery semantics: the outbox
// read is a plain list, NOT a lease. Two concurrent reads both see the same
// pending wakeup — so two relay instances can both deliver a Signal for it. That
// is deliberately harmless: delivery is at-least-once and the SessionWorkflow
// deduplicates duplicate wakeups by receipt sequence. This test guards against
// anyone reintroducing a claim/lease promise the code does not actually make.
func TestOutbox_ListIsAtLeastOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_atleastonce")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "x"}},
	}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	// Two independent reads (as two relay instances would issue) both observe the
	// same pending wakeup — there is no claim that hides it from the second reader.
	first, err := store.ListWakeupsForDelivery(ctx, 10)
	if err != nil {
		t.Fatalf("list 1: %v", err)
	}
	second, err := store.ListWakeupsForDelivery(ctx, 10)
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected both reads to see the wakeup, got %d and %d", len(first), len(second))
	}
	if first[0].SessionID != sess.ID || second[0].SessionID != sess.ID {
		t.Fatal("both reads should return the same session's wakeup")
	}

	// A guarded delete removes it once; a second delete on the same sequence is a
	// no-op, so a duplicate delivery+delete pair does not error or double-remove.
	removed, err := store.DeleteWakeupIfUnchanged(ctx, sess.ID, first[0].MaxEventSeq)
	if err != nil || !removed {
		t.Fatalf("first delete should remove: removed=%v err=%v", removed, err)
	}
	removed, err = store.DeleteWakeupIfUnchanged(ctx, sess.ID, second[0].MaxEventSeq)
	if err != nil {
		t.Fatalf("second delete errored: %v", err)
	}
	if removed {
		t.Fatal("second delete of an already-removed wakeup should be a no-op")
	}
}
