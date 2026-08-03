package pg

import (
	"context"
	"slices"
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

func TestAppendWorkflowEvents_IdempotentAndIncludedInCompletionReplay(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sess_progress_idem")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": "hello"},
	}})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	trigger := admission.Events[0]
	start := domain.EventDraft{
		ID: "sevt_model_start", Type: domain.EvSpanModelRequestStart,
		Payload: map[string]any{},
	}
	if err := store.AppendWorkflowEvents(ctx, session.ID, trigger.ID, []domain.EventDraft{start}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	if err := store.AppendWorkflowEvents(ctx, session.ID, trigger.ID, []domain.EventDraft{start}); err != nil {
		t.Fatalf("retry start: %v", err)
	}

	events, err := store.EventsAfter(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("events after start: %v", err)
	}
	if len(events) != 3 || events[0].Type != domain.EvUserMessage ||
		events[1].Type != domain.EvSessionStatusRunning ||
		events[2].Type != domain.EvSpanModelRequestStart {
		t.Fatalf("event order after idempotent start = %v", eventTypes(events))
	}
	if events[2].ProcessedAt == nil {
		t.Fatal("server-emitted model start was not processed on receipt")
	}
	storedTrigger, err := store.GetEvent(ctx, session.ID, trigger.ID)
	if err != nil {
		t.Fatalf("get trigger: %v", err)
	}
	if storedTrigger.ProcessedAt != nil {
		t.Fatal("intermediate progress processed the turn trigger")
	}

	completion, err := store.CompleteTurn(ctx, session.ID, trigger.ID, []domain.EventDraft{
		{ID: "sevt_message", Type: domain.EvAgentMessage, Payload: map[string]any{"content": "done"}},
		{ID: "sevt_model_end", Type: domain.EvSpanModelRequestEnd, Payload: map[string]any{
			"model_request_start_id": start.ID,
			"is_error":               false,
		}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{
			"stop_reason": map[string]any{"type": "end_turn"},
		}},
	}, domain.StatusIdle)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !completion.Applied {
		t.Fatal("first completion did not apply")
	}

	replay, err := store.CompleteTurn(ctx, session.ID, trigger.ID, nil, domain.StatusIdle)
	if err != nil {
		t.Fatalf("replay completion: %v", err)
	}
	if replay.Applied {
		t.Fatal("replayed completion unexpectedly applied")
	}
	if got, want := eventTypes(replay.Events), []string{
		domain.EvSpanModelRequestStart,
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvSessionStatusIdle,
	}; !slices.Equal(got, want) {
		t.Fatalf("replayed turn events = %v, want %v", got, want)
	}

	mismatch := start
	mismatch.Payload = map[string]any{"unexpected": true}
	if err := store.AppendWorkflowEvents(ctx, session.ID, trigger.ID, []domain.EventDraft{mismatch}); err == nil {
		t.Fatal("mismatched idempotency payload unexpectedly succeeded")
	}
}
