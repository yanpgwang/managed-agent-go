package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// journalTurn creates a session + admitted user.message and returns the trigger
// event id, the shared setup for journal tests.
func journalTurn(t *testing.T, store *Store, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	sess := newSession(sessionID)
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	adm, err := store.AdmitEvents(ctx, sessionID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": "go"}},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return adm.Events[0].ID
}

// TestJournal_HappyPath proves one tool step advances prepared -> started ->
// completed under an attempt, and the attempt closes completed.
func TestJournal_HappyPath(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	trigger := journalTurn(t, store, "sess_journal_ok")

	attempt, err := store.BeginAttempt(ctx, "sess_journal_ok", trigger)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if attempt.AttemptNo != 1 {
		t.Fatalf("expected attempt 1, got %d", attempt.AttemptNo)
	}
	stepID, err := store.PrepareToolStep(ctx, attempt.ID, 0, "tue_1", "bash", map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := store.StartToolStep(ctx, stepID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.CompleteToolStep(ctx, stepID, domain.ToolStepResult{
		Content: []any{map[string]any{"type": "text", "text": "hi"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.FinishAttempt(ctx, attempt.ID, domain.RunAttemptCompleted, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	state, ok, err := store.ToolStepStateByEventID(ctx, "tue_1")
	if err != nil || !ok {
		t.Fatalf("state lookup: ok=%v err=%v", ok, err)
	}
	if state != domain.ToolStepCompleted {
		t.Fatalf("expected completed, got %s", state)
	}
}

// TestJournal_StartedStepRecoveredAsAmbiguous proves the core safety property:
// a step that started (side effect may have occurred) but never completed is
// classified ambiguous by recovery, and the turn is then refused for a fresh
// attempt so the side effect is never silently replayed.
func TestJournal_StartedStepRecoveredAsAmbiguous(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	trigger := journalTurn(t, store, "sess_journal_amb")

	attempt, err := store.BeginAttempt(ctx, "sess_journal_amb", trigger)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stepID, err := store.PrepareToolStep(ctx, attempt.ID, 0, "tue_amb", "bash", map[string]any{"command": "rm -rf /tmp/x"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The step starts — the executor may now have changed the world — and then the
	// attempt "crashes": nothing completes it.
	if err := store.StartToolStep(ctx, stepID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Recovery classifies the started step ambiguous and reports prior execution.
	hasPrior, err := store.RecoverTurn(ctx, "sess_journal_amb", trigger)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !hasPrior {
		t.Fatal("recovery must report prior tool execution")
	}
	state, _, err := store.ToolStepStateByEventID(ctx, "tue_amb")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state != domain.ToolStepAmbiguous {
		t.Fatalf("expected ambiguous, got %s", state)
	}

	// A fresh attempt must be refused: the turn carries ambiguous prior execution
	// and must never be silently replayed.
	if _, err := store.BeginAttempt(ctx, "sess_journal_amb", trigger); err == nil {
		t.Fatal("BeginAttempt must refuse a turn with prior tool execution")
	}
}

// TestJournal_FinishRefusesUnclassifiedStartedStep proves an attempt cannot be
// closed while a started step remains unclassified.
func TestJournal_FinishRefusesUnclassifiedStartedStep(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	trigger := journalTurn(t, store, "sess_journal_finish")

	attempt, err := store.BeginAttempt(ctx, "sess_journal_finish", trigger)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stepID, err := store.PrepareToolStep(ctx, attempt.ID, 0, "tue_f", "bash", map[string]any{"command": "x"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := store.StartToolStep(ctx, stepID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.FinishAttempt(ctx, attempt.ID, domain.RunAttemptCompleted, nil); err == nil {
		t.Fatal("FinishAttempt must refuse while a started step is unclassified")
	}
}

// TestJournal_OneActiveAttempt proves the one-active-attempt guard.
func TestJournal_OneActiveAttempt(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	trigger := journalTurn(t, store, "sess_journal_active")

	if _, err := store.BeginAttempt(ctx, "sess_journal_active", trigger); err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	if _, err := store.BeginAttempt(ctx, "sess_journal_active", trigger); err == nil {
		t.Fatal("second BeginAttempt must be refused while one is active")
	}
}
