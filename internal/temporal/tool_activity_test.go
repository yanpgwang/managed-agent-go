package temporal

import (
	"context"
	"os"
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
