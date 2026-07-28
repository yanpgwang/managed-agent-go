package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func claimExecutionRun(t *testing.T, runs *RunStore, session domain.Session) domain.SessionRun {
	t.Helper()
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
	}}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	return claim.Run
}

func TestExecutionStore_AttemptAndToolStepLifecycle(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	run := claimExecutionRun(t, runs, session)

	attempt, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.AttemptNo != 1 || attempt.State != domain.RunAttemptActive {
		t.Fatalf("attempt = %#v", attempt)
	}
	if _, err := runs.BeginAttempt(ctx, run.ID); !isConflict(err) {
		t.Fatalf("second active attempt err = %v, want conflict", err)
	}

	input := map[string]any{"path": "notes.txt", "content": "hello"}
	step, err := runs.PrepareToolStep(ctx, attempt.ID, 0, "sevt_tool_1", "write", input)
	if err != nil {
		t.Fatal(err)
	}
	if step.State != domain.ToolStepPrepared || step.ToolUseEventID != "sevt_tool_1" {
		t.Fatalf("prepared step = %#v", step)
	}
	if _, err := runs.FinishAttempt(ctx, attempt.ID, domain.RunAttemptCompleted, nil); !isConflict(err) {
		t.Fatalf("completed attempt with prepared step err = %v, want conflict", err)
	}

	step, err = runs.StartToolStep(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if step.State != domain.ToolStepStarted || step.StartedAt == nil {
		t.Fatalf("started step = %#v", step)
	}
	if _, err := runs.FinishAttempt(ctx, attempt.ID, domain.RunAttemptInterrupted, nil); !isConflict(err) {
		t.Fatalf("attempt with started step err = %v, want conflict", err)
	}

	wantResult := domain.ToolStepResult{
		Content: []any{map[string]any{"type": "text", "text": "wrote notes.txt"}},
		IsError: false,
	}
	step, err = runs.CompleteToolStep(ctx, step.ID, wantResult)
	if err != nil {
		t.Fatal(err)
	}
	if step.State != domain.ToolStepCompleted || step.Result == nil ||
		step.FinishedAt == nil || step.Result.IsError {
		t.Fatalf("completed step = %#v", step)
	}

	attempt, err = runs.FinishAttempt(ctx, attempt.ID, domain.RunAttemptCompleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != domain.RunAttemptCompleted || attempt.FinishedAt == nil {
		t.Fatalf("finished attempt = %#v", attempt)
	}
	if _, err := runs.BeginAttempt(ctx, run.ID); !isConflict(err) {
		t.Fatalf("retry after completed attempt err = %v, want conflict", err)
	}

	stored, err := runs.GetToolStep(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Input["path"] != "notes.txt" || stored.Result == nil ||
		len(stored.Result.Content) != 1 {
		t.Fatalf("stored step = %#v", stored)
	}
	attempts, err := runs.ListAttempts(ctx, run.ID)
	if err != nil || len(attempts) != 1 || attempts[0].ID != attempt.ID {
		t.Fatalf("attempts = %#v err=%v", attempts, err)
	}
	steps, err := runs.ListToolSteps(ctx, attempt.ID)
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("steps = %#v err=%v", steps, err)
	}
}

func TestExecutionStore_AmbiguousStepBlocksAnotherAttempt(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	run := claimExecutionRun(t, runs, session)
	attempt, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := runs.PrepareToolStep(
		ctx, attempt.ID, 0, "sevt_tool_1", "bash", map[string]any{"command": "touch marker"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.CompleteToolStep(ctx, step.ID, domain.ToolStepResult{}); !isConflict(err) {
		t.Fatalf("complete before start err = %v, want conflict", err)
	}
	if _, err := runs.StartToolStep(ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	step, err = runs.MarkToolStepAmbiguous(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if step.State != domain.ToolStepAmbiguous || step.Result != nil {
		t.Fatalf("ambiguous step = %#v", step)
	}
	if _, err := runs.FinishAttempt(ctx, attempt.ID, domain.RunAttemptInterrupted, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.BeginAttempt(ctx, run.ID); !isConflict(err) {
		t.Fatalf("retry with ambiguous side effect err = %v, want conflict", err)
	}
}

func TestExecutionStore_CompletedStepInFailedAttemptBlocksAnotherAttempt(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	run := claimExecutionRun(t, runs, session)
	attempt, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := runs.PrepareToolStep(
		ctx, attempt.ID, 0, "sevt_tool_1", "write", map[string]any{"path": "marker"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.StartToolStep(ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.CompleteToolStep(ctx, step.ID, domain.ToolStepResult{}); err != nil {
		t.Fatal(err)
	}
	message := "model failed after the tool completed"
	if _, err := runs.FinishAttempt(ctx, attempt.ID, domain.RunAttemptFailed, &message); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.BeginAttempt(ctx, run.ID); !isConflict(err) {
		t.Fatalf("retry after completed side effect err = %v, want conflict", err)
	}
}

func TestExecutionStore_SafeFailedAttemptCanBeRetried(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	run := claimExecutionRun(t, runs, session)
	first, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.PrepareToolStep(
		ctx, first.ID, 0, "sevt_tool_1", "read", map[string]any{"path": "missing"},
	); err != nil {
		t.Fatal(err)
	}
	message := "model connection failed before tool execution"
	if _, err := runs.FinishAttempt(ctx, first.ID, domain.RunAttemptFailed, &message); err != nil {
		t.Fatal(err)
	}
	second, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.AttemptNo != 2 {
		t.Fatalf("second attempt number = %d, want 2", second.AttemptNo)
	}
}

func TestExecutionStore_JournalSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: now}
	if err := NewAgentRepo(db).PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "agent", Model: domain.Model{ID: "model"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewEnvironmentRepo(db).Put(ctx, domain.Environment{
		ID: "env_1", Name: "environment", ConfigType: "cloud",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "sesn_1", AgentID: "agent_1", AgentVersion: 1, EnvironmentID: "env_1",
		Status: domain.StatusIdle, CreatedAt: now, UpdatedAt: now,
	}
	runs := NewRunStore(db, ids, clk)
	run := claimExecutionRun(t, runs, session)
	attempt, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := runs.PrepareToolStep(
		ctx, attempt.ID, 0, "sevt_tool_1", "write", map[string]any{"path": "state.txt"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.StartToolStep(ctx, step.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedRuns := NewRunStore(reopened, domain.NewSeqIDGen(), clk)
	storedAttempt, err := reopenedRuns.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedStep, err := reopenedRuns.GetToolStep(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedAttempt.State != domain.RunAttemptActive ||
		storedStep.State != domain.ToolStepStarted ||
		storedStep.ToolUseEventID != "sevt_tool_1" {
		t.Fatalf("reopened attempt=%#v step=%#v", storedAttempt, storedStep)
	}
}

func TestExecutionStore_SessionDeleteCascadesJournal(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	run := claimExecutionRun(t, runs, session)
	attempt, err := runs.BeginAttempt(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.PrepareToolStep(
		ctx, attempt.ID, 0, "sevt_tool_1", "read", map[string]any{"path": "file"},
	); err != nil {
		t.Fatal(err)
	}
	if err := NewSessionRepo(db).Delete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	var attempts, steps int
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_steps`).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || steps != 0 {
		t.Fatalf("journal rows after session delete: attempts=%d steps=%d", attempts, steps)
	}
}
