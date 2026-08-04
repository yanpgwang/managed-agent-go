package temporal

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

var toolSchemaSeq atomic.Int64

func newToolTestStore(t *testing.T) *pg.Store {
	t.Helper()
	url := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MANAGED_AGENT_TEST_DATABASE_URL not set; skipping PostgreSQL tool-path test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	schema := "tool_test_" + itoaTest(toolSchemaSeq.Add(1))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		pool.Close()
		t.Skipf("cannot create schema (db unreachable?): %v", err)
	}
	if err := pg.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})
	return pg.NewStore(pool, domain.NewRandomIDGen(), toolClock{})
}

type toolClock struct{}

func (toolClock) Now() time.Time { return time.Now().UTC() }

func toolSession(t *testing.T, store *pg.Store, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	sess := domain.Session{
		ID:            sessionID,
		AgentID:       "agent_1",
		AgentVersion:  1,
		EnvironmentID: "env_1",
		Status:        domain.StatusIdle,
		Metadata:      map[string]any{},
		AgentSnapshot: domain.Agent{
			ID:      "agent_1",
			Version: 1,
			Model:   domain.Model{ID: "fake"},
			Tools:   []any{map[string]any{"type": domain.BuiltinToolsetType}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	adm, err := store.AdmitEvents(ctx, sessionID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "use a tool"}},
		},
	}})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return adm.Events[0].ID
}

func assertHasType(t *testing.T, events []domain.Event, typ string) {
	t.Helper()
	for _, event := range events {
		if event.Type == typ {
			return
		}
	}
	t.Fatalf("expected an event of type %s; not found", typ)
}

type countingSandboxLease struct {
	mu    sync.Mutex
	count int
}

type permanentResourceReconciler struct{}

type permanentSandboxLease struct{}

func (permanentSandboxLease) Acquire(
	context.Context,
	string,
	sandbox.Spec,
) (sandbox.Sandbox, error) {
	return nil, sandbox.Permanent(errors.New("sandbox ownership mismatch"))
}

func (permanentSandboxLease) Release(context.Context, string) error { return nil }

func (permanentResourceReconciler) Reconcile(
	context.Context,
	string,
	sandbox.Sandbox,
) error {
	return sandbox.Permanent(errors.New("resource provider mismatch"))
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

func (*countingSandboxLease) Release(context.Context, string) error { return nil }

func (l *countingSandboxLease) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

type forwardingCountingLease struct {
	inner SandboxLease
	mu    sync.Mutex
	count int
	spec  sandbox.Spec
}

func (l *forwardingCountingLease) Acquire(
	ctx context.Context,
	sessionID string,
	spec sandbox.Spec,
) (sandbox.Sandbox, error) {
	l.mu.Lock()
	l.count++
	l.spec = spec
	l.mu.Unlock()
	return l.inner.Acquire(ctx, sessionID, spec)
}

func (l *forwardingCountingLease) Release(ctx context.Context, sessionID string) error {
	return l.inner.Release(ctx, sessionID)
}

func (l *forwardingCountingLease) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

func (l *forwardingCountingLease) lastSpec() sandbox.Spec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spec
}

type loseFirstCompletionAckJournal struct {
	JournalStore
	mu     sync.Mutex
	failed bool
}

func (j *loseFirstCompletionAckJournal) CompleteToolStep(
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
	activities := NewActivities(nil, source, source, lease, domain.NewRandomIDGen())
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
	activities := NewActivities(nil, source, source, lease, domain.NewRandomIDGen())
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

func TestExecuteTool_ResourcePermanentErrorTerminatesTurn(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	const sessionID = "sess_resource_permanent"
	trigger := toolSession(t, store, sessionID)
	lease := &countingSandboxLease{}
	source := storeSource{store: store}
	activities := NewActivities(nil, source, source, lease, domain.NewRandomIDGen()).
		WithSandboxResourceReconciler(permanentResourceReconciler{})

	result, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID: sessionID, TriggerEventID: trigger,
		AttemptID: "ratm_resource_permanent", Ordinal: 0,
		ToolUseEventID: "sevt_resource_permanent",
		ToolStepID:     "tstep_resource_permanent",
		ToolName:       "bash",
		Input:          map[string]any{"command": "true"},
	})
	if err != nil {
		t.Fatalf("ExecuteTool error = %v, want terminal result", err)
	}
	if result.FatalError == "" {
		t.Fatalf("ExecuteTool result = %+v, want fatal error", result)
	}
}

func TestExecuteTool_SandboxPermanentErrorTerminatesTurn(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	const sessionID = "sess_sandbox_permanent"
	trigger := toolSession(t, store, sessionID)
	source := storeSource{store: store}
	activities := NewActivities(
		nil, source, source, permanentSandboxLease{}, domain.NewRandomIDGen(),
	)

	result, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID: sessionID, TriggerEventID: trigger,
		AttemptID: "ratm_sandbox_permanent", Ordinal: 0,
		ToolUseEventID: "sevt_sandbox_permanent",
		ToolStepID:     "tstep_sandbox_permanent",
		ToolName:       "bash",
		Input:          map[string]any{"command": "true"},
	})
	if err != nil {
		t.Fatalf("ExecuteTool error = %v, want terminal result", err)
	}
	if result.FatalError == "" {
		t.Fatalf("ExecuteTool result = %+v, want fatal error", result)
	}
}

func TestWorkflowTurn_ToolResultWriteRetryDoesNotReexecute(t *testing.T) {
	store := newToolTestStore(t)
	const sessionID = "sess_workflow_activity_retry"
	trigger := toolSession(t, store, sessionID)

	ids := domain.NewRandomIDGen()
	source := storeSource{store: store}
	journal := &loseFirstCompletionAckJournal{JournalStore: source}
	manager := sandbox.NewSessionManager(sandbox.NewLocalProvider(), store)
	lease := &forwardingCountingLease{inner: manager}
	t.Cleanup(func() {
		_ = manager.Release(context.Background(), sessionID)
	})
	modelClient := model.NewFake()
	activities := NewActivities(modelClient, source, journal, lease, ids)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)
	env.RegisterActivityWithOptions(activities.PrepareTurn, activity.RegisterOptions{Name: ActivityPrepareTurn})
	env.RegisterActivityWithOptions(activities.CallModel, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(activities.StartModelRequest, activity.RegisterOptions{Name: ActivityStartModelRequest})
	env.RegisterActivityWithOptions(activities.AppendWorkflowEvents, activity.RegisterOptions{Name: ActivityAppendWorkflowEvents})
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
	if spec := lease.lastSpec(); spec.Network != defaultCloudSandboxNetwork {
		t.Fatalf("sandbox network = %q, want %q", spec.Network, defaultCloudSandboxNetwork)
	}
	events, err := store.EventsAfter(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	assertHasType(t, events, domain.EvAgentToolUse)
	assertHasType(t, events, domain.EvAgentToolResult)
	assertHasType(t, events, domain.EvSessionStatusIdle)
}
