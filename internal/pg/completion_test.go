package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// TestCompleteTurn_Idempotent proves the completion commit is idempotent — the
// property a Temporal Activity requires, since it may run more than once. The
// first call appends output and stamps the trigger processed; a second call with
// the same trigger replays the exact committed events without appending a
// duplicate and without moving the projection again.
func TestCompleteTurn_Idempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_idem")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	adm, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "hi"}},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	trigger := adm.Events[0]

	output := []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": "reply"}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}

	first, err := store.CompleteTurn(ctx, sess.ID, trigger.ID, output, domain.StatusIdle)
	if err != nil {
		t.Fatalf("complete 1: %v", err)
	}
	if !first.Applied {
		t.Fatal("first completion should apply")
	}
	if len(first.Events) != 2 {
		t.Fatalf("expected 2 committed events, got %d", len(first.Events))
	}

	// Snapshot the full ledger after the first commit.
	before, err := store.EventsAfter(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("events after 1: %v", err)
	}

	// A retry (duplicate Activity execution) must be harmless.
	second, err := store.CompleteTurn(ctx, sess.ID, trigger.ID, output, domain.StatusIdle)
	if err != nil {
		t.Fatalf("complete 2: %v", err)
	}
	if second.Applied {
		t.Fatal("second completion must not re-apply")
	}
	if len(second.Events) != 2 {
		t.Fatalf("replay should return the 2 prior events, got %d", len(second.Events))
	}
	// The replayed events must be exactly the originally committed ones.
	for i := range first.Events {
		if second.Events[i].ID != first.Events[i].ID || second.Events[i].Sequence != first.Events[i].Sequence {
			t.Fatalf("replay mismatch at %d: %+v vs %+v", i, second.Events[i], first.Events[i])
		}
	}

	after, err := store.EventsAfter(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("events after 2: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("ledger grew on retry: before=%d after=%d", len(before), len(after))
	}

	final, err := store.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}
}
