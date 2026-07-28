package temporal_test

import (
	"context"
	"os"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// TestVerticalSlice_EndToEnd is the real integration path required by the
// milestone: a genuine Temporal service + real PostgreSQL. It admits one
// user.message and asserts the full spine runs — admission writes the outbox,
// the relay delivers a Signal-With-Start, the SessionWorkflow drives a RunTurn
// Activity through the real agent runtime (offline fake model), and the turn's
// authoritative agent.message plus terminal idle land in PostgreSQL in receipt
// order with the session projected back to idle.
//
// It skips unless BOTH MANAGED_AGENT_TEST_DATABASE_URL and
// MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT are set, so `go test ./...` passes with no
// local stack. The local dev stack (deployments/local) satisfies both.
func TestVerticalSlice_EndToEnd(t *testing.T) {
	dbURL := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	hostPort := os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT")
	if dbURL == "" || hostPort == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the real end-to-end slice")
	}
	ctx := context.Background()

	// Isolated PostgreSQL schema for this test.
	store, cleanup := integrationStore(t, dbURL)
	defer cleanup()

	// Real Temporal client against the running dev cluster.
	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		t.Skipf("temporal unreachable at %s: %v", hostPort, err)
	}
	defer c.Close()

	ids := domain.NewRandomIDGen()
	rt := agentruntime.NewAgentCore(model.NewFake(), ids)

	runtime := temporalpkg.NewRuntime(c, store, rt, sandbox.NewLocalProvider(), ids, temporalpkg.RelayConfig{PollInterval: 200 * time.Millisecond})

	// Start the worker.
	if err := runtime.Worker.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer runtime.Worker.Stop()

	// Start the relay.
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go func() { _ = runtime.Relay.Run(relayCtx) }()

	// Create a session and admit one user.message through the orchestrator (which
	// admits to PostgreSQL and fast-path signals).
	orch := runtime.Orchestrator()
	sess := domain.Session{
		ID:            "sess_e2e_" + ids.NewID(""),
		AgentID:       "agent_1",
		AgentVersion:  1,
		EnvironmentID: "env_1",
		Status:        domain.StatusIdle,
		Metadata:      map[string]any{},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if _, _, err := orch.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := orch.Admit(ctx, sess.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello world"}}},
	}}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	// Poll PostgreSQL until the agent.message and terminal idle land.
	deadline := time.Now().Add(30 * time.Second)
	var events []domain.Event
	for time.Now().Before(deadline) {
		events, err = store.EventsAfter(ctx, sess.ID, 0, 100)
		if err != nil {
			t.Fatalf("events after: %v", err)
		}
		if hasType(events, domain.EvAgentMessage) && hasType(events, domain.EvSessionStatusIdle) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !hasType(events, domain.EvAgentMessage) {
		t.Fatalf("agent.message never committed; got %d events: %s", len(events), typeList(events))
	}
	if !hasType(events, domain.EvSessionStatusIdle) {
		t.Fatalf("terminal idle never committed; got: %s", typeList(events))
	}

	// Receipt order: user.message < status_running < agent.message < status_idle.
	assertOrder(t, events,
		domain.EvUserMessage,
		domain.EvSessionStatusRunning,
		domain.EvAgentMessage,
		domain.EvSessionStatusIdle,
	)

	// Session projected back to idle.
	final, err := store.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}

	// The outbox wakeup was consumed by the relay.
	if _, ok, err := store.PendingWakeup(ctx, sess.ID); err != nil || ok {
		t.Fatalf("expected no pending wakeup after processing: ok=%v err=%v", ok, err)
	}
}

// TestVerticalSlice_ToolStepEndToEnd is the real integration path for a
// tool-using turn: a session whose agent enables the built-in toolset admits one
// user.message, and the fake model requests the first enabled built-in. The turn
// runs the tool step under the durable journal inside the RunTurn Activity and
// commits the paired agent.tool_use / agent.tool_result plus a terminal idle to
// PostgreSQL. Skips unless both the DB and Temporal env vars are set.
func TestVerticalSlice_ToolStepEndToEnd(t *testing.T) {
	dbURL := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	hostPort := os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT")
	if dbURL == "" || hostPort == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the tool end-to-end slice")
	}
	ctx := context.Background()

	store, cleanup := integrationStore(t, dbURL)
	defer cleanup()

	c, err := client.Dial(client.Options{HostPort: hostPort})
	if err != nil {
		t.Skipf("temporal unreachable at %s: %v", hostPort, err)
	}
	defer c.Close()

	ids := domain.NewRandomIDGen()
	rt := agentruntime.NewAgentCore(model.NewFake(), ids)
	runtime := temporalpkg.NewRuntime(c, store, rt, sandbox.NewLocalProvider(), ids, temporalpkg.RelayConfig{PollInterval: 200 * time.Millisecond})

	if err := runtime.Worker.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer runtime.Worker.Stop()
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go func() { _ = runtime.Relay.Run(relayCtx) }()

	orch := runtime.Orchestrator()
	sessID := "sess_tool_e2e_" + ids.NewID("")
	sess := domain.Session{
		ID:            sessID,
		AgentID:       "agent_1",
		AgentVersion:  1,
		EnvironmentID: "env_1",
		Status:        domain.StatusIdle,
		Metadata:      map[string]any{},
		AgentSnapshot: domain.Agent{
			ID: "agent_1", Version: 1, Model: domain.Model{ID: "fake"},
			Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if _, _, err := orch.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := orch.Admit(ctx, sessID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "run a tool"}}},
	}}); err != nil {
		t.Fatalf("admit: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var events []domain.Event
	for time.Now().Before(deadline) {
		events, err = store.EventsAfter(ctx, sessID, 0, 100)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if hasType(events, domain.EvAgentToolResult) && hasType(events, domain.EvSessionStatusIdle) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	assertOrder(t, events,
		domain.EvUserMessage,
		domain.EvSessionStatusRunning,
		domain.EvAgentToolUse,
		domain.EvAgentToolResult,
		domain.EvSessionStatusIdle,
	)

	final, err := store.GetSession(ctx, sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}
}

func hasType(events []domain.Event, t string) bool {
	for _, e := range events {
		if e.Type == t {
			return true
		}
	}
	return false
}

func typeList(events []domain.Event) string {
	s := ""
	for _, e := range events {
		s += e.Type + " "
	}
	return s
}

// assertOrder checks that the given event types appear in the slice in the given
// relative order (not necessarily contiguous).
func assertOrder(t *testing.T, events []domain.Event, types ...string) {
	t.Helper()
	idx := 0
	for _, e := range events {
		if idx < len(types) && e.Type == types[idx] {
			idx++
		}
	}
	if idx != len(types) {
		t.Fatalf("events not in expected order %v; got %s", types, typeList(events))
	}
}
