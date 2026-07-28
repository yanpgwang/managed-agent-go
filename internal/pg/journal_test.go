package pg

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestJournal_StalePreparedStepCannotStartAfterRecovery proves recovery fences
// an overlapping old Activity before the external side-effect boundary. A step
// that was only prepared is safe to abandon, but the old Activity must not be
// allowed to advance it to started after its parent attempt was failed.
func TestJournal_StalePreparedStepCannotStartAfterRecovery(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const sessionID = "sess_journal_stale"
	trigger := journalTurn(t, store, sessionID)

	oldAttempt, err := store.BeginAttempt(ctx, sessionID, trigger)
	if err != nil {
		t.Fatalf("begin old attempt: %v", err)
	}
	oldStep, err := store.PrepareToolStep(ctx, oldAttempt.ID, 0, "tue_stale", "bash", map[string]any{"command": "side-effect"})
	if err != nil {
		t.Fatalf("prepare old step: %v", err)
	}

	hasPrior, err := store.RecoverTurn(ctx, sessionID, trigger)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if hasPrior {
		t.Fatal("a prepared-only step has not crossed the side-effect boundary")
	}
	if err := store.StartToolStep(ctx, oldStep); err == nil {
		t.Fatal("stale Activity must not start a step after recovery failed its attempt")
	}

	newAttempt, err := store.BeginAttempt(ctx, sessionID, trigger)
	if err != nil {
		t.Fatalf("begin replacement attempt: %v", err)
	}
	newStep, err := store.PrepareToolStep(ctx, newAttempt.ID, 0, "tue_replacement", "bash", map[string]any{"command": "safe"})
	if err != nil {
		t.Fatalf("prepare replacement step: %v", err)
	}
	if err := store.StartToolStep(ctx, newStep); err != nil {
		t.Fatalf("replacement active attempt should proceed: %v", err)
	}
}

// TestJournal_StartWaitsForConcurrentAttemptFence proves the active-attempt
// predicate is protected by a row lock, not only by a snapshot read. It holds an
// uncommitted recovery-style attempt failure: Start must wait (and time out)
// rather than observe the old active state and cross the side-effect boundary.
func TestJournal_StartWaitsForConcurrentAttemptFence(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const sessionID = "sess_journal_fence"
	trigger := journalTurn(t, store, sessionID)

	attempt, err := store.BeginAttempt(ctx, sessionID, trigger)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	stepID, err := store.PrepareToolStep(ctx, attempt.ID, 0, "tue_fence", "bash", map[string]any{"command": "side-effect"})
	if err != nil {
		t.Fatalf("prepare step: %v", err)
	}

	fenceTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fence transaction: %v", err)
	}
	if _, err := fenceTx.Exec(ctx, `
		UPDATE turn_attempts
		SET state = 'failed', error = 'recovered', updated_at = now(), finished_at = now()
		WHERE id = $1 AND state = 'active'`, attempt.ID); err != nil {
		_ = fenceTx.Rollback(ctx)
		t.Fatalf("stage attempt fence: %v", err)
	}

	blockedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = store.StartToolStep(blockedCtx, stepID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		_ = fenceTx.Rollback(ctx)
		t.Fatalf("StartToolStep should wait for the concurrent attempt fence, got %v", err)
	}

	// Roll back the simulated recovery. The parent becomes active again and the
	// still-prepared step can start normally, proving the timeout did not mutate it.
	if err := fenceTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback fence: %v", err)
	}
	if err := store.StartToolStep(ctx, stepID); err != nil {
		t.Fatalf("start after rolled-back fence: %v", err)
	}
}
