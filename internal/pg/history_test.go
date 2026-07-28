package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// textMsg builds a user.message draft with a single text block.
func textMsg(text string) domain.EventDraft {
	return domain.EventDraft{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	}
}

// TestHistoryThrough_CausalBatchOrdering proves the causal model-history
// reconstruction for a batched A,B admission. After turn A commits its
// agent.message, the history projected for turn B must be the causal chain
// [A, agent(A), B] — NOT the raw receipt log (which would place A and B as two
// consecutive user turns because B was admitted before A's output existed).
func TestHistoryThrough_CausalBatchOrdering(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sess := newSession("sess_causal")
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Admit A and B in one batch (both queued before either runs).
	adm, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{textMsg("A"), textMsg("B")})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	// adm.Events = [A, B, status_running]. Identify A and B.
	var idA, idB string
	for _, e := range adm.Events {
		if e.Type != domain.EvUserMessage {
			continue
		}
		content, ok := e.Payload["content"].([]any)
		if !ok || len(content) == 0 {
			continue
		}
		switch content[0].(map[string]any)["text"] {
		case "A":
			idA = e.ID
		case "B":
			idB = e.ID
		}
	}
	if idA == "" || idB == "" {
		t.Fatalf("could not identify A/B events: %+v", adm.Events)
	}

	// History for turn A (nothing prior): just [A].
	histA, err := store.HistoryThrough(ctx, sess.ID, idA, 100)
	if err != nil {
		t.Fatalf("history A: %v", err)
	}
	if len(histA) != 1 || histA[0].ID != idA {
		t.Fatalf("history for A should be [A], got %s", typeIDs(histA))
	}

	// Turn A completes with an agent.message + terminal idle. Because B is still
	// unprocessed, the store must keep the session running and suppress the idle.
	compA, err := store.CompleteTurn(ctx, sess.ID, idA, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "reply-A"}}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle)
	if err != nil {
		t.Fatalf("complete A: %v", err)
	}
	if compA.Session.Status != domain.StatusRunning {
		t.Fatalf("session must stay running while B is queued, got %s", compA.Session.Status)
	}
	for _, e := range compA.Events {
		if e.Type == domain.EvSessionStatusIdle {
			t.Fatal("intermediate turn A must not emit session.status_idle")
		}
	}

	// History for turn B: the causal chain [A, agent(A), B].
	histB, err := store.HistoryThrough(ctx, sess.ID, idB, 100)
	if err != nil {
		t.Fatalf("history B: %v", err)
	}
	if len(histB) != 3 {
		t.Fatalf("expected 3 causal events for B, got %d: %s", len(histB), typeIDs(histB))
	}
	if histB[0].ID != idA {
		t.Fatalf("history B[0] should be A, got %s", histB[0].ID)
	}
	if histB[1].Type != domain.EvAgentMessage {
		t.Fatalf("history B[1] should be agent(A), got %s", histB[1].Type)
	}
	if histB[2].ID != idB {
		t.Fatalf("history B[2] should be B, got %s", histB[2].ID)
	}

	// Projecting that history yields the causal conversation user(A) /
	// assistant(A) / user(B) — three alternating turns, not a collapsed user block.
	msgs := domain.ProjectMessages(histB)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 projected messages, got %d", len(msgs))
	}
	if msgs[0].Role != domain.RoleUser || msgs[1].Role != domain.RoleAssistant || msgs[2].Role != domain.RoleUser {
		t.Fatalf("expected user/assistant/user, got %s/%s/%s", msgs[0].Role, msgs[1].Role, msgs[2].Role)
	}

	// Turn B completes; now no unprocessed user.message remains, so the session
	// goes idle and the idle event is emitted.
	compB, err := store.CompleteTurn(ctx, sess.ID, idB, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "reply-B"}}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle)
	if err != nil {
		t.Fatalf("complete B: %v", err)
	}
	if compB.Session.Status != domain.StatusIdle {
		t.Fatalf("session should be idle after the last turn, got %s", compB.Session.Status)
	}
	sawIdle := false
	for _, e := range compB.Events {
		if e.Type == domain.EvSessionStatusIdle {
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Fatal("final turn B must emit session.status_idle")
	}

	// Exactly one public session.status_idle exists across the whole session.
	all, err := store.EventsAfter(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	idleCount := 0
	for _, e := range all {
		if e.Type == domain.EvSessionStatusIdle {
			idleCount++
		}
	}
	if idleCount != 1 {
		t.Fatalf("expected exactly one session.status_idle across the batch, got %d", idleCount)
	}
}

func typeIDs(events []domain.Event) string {
	s := ""
	for _, e := range events {
		s += e.Type + "(" + e.ID + ") "
	}
	return s
}
