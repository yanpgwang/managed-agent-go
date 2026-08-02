package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// TestAdmitEvents_AtomicEventAndOutbox proves the core admission invariant: one
// transaction appends the public events with durable receipt sequences, flips
// the session to running, and writes exactly one coalescible outbox wakeup
// carrying the highest sequence.
func TestAdmitEvents_AtomicEventAndOutbox(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_atomic")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	adm, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "hello"}},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !adm.Enqueued {
		t.Fatal("expected an outbox wakeup to be enqueued")
	}
	if adm.Session.Status != domain.StatusRunning {
		t.Fatalf("expected running, got %s", adm.Session.Status)
	}
	// The batch produced the user.message plus a synthetic session.status_running.
	if len(adm.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(adm.Events))
	}
	if adm.Events[0].Type != domain.EvUserMessage || adm.Events[1].Type != domain.EvSessionStatusRunning {
		t.Fatalf("unexpected event order: %s, %s", adm.Events[0].Type, adm.Events[1].Type)
	}
	if adm.Events[0].Sequence != 1 || adm.Events[1].Sequence != 2 {
		t.Fatalf("unexpected receipt sequences: %d, %d", adm.Events[0].Sequence, adm.Events[1].Sequence)
	}
	if adm.MaxSeq != 2 {
		t.Fatalf("expected max seq 2, got %d", adm.MaxSeq)
	}

	wakeup, ok, err := store.PendingWakeup(ctx, sess.ID)
	if err != nil {
		t.Fatalf("pending wakeup: %v", err)
	}
	if !ok {
		t.Fatal("expected a pending wakeup")
	}
	if wakeup.MaxEventSeq != 2 {
		t.Fatalf("expected wakeup seq 2, got %d", wakeup.MaxEventSeq)
	}
}

func TestSystemMessage_IsLinkedToItsTurnAndProcessedAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	sess := newSession("sess_system_companion")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	admission, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
		}},
		{Type: domain.EvSystemMessage, Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "timezone UTC"}},
		}},
	})
	if err != nil {
		t.Fatalf("admit paired system message: %v", err)
	}
	if len(admission.Events) != 3 {
		t.Fatalf("admitted events = %d, want user/system/running", len(admission.Events))
	}
	trigger := admission.Events[0]
	systemEvent := admission.Events[1]
	if got := trigger.Payload[domain.InternalCompanionSystemEventID]; got != systemEvent.ID {
		t.Fatalf("companion link = %v, want %s", got, systemEvent.ID)
	}

	if _, err := store.CompleteWorkflowTurn(
		ctx,
		sess.ID,
		trigger.ID,
		[]domain.EventDraft{{
			Type:    domain.EvSessionStatusIdle,
			Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatalf("complete paired turn: %v", err)
	}
	processed, err := store.GetEvent(ctx, sess.ID, systemEvent.ID)
	if err != nil {
		t.Fatalf("get system event: %v", err)
	}
	if processed.ProcessedAt == nil {
		t.Fatal("system.message remained unprocessed after its accompanying turn")
	}
}

// TestAdmitEvents_CoalescesWakeup proves the outbox is coalescible, not a queue:
// a second admission before delivery updates the single pending row to the new
// highest sequence rather than creating another wakeup.
func TestAdmitEvents_CoalescesWakeup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_coalesce")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "one"}},
	}); err != nil {
		t.Fatalf("admit 1: %v", err)
	}
	adm2, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "two"}},
	})
	if err != nil {
		t.Fatalf("admit 2: %v", err)
	}

	// First admission produced 2 events (msg + running). Second is already running,
	// so it produces just the message: seq 3.
	if len(adm2.Events) != 1 || adm2.Events[0].Sequence != 3 {
		t.Fatalf("expected single event at seq 3, got %d events", len(adm2.Events))
	}
	wakeup, ok, err := store.PendingWakeup(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("pending wakeup: ok=%v err=%v", ok, err)
	}
	// Exactly one wakeup, coalesced to the newest sequence (3).
	if wakeup.MaxEventSeq != 3 {
		t.Fatalf("expected coalesced wakeup seq 3, got %d", wakeup.MaxEventSeq)
	}
}

// TestAdmitEvents_OrderedConsumption proves events are consumed in receipt order
// after a cursor — the SessionWorkflow's ordered-consumption contract.
func TestAdmitEvents_OrderedConsumption(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_order")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, body := range []string{"a", "b", "c"} {
		if _, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": body}},
		}); err != nil {
			t.Fatalf("admit %s: %v", body, err)
		}
	}

	all, err := store.EventsAfter(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatalf("events after: %v", err)
	}
	var lastSeq int64
	for _, e := range all {
		if e.Sequence <= lastSeq {
			t.Fatalf("events out of order: %d after %d", e.Sequence, lastSeq)
		}
		lastSeq = e.Sequence
	}

	// Consuming after a cursor yields only later events.
	tail, err := store.EventsAfter(ctx, sess.ID, 2, 100)
	if err != nil {
		t.Fatalf("events after cursor: %v", err)
	}
	for _, e := range tail {
		if e.Sequence <= 2 {
			t.Fatalf("cursor not honored: got seq %d", e.Sequence)
		}
	}
}

// TestAdmitEvents_TerminatedRejected proves a terminated session refuses new
// input at admission.
func TestAdmitEvents_TerminatedRejected(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_term")
	sess.Status = domain.StatusTerminated
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "x"}},
	})
	if err == nil {
		t.Fatal("expected admission to a terminated session to fail")
	}
}
