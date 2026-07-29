package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

type countingSandboxLease struct {
	mu    sync.Mutex
	count int
}

func (l *countingSandboxLease) Acquire(
	context.Context,
	string,
	sandbox.Spec,
) (sandbox.Sandbox, error) {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
	return nil, nil
}

func (l *countingSandboxLease) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

type forwardingCountingLease struct {
	inner SandboxLease
	mu    sync.Mutex
	count int
}

func (l *forwardingCountingLease) Acquire(
	ctx context.Context,
	sessionID string,
	spec sandbox.Spec,
) (sandbox.Sandbox, error) {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
	return l.inner.Acquire(ctx, sessionID, spec)
}

func (l *forwardingCountingLease) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

type failAfterCompleteJournal struct {
	JournalStore
	mu     sync.Mutex
	failed bool
}

func (j *failAfterCompleteJournal) CompleteToolStep(
	ctx context.Context,
	stepID string,
	result domain.ToolStepResult,
) error {
	if err := j.JournalStore.CompleteToolStep(ctx, stepID, result); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.failed {
		j.failed = true
		return errors.New("tool result committed but Activity acknowledgement was lost")
	}
	return nil
}

func TestExecuteTool_CompletedStepReturnsWithoutReexecution(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	const sessionID = "sess_workflow_tool_completed"
	trigger := toolSession(t, store, sessionID)

	attempt, err := store.EnsureAttempt(ctx, sessionID, trigger, "ratm_workflow_completed")
	if err != nil {
		t.Fatalf("ensure attempt: %v", err)
	}
	step, err := store.EnsureToolStep(
		ctx,
		attempt.ID,
		"tstep_workflow_completed",
		0,
		"sevt_workflow_completed",
		"bash",
		map[string]any{"command": "echo once"},
	)
	if err != nil {
		t.Fatalf("ensure step: %v", err)
	}
	if err := store.StartToolStep(ctx, step.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.CompleteToolStep(ctx, step.ID, domain.ToolStepResult{
		Content: []any{map[string]any{"type": "text", "text": "once"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	lease := &countingSandboxLease{}
	source := storeSource{store: store}
	activities := NewActivities(nil, nil, source, source, lease, domain.NewRandomIDGen())
	result, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID:      sessionID,
		TriggerEventID: trigger,
		AttemptID:      attempt.ID,
		Ordinal:        0,
		ToolUseEventID: "sevt_workflow_completed",
		ToolStepID:     step.ID,
		ToolName:       "bash",
		Input:          map[string]any{"command": "echo once"},
	})
	if err != nil {
		t.Fatalf("execute retry: %v", err)
	}
	if result.Ambiguous || result.Result.Content[0].(map[string]any)["text"] != "once" {
		t.Fatalf("unexpected recovered result: %+v", result)
	}
	if lease.calls() != 0 {
		t.Fatalf("completed step reacquired a sandbox %d time(s)", lease.calls())
	}
}

func TestExecuteTool_StartedStepBecomesAmbiguousWithoutReexecution(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	const sessionID = "sess_workflow_tool_ambiguous"
	trigger := toolSession(t, store, sessionID)

	attempt, err := store.EnsureAttempt(ctx, sessionID, trigger, "ratm_workflow_ambiguous")
	if err != nil {
		t.Fatalf("ensure attempt: %v", err)
	}
	step, err := store.EnsureToolStep(
		ctx,
		attempt.ID,
		"tstep_workflow_ambiguous",
		0,
		"sevt_workflow_ambiguous",
		"bash",
		map[string]any{"command": "side effect"},
	)
	if err != nil {
		t.Fatalf("ensure step: %v", err)
	}
	if err := store.StartToolStep(ctx, step.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	lease := &countingSandboxLease{}
	source := storeSource{store: store}
	activities := NewActivities(nil, nil, source, source, lease, domain.NewRandomIDGen())
	result, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID:      sessionID,
		TriggerEventID: trigger,
		AttemptID:      attempt.ID,
		Ordinal:        0,
		ToolUseEventID: "sevt_workflow_ambiguous",
		ToolStepID:     step.ID,
		ToolName:       "bash",
		Input:          map[string]any{"command": "side effect"},
	})
	if err != nil {
		t.Fatalf("execute retry: %v", err)
	}
	if !result.Ambiguous {
		t.Fatalf("started step must be reported ambiguous: %+v", result)
	}
	if lease.calls() != 0 {
		t.Fatalf("ambiguous step reacquired a sandbox %d time(s)", lease.calls())
	}
	state, ok, err := store.ToolStepStateByEventID(ctx, "sevt_workflow_ambiguous")
	if err != nil || !ok || state != domain.ToolStepAmbiguous {
		t.Fatalf("state = %s ok=%v err=%v", state, ok, err)
	}
}

func TestWorkflowTurn_CompletedToolSurvivesActivityRetry(t *testing.T) {
	store := newToolTestStore(t)
	const sessionID = "sess_workflow_activity_retry"
	trigger := toolSession(t, store, sessionID)

	ids := domain.NewRandomIDGen()
	source := storeSource{store: store}
	journal := &failAfterCompleteJournal{JournalStore: source}
	manager := sandbox.NewSessionManager(sandbox.NewLocalProvider())
	lease := &forwardingCountingLease{inner: manager}
	t.Cleanup(func() {
		_ = manager.Release(context.Background(), sessionID)
	})
	modelClient := model.NewFake()
	activities := NewActivities(nil, modelClient, source, journal, lease, ids)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)
	env.RegisterActivityWithOptions(activities.PrepareTurn, activity.RegisterOptions{Name: ActivityPrepareTurn})
	env.RegisterActivityWithOptions(activities.CallModel, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(activities.ExecuteTool, activity.RegisterOptions{Name: ActivityExecuteTool})
	env.RegisterActivityWithOptions(activities.CompleteWorkflowTurn, activity.RegisterOptions{Name: ActivityCompleteWorkflowTurn})

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: sessionID, TriggerEventID: trigger,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	if lease.calls() != 1 {
		t.Fatalf("completed tool was re-executed: sandbox acquired %d times", lease.calls())
	}
	events, err := store.EventsAfter(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	assertHasType(t, events, domain.EvAgentToolUse)
	assertHasType(t, events, domain.EvAgentToolResult)
	assertHasType(t, events, domain.EvSessionStatusIdle)
}
