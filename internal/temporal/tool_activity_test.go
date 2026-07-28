package temporal

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

var toolSchemaSeq atomic.Int64

// newToolTestStore provisions an isolated PostgreSQL schema for a tool-path test
// and returns a pg.Store bound to it. It skips when no test database is set, so
// `go test ./...` stays offline.
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

// toolSession builds a session whose agent snapshot enables the built-in toolset
// (default always_allow) and admits one user.message, returning the store, the
// session id, and the trigger event id.
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
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "use a tool"}}},
	}})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return adm.Events[0].ID
}

// TestRunTurn_ToolStepHappyPath drives a real built-in tool step through the
// RunTurn Activity: AgentCore + fake model requests the first enabled built-in
// (bash), the journal records prepared -> started -> completed, and the turn
// commits the paired agent.tool_use / agent.tool_result plus a terminal idle.
func TestRunTurn_ToolStepHappyPath(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	trigger := toolSession(t, store, "sess_tool_ok")

	ids := domain.NewRandomIDGen()
	rt := agentruntime.NewAgentCore(model.NewFake(), ids)
	sandboxes := sandbox.NewSessionManager(sandbox.NewLocalProvider())
	src := storeSource{store: store}
	acts := NewActivities(rt, src, src, sandboxes, ids)

	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_tool_ok", TriggerEventID: trigger}); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	events, err := store.EventsAfter(ctx, "sess_tool_ok", 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	assertHasType(t, events, domain.EvAgentToolUse)
	assertHasType(t, events, domain.EvAgentToolResult)
	assertHasType(t, events, domain.EvSessionStatusIdle)

	// The journal recorded a completed step for the emitted tool_use.
	var toolUseID string
	for _, e := range events {
		if e.Type == domain.EvAgentToolUse {
			toolUseID = e.ID
		}
	}
	if toolUseID == "" {
		t.Fatal("no agent.tool_use committed")
	}
	state, ok, err := store.ToolStepStateByEventID(ctx, toolUseID)
	if err != nil || !ok {
		t.Fatalf("tool step state: ok=%v err=%v", ok, err)
	}
	if state != domain.ToolStepCompleted {
		t.Fatalf("expected completed tool step, got %s", state)
	}

	final, err := store.GetSession(ctx, "sess_tool_ok")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}
}

// crashAfterStartRuntime is a fake AgentRuntime that starts a tool step (the
// side effect may now have happened) and then returns an error before recording
// a result, modelling a worker crash mid-tool. It counts how many times it
// actually executed so a silent replay would be visible.
type crashAfterStartRuntime struct {
	mu        sync.Mutex
	execCount int
}

func (r *crashAfterStartRuntime) Run(ctx context.Context, req agentruntime.RunRequest, sink agentruntime.EventSink) (agentruntime.RunOutcome, error) {
	r.mu.Lock()
	r.execCount++
	r.mu.Unlock()
	stepID, err := req.ToolJournal.Prepare(ctx, 0, "tue_crash", "bash", map[string]any{"command": "side-effect"})
	if err != nil {
		return agentruntime.RunOutcome{}, err
	}
	if err := req.ToolJournal.Start(ctx, stepID); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	// Crash: return before Complete, leaving the step started with no result.
	return agentruntime.RunOutcome{}, context.DeadlineExceeded
}

func (r *crashAfterStartRuntime) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execCount
}

// TestRunTurn_AmbiguousToolNotReplayed proves the core safety property under
// Activity retry: attempt 1 starts a tool step then crashes; the retry recovers
// the started step as ambiguous, refuses to re-execute, and terminates the turn
// honestly. The executor must run exactly once — the side effect is never
// silently replayed.
func TestRunTurn_AmbiguousToolNotReplayed(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	trigger := toolSession(t, store, "sess_tool_amb")

	ids := domain.NewRandomIDGen()
	crash := &crashAfterStartRuntime{}
	sandboxes := sandbox.NewSessionManager(sandbox.NewLocalProvider())
	src := storeSource{store: store}
	acts := NewActivities(crash, src, src, sandboxes, ids)

	// Attempt 1: the runtime starts the tool then "crashes"; RunTurn surfaces the
	// error (Temporal would retry).
	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_tool_amb", TriggerEventID: trigger}); err == nil {
		t.Fatal("attempt 1 should surface the crash error")
	}
	if crash.count() != 1 {
		t.Fatalf("expected executor to run once, got %d", crash.count())
	}

	// Attempt 2 (retry): recovery classifies the started step ambiguous and RunTurn
	// refuses to re-run, terminating the turn. The runtime is NOT invoked again.
	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_tool_amb", TriggerEventID: trigger}); err != nil {
		t.Fatalf("attempt 2 should resolve to an honest termination, got error: %v", err)
	}
	if crash.count() != 1 {
		t.Fatalf("side effect was silently replayed: executor ran %d times", crash.count())
	}

	// The step is ambiguous, and the session terminated with an error.
	state, ok, err := store.ToolStepStateByEventID(ctx, "tue_crash")
	if err != nil || !ok {
		t.Fatalf("tool step state: ok=%v err=%v", ok, err)
	}
	if state != domain.ToolStepAmbiguous {
		t.Fatalf("expected ambiguous, got %s", state)
	}
	events, err := store.EventsAfter(ctx, "sess_tool_amb", 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	assertHasType(t, events, domain.EvSessionError)
	assertHasType(t, events, domain.EvSessionStatusTerminated)
	final, err := store.GetSession(ctx, "sess_tool_amb")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if final.Status != domain.StatusTerminated {
		t.Fatalf("expected terminated, got %s", final.Status)
	}
}

// TestRunTurn_IdempotentAfterProcessed proves a retry of an already-completed
// turn does not re-invoke the runtime.
func TestRunTurn_IdempotentAfterProcessed(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	trigger := toolSession(t, store, "sess_tool_idem")

	ids := domain.NewRandomIDGen()
	counting := &countingRuntime{inner: agentruntime.NewAgentCore(model.NewFake(), ids)}
	sandboxes := sandbox.NewSessionManager(sandbox.NewLocalProvider())
	src := storeSource{store: store}
	acts := NewActivities(counting, src, src, sandboxes, ids)

	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_tool_idem", TriggerEventID: trigger}); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_tool_idem", TriggerEventID: trigger}); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if counting.count() != 1 {
		t.Fatalf("expected runtime invoked once (trigger processed short-circuits retry), got %d", counting.count())
	}
}

type countingRuntime struct {
	inner agentruntime.AgentRuntime
	mu    sync.Mutex
	n     int
}

func (r *countingRuntime) Run(ctx context.Context, req agentruntime.RunRequest, sink agentruntime.EventSink) (agentruntime.RunOutcome, error) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return r.inner.Run(ctx, req, sink)
}

func (r *countingRuntime) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func assertHasType(t *testing.T, events []domain.Event, typ string) {
	t.Helper()
	for _, e := range events {
		if e.Type == typ {
			return
		}
	}
	t.Fatalf("expected an event of type %s; not found", typ)
}

// TestRunTurn_TerminationReportedAndBQueuedUnprocessed proves the activity side
// of the P0 termination-propagation fix. A batch A,B is admitted; A's turn is
// misconfigured (an invalid toolset) so RunTurn terminates the session. The
// result reports Terminated=true, a retry of A (now processed) still reports
// Terminated from the projection, and B is never processed — it stays queued
// while the session remains terminated.
func TestRunTurn_TerminationReportedAndBQueuedUnprocessed(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	sess := domain.Session{
		ID: "sess_term_prop", AgentID: "agent_1", AgentVersion: 1, EnvironmentID: "env_1",
		Status: domain.StatusIdle, Metadata: map[string]any{},
		AgentSnapshot: domain.Agent{
			ID: "agent_1", Version: 1, Model: domain.Model{ID: "fake"},
			// An unknown tool type makes domain.ParseTools fail, so RunTurn takes the
			// honest-termination path without ever invoking the model.
			Tools: []any{map[string]any{"type": "bogus_tool_type"}},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	adm, err := store.AdmitEvents(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "A"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "B"}}}},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	var idA, idB string
	for _, e := range adm.Events {
		if e.Type != domain.EvUserMessage {
			continue
		}
		if e.Payload["content"].([]any)[0].(map[string]any)["text"] == "A" {
			idA = e.ID
		} else {
			idB = e.ID
		}
	}

	ids := domain.NewRandomIDGen()
	src := storeSource{store: store}
	acts := NewActivities(agentruntime.NewAgentCore(model.NewFake(), ids), src, src, sandbox.NewSessionManager(sandbox.NewLocalProvider()), ids)

	// A terminates the session.
	resA, err := acts.RunTurn(ctx, RunTurnInput{SessionID: sess.ID, TriggerEventID: idA})
	if err != nil {
		t.Fatalf("run A: %v", err)
	}
	if !resA.Terminated {
		t.Fatal("turn A should report Terminated")
	}
	final, err := store.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusTerminated {
		t.Fatalf("session should be terminated, got %s", final.Status)
	}

	// A retry of A (now processed) must still report Terminated from the
	// projection — not be mistaken for an ordinary completion.
	resARetry, err := acts.RunTurn(ctx, RunTurnInput{SessionID: sess.ID, TriggerEventID: idA})
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if !resARetry.Terminated {
		t.Fatal("retry of a terminated turn must still report Terminated")
	}

	// B was never processed by the workflow (the workflow stops on termination),
	// so its trigger stays unprocessed.
	bEvent, err := store.GetEvent(ctx, sess.ID, idB)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if bEvent.ProcessedAt != nil {
		t.Fatal("B must remain unprocessed after the session terminated")
	}
}

// cancelBeforeCompleteRuntime prepares and starts a tool step, simulates its side
// effect, then cancels the Activity context BEFORE recording Complete. It proves
// the completion write itself — not only later turn finalization — is detached
// from Activity cancellation.
type cancelBeforeCompleteRuntime struct {
	cancel context.CancelFunc
}

func (r *cancelBeforeCompleteRuntime) Run(ctx context.Context, req agentruntime.RunRequest, sink agentruntime.EventSink) (agentruntime.RunOutcome, error) {
	stepID, err := req.ToolJournal.Prepare(ctx, 0, "tue_cancel", "bash", map[string]any{"command": "echo hi"})
	if err != nil {
		return agentruntime.RunOutcome{}, err
	}
	if err := req.ToolJournal.Start(ctx, stepID); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	// The tool side effect has happened. Cancel before Complete so this call
	// exercises activityToolJournal's WithoutCancel context directly.
	r.cancel()
	if err := req.ToolJournal.Complete(ctx, stepID, domain.ToolStepResult{
		Content: []any{map[string]any{"type": "text", "text": "ok"}},
	}); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	// Emit the paired events + a message so the turn has output to commit.
	if _, err := sink.Emit(ctx, []domain.EventDraft{
		{ID: "tue_cancel", Type: domain.EvAgentToolUse, Payload: map[string]any{"name": "bash", "input": map[string]any{"command": "echo hi"}}},
		{Type: domain.EvAgentToolResult, Payload: map[string]any{"tool_use_id": "tue_cancel", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "is_error": false}},
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}},
	}); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	return agentruntime.RunOutcome{}, nil
}

// TestRunTurn_DurableWritesSurviveCancellation proves that a cancellation of the
// Activity context arriving AFTER a tool step's side effect does not prevent
// recording the completed step, finishing the attempt, or committing the turn:
// those durable writes run on a WithoutCancel context. Without that, a cancel
// here would leave the step started (recovered later as ambiguous) and the turn
// uncommitted, losing the fact that the side effect already succeeded.
func TestRunTurn_DurableWritesSurviveCancellation(t *testing.T) {
	store := newToolTestStore(t)
	baseCtx := context.Background()
	trigger := toolSession(t, store, "sess_cancel_durable")

	ctx, cancel := context.WithCancel(baseCtx)
	ids := domain.NewRandomIDGen()
	rt := &cancelBeforeCompleteRuntime{cancel: cancel}
	src := storeSource{store: store}
	acts := NewActivities(rt, src, src, sandbox.NewSessionManager(sandbox.NewLocalProvider()), ids)

	res, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_cancel_durable", TriggerEventID: trigger})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if res.Terminated {
		t.Fatal("a normal (canceled-before-complete) turn should not be terminated")
	}

	// The tool step is durably completed despite the cancellation.
	state, ok, err := store.ToolStepStateByEventID(baseCtx, "tue_cancel")
	if err != nil || !ok {
		t.Fatalf("tool step state: ok=%v err=%v", ok, err)
	}
	if state != domain.ToolStepCompleted {
		t.Fatalf("expected completed tool step despite cancellation, got %s", state)
	}

	// The turn committed: trigger processed and agent output present.
	tEvent, err := store.GetEvent(baseCtx, "sess_cancel_durable", trigger)
	if err != nil {
		t.Fatalf("get trigger: %v", err)
	}
	if tEvent.ProcessedAt == nil {
		t.Fatal("trigger must be marked processed despite cancellation")
	}
	events, err := store.EventsAfter(baseCtx, "sess_cancel_durable", 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	assertHasType(t, events, domain.EvAgentToolResult)
	assertHasType(t, events, domain.EvSessionStatusIdle)
}

// completeThenCrashRuntime runs a full tool step to completed, then returns an
// error before the turn commits — modelling a crash after the tool result is
// durable but before turn completion. It counts executions so a silent replay is
// visible.
type completeThenCrashRuntime struct {
	mu        sync.Mutex
	execCount int
}

func (r *completeThenCrashRuntime) Run(ctx context.Context, req agentruntime.RunRequest, sink agentruntime.EventSink) (agentruntime.RunOutcome, error) {
	r.mu.Lock()
	r.execCount++
	r.mu.Unlock()
	stepID, err := req.ToolJournal.Prepare(ctx, 0, "tue_completed", "bash", map[string]any{"command": "echo done"})
	if err != nil {
		return agentruntime.RunOutcome{}, err
	}
	if err := req.ToolJournal.Start(ctx, stepID); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	if err := req.ToolJournal.Complete(ctx, stepID, domain.ToolStepResult{
		Content: []any{map[string]any{"type": "text", "text": "done"}},
	}); err != nil {
		return agentruntime.RunOutcome{}, err
	}
	// The step is durably COMPLETED. Now crash before the turn commits.
	return agentruntime.RunOutcome{}, context.DeadlineExceeded
}

func (r *completeThenCrashRuntime) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execCount
}

// TestRunTurn_CompletedStepNotReplayedNotCalledAmbiguous proves the
// completed-vs-ambiguous distinction. A prior attempt completed a tool step, then
// crashed before the turn committed. On retry:
//   - the step stays 'completed' (it is NOT reclassified ambiguous — a completed
//     result is trustworthy);
//   - RunTurn refuses to re-run and terminates honestly, so the tool executes
//     exactly once (no silent replay);
//   - the honest termination message does NOT call the outcome "ambiguous".
//
// Resuming the model loop from the durable completed result is deferred; this
// test pins the honest interim behavior.
func TestRunTurn_CompletedStepNotReplayedNotCalledAmbiguous(t *testing.T) {
	store := newToolTestStore(t)
	ctx := context.Background()
	trigger := toolSession(t, store, "sess_completed_boundary")

	ids := domain.NewRandomIDGen()
	rt := &completeThenCrashRuntime{}
	src := storeSource{store: store}
	acts := NewActivities(rt, src, src, sandbox.NewSessionManager(sandbox.NewLocalProvider()), ids)

	// Attempt 1: completes the step, then crashes before commit.
	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_completed_boundary", TriggerEventID: trigger}); err == nil {
		t.Fatal("attempt 1 should surface the crash error")
	}
	if rt.count() != 1 {
		t.Fatalf("expected one execution, got %d", rt.count())
	}
	// The step is completed (not ambiguous): a durable result is trustworthy.
	state, ok, err := store.ToolStepStateByEventID(ctx, "tue_completed")
	if err != nil || !ok {
		t.Fatalf("state: ok=%v err=%v", ok, err)
	}
	if state != domain.ToolStepCompleted {
		t.Fatalf("a completed step must stay completed, got %s", state)
	}

	// Attempt 2 (retry): recovery leaves the completed step as-is, reports prior
	// execution, and RunTurn terminates honestly WITHOUT re-executing.
	if _, err := acts.RunTurn(ctx, RunTurnInput{SessionID: "sess_completed_boundary", TriggerEventID: trigger}); err != nil {
		t.Fatalf("attempt 2 should resolve to honest termination, got: %v", err)
	}
	if rt.count() != 1 {
		t.Fatalf("completed step was silently replayed: executor ran %d times", rt.count())
	}
	state, _, err = store.ToolStepStateByEventID(ctx, "tue_completed")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state == domain.ToolStepAmbiguous {
		t.Fatal("a completed step must NOT be reclassified as ambiguous")
	}
	if state != domain.ToolStepCompleted {
		t.Fatalf("completed step should remain completed, got %s", state)
	}

	// The termination error message must be honest: it must not call a completed
	// outcome "ambiguous".
	events, err := store.EventsAfter(ctx, "sess_completed_boundary", 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Type != domain.EvSessionError {
			continue
		}
		errObj, _ := e.Payload["error"].(map[string]any)
		msg, _ := errObj["message"].(string)
		if strings.Contains(strings.ToLower(msg), "ambiguous") {
			t.Fatalf("termination message for a completed step must not say 'ambiguous': %q", msg)
		}
	}
}
