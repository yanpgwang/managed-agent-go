package temporal_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// TestVerticalSlice_EndToEnd is the real integration path required by the
// milestone: a genuine Temporal service + real PostgreSQL. It admits one
// user.message and asserts the full spine runs — admission writes the outbox,
// the relay delivers a Signal-With-Start, the SessionWorkflow drives the
// Workflow-owned model loop through granular Activities, and the turn's
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
	modelClient := model.NewFake()

	runtime := temporalpkg.NewRuntimeOnTaskQueue(
		c,
		store,
		modelClient,
		sandbox.NewLocalProvider(),
		ids,
		temporalpkg.RelayConfig{PollInterval: 200 * time.Millisecond},
		"managed-agent-test-"+ids.NewID(""),
	)

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
	defer terminateIntegrationWorkflow(t, c, sess.ID)

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
// runs the tool step as its own Activity under the durable journal and commits
// the paired agent.tool_use / agent.tool_result plus a terminal idle to
// PostgreSQL. Skips unless both the DB and Temporal env vars are set.
func TestVerticalSlice_ToolStepEndToEnd(t *testing.T) {
	runToolStepEndToEnd(t, sandbox.NewLocalProvider(), model.NewFake(), "sess_tool_e2e_", "")
}

// TestVerticalSlice_DockerToolStepEndToEnd runs the same real PostgreSQL +
// Temporal tool path through the Docker sandbox provider. Unlike the generic
// fake-model path, its model requests a real shell command that checks
// /.dockerenv, writes inside /workspace, and reads the marker back. The committed
// non-error tool_result therefore proves the Activity actually executed inside
// the provisioned container, not merely that Docker provisioning succeeded.
func TestVerticalSlice_DockerToolStepEndToEnd(t *testing.T) {
	if os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL") == "" ||
		os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT") == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the Docker tool end-to-end slice")
	}
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker CLI not installed")
	}
	if err := exec.Command(dockerPath, "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	provider, err := sandbox.NewDockerProvider(sandbox.DockerConfig{DockerPath: dockerPath, DefaultImage: "alpine:latest"})
	if err != nil {
		t.Fatalf("docker provider: %v", err)
	}
	const marker = "managed-agent-temporal-docker-ok"
	runToolStepEndToEnd(t, provider, dockerProbeModel{marker: marker}, "sess_docker_tool_e2e_", marker)
}

func runToolStepEndToEnd(t *testing.T, provider sandbox.Provider, modelClient model.Client, sessionPrefix, expectedToolOutput string) {
	t.Helper()
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
	runtime := temporalpkg.NewRuntimeOnTaskQueue(
		c,
		store,
		modelClient,
		provider,
		ids,
		temporalpkg.RelayConfig{PollInterval: 200 * time.Millisecond},
		"managed-agent-test-"+ids.NewID(""),
	)

	if err := runtime.Worker.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer runtime.Worker.Stop()
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go func() { _ = runtime.Relay.Run(relayCtx) }()

	orch := runtime.Orchestrator()
	sessID := sessionPrefix + ids.NewID("")
	// SessionManager keeps a sandbox alive across turns by design. Explicitly
	// release it after this integration test so the Docker variant cannot leak a
	// container (the second call is a harmless no-op after normal-path release).
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runtime.Sandbox.Release(releaseCtx, sessID)
	}()
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
	// The production workflow is intentionally long-lived at idle. Integration
	// tests use disposable PostgreSQL schemas, so terminate their execution before
	// cleanup drops the schema; otherwise a later local worker can pick up a stale
	// retry against data that no longer exists.
	defer terminateIntegrationWorkflow(t, c, sessID)

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
	if expectedToolOutput != "" {
		text, isError, ok := toolResult(events)
		if !ok {
			t.Fatalf("agent.tool_result missing; got %s", typeList(events))
		}
		if isError {
			t.Fatalf("Docker probe tool_result is_error=true; content=%q", text)
		}
		if !strings.Contains(text, expectedToolOutput) {
			t.Fatalf("Docker probe output %q does not contain %q", text, expectedToolOutput)
		}
	}

	final, err := store.GetSession(ctx, sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runtime.Sandbox.Release(releaseCtx, sessID); err != nil {
		t.Fatalf("release session sandbox: %v", err)
	}
}

// dockerProbeModel is a deterministic, retry-safe model client for the Docker
// integration test. Before a tool result exists it requests one real bash step;
// afterwards it ends the turn. Behavior depends only on projected history, not a
// mutable call counter, so an Activity retry receives the same response.
type dockerProbeModel struct {
	marker string
}

func (m dockerProbeModel) CreateMessage(_ context.Context, req model.Request) (model.Response, error) {
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" {
				return model.Response{
					Content:    []domain.ContentBlock{{Type: "text", Text: "Docker probe completed"}},
					StopReason: "end_turn",
				}, nil
			}
		}
	}
	command := "test -f /.dockerenv && test \"$(pwd)\" = /workspace && printf '" + m.marker + "' > probe.txt && cat probe.txt"
	return model.Response{
		Content: []domain.ContentBlock{{
			Type: "tool_use", ToolUseID: "docker_probe_tool_1", ToolName: "bash",
			Input: map[string]any{"command": command},
		}},
		StopReason: "tool_use",
	}, nil
}

func (m dockerProbeModel) CreateMessageStream(ctx context.Context, req model.Request, onDelta func(index int, text string)) (model.Response, error) {
	resp, err := m.CreateMessage(ctx, req)
	if err == nil && resp.StopReason == "end_turn" && len(resp.Content) == 1 && onDelta != nil {
		onDelta(0, resp.Content[0].Text)
	}
	return resp, err
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

func toolResult(events []domain.Event) (text string, isError bool, ok bool) {
	for _, event := range events {
		if event.Type != domain.EvAgentToolResult {
			continue
		}
		isError, _ = event.Payload["is_error"].(bool)
		content, _ := event.Payload["content"].([]any)
		var out strings.Builder
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			part, _ := block["text"].(string)
			out.WriteString(part)
		}
		return out.String(), isError, true
	}
	return "", false, false
}

func terminateIntegrationWorkflow(t *testing.T, c client.Client, workflowID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.TerminateWorkflow(ctx, workflowID, "", "managed-agent integration test cleanup"); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return
		}
		t.Errorf("terminate integration workflow %s: %v", workflowID, err)
	}
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
