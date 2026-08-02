package temporal_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	runVerticalSliceEndToEnd(t, model.NewFake(), "fake", 30*time.Second)
}

// TestVerticalSlice_LiveModelEndToEnd exercises the same durable platform path
// with a real Anthropic-shaped Messages endpoint. It is deliberately gated so
// normal development and CI never make billable, credentialed network calls.
func TestVerticalSlice_LiveModelEndToEnd(t *testing.T) {
	modelClient, modelID := liveModelForTest(t, "platform smoke test")
	runVerticalSliceEndToEnd(t, modelClient, modelID, 2*time.Minute)
}

func liveModelForTest(t *testing.T, purpose string) (model.Client, string) {
	t.Helper()
	if os.Getenv("MANAGED_AGENT_TEST_LIVE_MODEL") != "1" {
		t.Skipf("set MANAGED_AGENT_TEST_LIVE_MODEL=1 to run the live-model %s", purpose)
	}
	if os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL") == "" ||
		os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT") == "" {
		t.Skipf("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the live-model %s", purpose)
	}
	modelID := strings.TrimSpace(os.Getenv("MANAGED_AGENT_MODEL_ID"))
	if modelID == "" {
		t.Fatalf("MANAGED_AGENT_MODEL_ID is required for the live-model %s", purpose)
	}
	modelClient, configured, err := model.AnthropicFromEnv()
	if err != nil {
		t.Fatalf("configure live model: %v", err)
	}
	if !configured {
		t.Fatalf("MANAGED_AGENT_MODEL_BASE_URL and MANAGED_AGENT_MODEL_API_KEY are required for the live-model %s", purpose)
	}
	return modelClient, modelID
}

func runVerticalSliceEndToEnd(
	t *testing.T,
	modelClient model.Client,
	modelID string,
	testTimeout time.Duration,
) {
	t.Helper()
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
		AgentSnapshot: domain.Agent{
			ID: "agent_1", Version: 1, Model: domain.Model{ID: modelID},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
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
	deadline := time.Now().Add(testTimeout)
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

// TestLifecycleReconciler_RecoversPreparedDeletionEndToEnd models an API
// process exiting after PostgreSQL commits the deletion fence but before it
// starts Temporal cleanup. A worker-side scan must discover the row, release
// the persisted sandbox through the deterministic cleanup Workflow, and
// physically finalize the Session without another DELETE request.
func TestLifecycleReconciler_RecoversPreparedDeletionEndToEnd(t *testing.T) {
	dbURL := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	hostPort := os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT")
	if dbURL == "" || hostPort == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run lifecycle recovery")
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
		model.NewFake(),
		sandbox.NewLocalProvider(),
		ids,
		temporalpkg.RelayConfig{},
		"managed-agent-lifecycle-test-"+ids.NewID(""),
	)
	if err := runtime.Worker.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer runtime.Worker.Stop()

	session := domain.Session{
		ID:            "sess_lifecycle_" + ids.NewID(""),
		AgentID:       "agent_1",
		AgentVersion:  1,
		EnvironmentID: "env_1",
		Status:        domain.StatusIdle,
		Metadata:      map[string]any{},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	box, err := runtime.Sandbox.Acquire(ctx, session.ID, sandbox.Spec{})
	if err != nil {
		t.Fatalf("acquire sandbox: %v", err)
	}
	root := box.Root()
	if err := box.WriteFile(ctx, "before-crash", []byte("durable")); err != nil {
		t.Fatalf("write sandbox marker: %v", err)
	}
	if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("prepare deletion: %v", err)
	}

	reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := runtime.Lifecycle.RunOnce(reconcileCtx)
	if err != nil {
		t.Fatalf("reconcile deletion: %v", err)
	}
	if result.Deletions != 1 {
		t.Fatalf("reconciled deletions = %d, want 1", result.Deletions)
	}
	if _, err := store.GetSession(ctx, session.ID); err == nil {
		t.Fatal("session survived reconciled deletion")
	}
	if _, found, err := store.GetSandboxBinding(ctx, session.ID); err != nil || found {
		t.Fatalf("sandbox binding survived: found=%v err=%v", found, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox root survived cleanup or stat failed: %v", err)
	}
}

// TestVerticalSlice_InterruptCancelsModelActivity proves the cross-process
// cancellation path against real Temporal and PostgreSQL. The public interrupt
// is first committed to PostgreSQL, its metadata-only wakeup reaches the
// Workflow, the Workflow rereads the durable ledger, and only then requests
// cancellation of the heartbeat-enabled model Activity.
func TestVerticalSlice_InterruptCancelsModelActivity(t *testing.T) {
	dbURL := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	hostPort := os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT")
	if dbURL == "" || hostPort == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the interrupt end-to-end slice")
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
	blockingModel := newInterruptBlockingModel()
	runtime := temporalpkg.NewRuntimeOnTaskQueue(
		c,
		store,
		blockingModel,
		sandbox.NewLocalProvider(),
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
	sess := domain.Session{
		ID:            "sess_interrupt_e2e_" + ids.NewID(""),
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
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "block until interrupted"},
		}},
	}}); err != nil {
		t.Fatalf("admit message: %v", err)
	}
	defer terminateIntegrationWorkflow(t, c, sess.ID)

	select {
	case <-blockingModel.started:
	case <-time.After(15 * time.Second):
		t.Fatal("model Activity never started")
	}

	admitted, err := orch.Admit(ctx, sess.ID, []domain.EventDraft{{
		Type:    domain.EvUserInterrupt,
		Payload: map[string]any{},
	}})
	if err != nil {
		t.Fatalf("admit interrupt: %v", err)
	}
	if len(admitted) != 1 {
		t.Fatalf("interrupt events = %d, want 1", len(admitted))
	}
	interruptID := admitted[0].ID

	select {
	case <-blockingModel.canceled:
	case <-time.After(15 * time.Second):
		t.Fatal("durable interrupt did not cancel the model Activity context")
	}

	deadline := time.Now().Add(15 * time.Second)
	var events []domain.Event
	for time.Now().Before(deadline) {
		events, err = store.EventsAfter(ctx, sess.ID, 0, 100)
		if err != nil {
			t.Fatalf("events after: %v", err)
		}
		interrupt, getErr := store.GetEvent(ctx, sess.ID, interruptID)
		if getErr != nil {
			t.Fatalf("get interrupt: %v", getErr)
		}
		if interrupt.ProcessedAt != nil && hasType(events, domain.EvSessionStatusIdle) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	assertOrder(t, events,
		domain.EvUserMessage,
		domain.EvSessionStatusRunning,
		domain.EvUserInterrupt,
		domain.EvSessionStatusIdle,
	)
	idleCount := 0
	for _, event := range events {
		switch event.Type {
		case domain.EvSessionStatusIdle:
			idleCount++
			stopReason, _ := event.Payload["stop_reason"].(map[string]any)
			if stopReason["type"] != "end_turn" {
				t.Fatalf("interrupt stop reason = %#v, want end_turn", stopReason)
			}
		case domain.EvSessionError, domain.EvSessionStatusTerminated:
			t.Fatalf("interrupt published failure event %s", event.Type)
		case domain.EvAgentMessage:
			t.Fatal("canceled blocking model unexpectedly published agent.message")
		}
	}
	if idleCount != 1 {
		t.Fatalf("idle events = %d, want exactly 1; got %s", idleCount, typeList(events))
	}
	interrupt, err := store.GetEvent(ctx, sess.ID, interruptID)
	if err != nil {
		t.Fatalf("get final interrupt: %v", err)
	}
	if interrupt.ProcessedAt == nil {
		t.Fatal("interrupt was not marked processed with turn completion")
	}
	final, err := store.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("final status = %s, want idle", final.Status)
	}
}

// TestVerticalSlice_ToolStepEndToEnd is the real integration path for a
// tool-using turn: a session whose agent enables the built-in toolset admits one
// user.message, and a deterministic model requests bash with a valid command.
// The turn runs the tool step as its own Activity under the durable journal and
// commits agent.tool_use, agent.tool_result, the final agent.message, and
// terminal idle to PostgreSQL. Skips unless both the DB and Temporal env vars
// are set.
func TestVerticalSlice_ToolStepEndToEnd(t *testing.T) {
	const marker = "managed-agent-temporal-local-ok"
	runToolStepEndToEnd(t, toolStepCase{
		provider: sandbox.NewLocalProvider(),
		modelClient: toolProbeModel{
			command:   "printf '" + marker + "'",
			finalText: "Local probe completed",
		},
		modelID:            "fake",
		sessionPrefix:      "sess_tool_e2e_",
		prompt:             "run a tool",
		tools:              []any{map[string]any{"type": domain.BuiltinToolsetType}},
		expectedTool:       bashToolName,
		expectedToolOutput: marker,
		timeout:            30 * time.Second,
	})
}

// TestVerticalSlice_LiveModelToolStepEndToEnd verifies the external model
// contract beyond plain text streaming: a real model selects the offered bash
// tool, Mango executes it in Docker as a durable sandbox Activity, feeds the
// result back into the provider transcript, and commits the final assistant
// response. It is opt-in because it reaches a billable model endpoint.
func TestVerticalSlice_LiveModelToolStepEndToEnd(t *testing.T) {
	modelClient, modelID := liveModelForTest(t, "tool conformance test")
	provider := dockerProviderForTest(t, dockerRequired)
	const marker = "mango-live-tool-ok"
	runToolStepEndToEnd(t, toolStepCase{
		provider:      provider,
		modelClient:   modelClient,
		modelID:       modelID,
		sessionPrefix: "sess_live_tool_e2e_",
		prompt: fmt.Sprintf(
			"Use the bash tool exactly once. Pass the text between <command> tags as the command without changes; do not include the tags. <command>printf '%s' > live-tool.txt && cat live-tool.txt</command> After you receive the tool result, reply with a short confirmation and do not call another tool.",
			marker,
		),
		tools:              bashOnlyToolset(t),
		expectedTool:       bashToolName,
		expectedToolOutput: marker,
		timeout:            2 * time.Minute,
	})
}

// TestVerticalSlice_DockerToolStepEndToEnd runs the same real PostgreSQL +
// Temporal tool path through the Docker sandbox provider. Its command checks
// /.dockerenv and /workspace before writing and reading the marker. The committed
// non-error tool_result therefore proves the Activity actually executed inside
// the provisioned container, not merely that Docker provisioning succeeded.
func TestVerticalSlice_DockerToolStepEndToEnd(t *testing.T) {
	if os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL") == "" ||
		os.Getenv("MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT") == "" {
		t.Skip("set MANAGED_AGENT_TEST_DATABASE_URL and MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT to run the Docker tool end-to-end slice")
	}
	provider := dockerProviderForTest(t, dockerOptional)
	const marker = "managed-agent-temporal-docker-ok"
	runToolStepEndToEnd(t, toolStepCase{
		provider: provider,
		modelClient: toolProbeModel{
			command:   "test -f /.dockerenv && test \"$(pwd)\" = /workspace && printf '" + marker + "' > probe.txt && cat probe.txt",
			finalText: "Docker probe completed",
		},
		modelID:            "fake",
		sessionPrefix:      "sess_docker_tool_e2e_",
		prompt:             "run a tool",
		tools:              []any{map[string]any{"type": domain.BuiltinToolsetType}},
		expectedTool:       bashToolName,
		expectedToolOutput: marker,
		timeout:            30 * time.Second,
	})
}

type dockerRequirement bool

const (
	dockerOptional dockerRequirement = false
	dockerRequired dockerRequirement = true
)

func dockerProviderForTest(t *testing.T, requirement dockerRequirement) sandbox.Provider {
	t.Helper()
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		if requirement == dockerRequired {
			t.Fatalf("docker CLI is required for live tool conformance: %v", err)
		}
		t.Skip("docker CLI not installed")
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(probeCtx, dockerPath, "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		if requirement == dockerRequired {
			t.Fatalf("docker daemon is required for live tool conformance: %v", err)
		}
		t.Skipf("docker daemon unreachable: %v", err)
	}
	provider, err := sandbox.NewDockerProvider(sandbox.DockerConfig{DockerPath: dockerPath, DefaultImage: "alpine:latest"})
	if err != nil {
		t.Fatalf("docker provider: %v", err)
	}
	return provider
}

type toolStepCase struct {
	provider           sandbox.Provider
	modelClient        model.Client
	modelID            string
	sessionPrefix      string
	prompt             string
	tools              []any
	expectedTool       string
	expectedToolOutput string
	timeout            time.Duration
}

const bashToolName = "bash"

func bashOnlyToolset(t *testing.T) []any {
	t.Helper()
	raw := []any{map[string]any{
		"type": domain.BuiltinToolsetType,
		"default_config": map[string]any{
			"enabled": false,
			"permission_policy": map[string]any{
				"type": "always_allow",
			},
		},
		"configs": []any{map[string]any{
			"name": bashToolName, "enabled": true,
			"permission_policy": map[string]any{
				"type": "always_allow",
			},
		}},
	}}
	parsed, err := domain.ParseTools(raw)
	if err != nil {
		t.Fatalf("parse live tool configuration: %v", err)
	}
	bashEnabled, bashPolicy := parsed.BuiltinEnabled(bashToolName)
	if !bashEnabled || bashPolicy.Type != "always_allow" {
		t.Fatalf("live tool configuration enables bash = %v with policy %q, want true with always_allow", bashEnabled, bashPolicy.Type)
	}
	for _, name := range domain.BuiltinToolNames {
		enabled, policy := parsed.BuiltinEnabled(name)
		wantEnabled := name == bashToolName
		if enabled != wantEnabled {
			t.Fatalf("live tool configuration enables %q = %v, want %v", name, enabled, wantEnabled)
		}
		if enabled && policy.Type != "always_allow" {
			t.Fatalf("live tool configuration policy for %q = %q, want always_allow", name, policy.Type)
		}
	}
	return raw
}

func runToolStepEndToEnd(t *testing.T, tc toolStepCase) {
	t.Helper()
	if tc.provider == nil || tc.modelClient == nil {
		t.Fatal("tool step test provider and model client are required")
	}
	if tc.modelID == "" || tc.sessionPrefix == "" || tc.prompt == "" || tc.expectedTool == "" {
		t.Fatal("tool step test model ID, session prefix, prompt, and expected tool are required")
	}
	if tc.timeout <= 0 {
		t.Fatal("tool step test timeout must be positive")
	}
	if tc.expectedToolOutput == "" {
		t.Fatal("tool step test expected output must be non-empty")
	}
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
		tc.modelClient,
		tc.provider,
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
	sessID := tc.sessionPrefix + ids.NewID("")
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
			ID: "agent_1", Version: 1, Model: domain.Model{ID: tc.modelID},
			Tools: tc.tools,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if _, _, err := orch.CreateSession(ctx, sess, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := orch.Admit(ctx, sessID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": tc.prompt}}},
	}}); err != nil {
		t.Fatalf("admit: %v", err)
	}
	// The production workflow is intentionally long-lived at idle. Integration
	// tests use disposable PostgreSQL schemas, so terminate their execution before
	// cleanup drops the schema; otherwise a later local worker can pick up a stale
	// retry against data that no longer exists.
	defer terminateIntegrationWorkflow(t, c, sessID)

	expectedOrder := []string{
		domain.EvUserMessage,
		domain.EvSessionStatusRunning,
		domain.EvAgentToolUse,
		domain.EvAgentToolResult,
		domain.EvAgentMessage,
		domain.EvSessionStatusIdle,
	}
	deadline := time.Now().Add(tc.timeout)
	var events []domain.Event
	completed := false
	for time.Now().Before(deadline) {
		events, err = store.EventsAfter(ctx, sessID, 0, 100)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if failure, ok := firstFailureEvent(events); ok {
			t.Fatalf("tool workflow failed with %s: %#v; events=%s", failure.Type, failure.Payload, typeList(events))
		}
		if eventsHaveOrder(events, expectedOrder...) {
			completed = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("timed out after %s waiting for %v; got %s", tc.timeout, expectedOrder, typeList(events))
	}

	assertOrder(t, events, expectedOrder...)
	toolUses := eventsOfType(events, domain.EvAgentToolUse)
	if len(toolUses) != 1 {
		t.Fatalf("agent.tool_use count = %d, want exactly 1; got %s", len(toolUses), typeList(events))
	}
	toolUse := toolUses[0]
	toolName, ok := toolUse.Payload["name"].(string)
	if !ok || toolName == "" {
		t.Fatalf("agent.tool_use has invalid name payload: %#v", toolUse.Payload)
	}
	if toolName != tc.expectedTool {
		t.Fatalf("agent.tool_use name = %q, want %q", toolName, tc.expectedTool)
	}
	toolResults := eventsOfType(events, domain.EvAgentToolResult)
	if len(toolResults) != 1 {
		t.Fatalf("agent.tool_result count = %d, want exactly 1; got %s", len(toolResults), typeList(events))
	}
	toolResult := toolResults[0]
	toolUseID, ok := toolResult.Payload["tool_use_id"].(string)
	if !ok || toolUseID != toolUse.ID {
		t.Fatalf("agent.tool_result tool_use_id = %q, want %q", toolUseID, toolUse.ID)
	}
	text, isError, ok := eventText(toolResult)
	if !ok {
		t.Fatalf("agent.tool_result has invalid content payload: %#v", toolResult.Payload)
	}
	if isError {
		t.Fatalf("tool_result is_error=true; content=%q", text)
	}
	if strings.TrimSpace(text) != tc.expectedToolOutput {
		t.Fatalf("tool output = %q, want %q", text, tc.expectedToolOutput)
	}
	finalMessage, ok := firstEventOfTypeAfter(events, toolResult.ID, domain.EvAgentMessage)
	if !ok {
		t.Fatalf("agent.message missing after tool result; got %s", typeList(events))
	}
	finalText, _, ok := eventText(finalMessage)
	if !ok || strings.TrimSpace(finalText) == "" {
		t.Fatalf("final agent.message has empty or invalid content: %#v", finalMessage.Payload)
	}

	final, err := store.GetSession(ctx, sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if final.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", final.Status)
	}
	binding, found, err := store.GetSandboxBinding(ctx, sessID)
	if err != nil {
		t.Fatalf("get sandbox binding: %v", err)
	}
	if !found || binding.Ref.Provider != tc.provider.Name() || binding.Ref.ID == "" {
		t.Fatalf("sandbox binding = %+v, found=%v", binding, found)
	}
	if err := store.PrepareSessionDeletion(ctx, sessID); err != nil {
		t.Fatalf("prepare session deletion: %v", err)
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := orch.TerminateSession(releaseCtx, sessID); err != nil {
		t.Fatalf("terminate session and release sandbox: %v", err)
	}
	if _, found, err := store.GetSandboxBinding(ctx, sessID); err != nil || found {
		t.Fatalf("sandbox binding survived cleanup: found=%v err=%v", found, err)
	}
	if err := store.FinalizeSessionDeletion(releaseCtx, sessID); err != nil {
		t.Fatalf("finalize session deletion: %v", err)
	}
}

// toolProbeModel is a deterministic, retry-safe model client for integration
// tests. Before a tool result exists it requests one real bash step; afterwards
// it ends the turn. Behavior depends only on projected history, not a mutable
// call counter, so an Activity retry receives the same response.
type toolProbeModel struct {
	command   string
	finalText string
}

func (m toolProbeModel) CreateMessage(_ context.Context, req model.Request) (model.Response, error) {
	for _, message := range req.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" {
				return model.Response{
					Content:    []domain.ContentBlock{{Type: "text", Text: m.finalText}},
					StopReason: "end_turn",
				}, nil
			}
		}
	}
	return model.Response{
		Content: []domain.ContentBlock{{
			Type: "tool_use", ToolUseID: "probe_tool_1", ToolName: bashToolName,
			Input: map[string]any{"command": m.command},
		}},
		StopReason: "tool_use",
	}, nil
}

func (m toolProbeModel) CreateMessageStream(ctx context.Context, req model.Request, onDelta func(index int, text string)) (model.Response, error) {
	resp, err := m.CreateMessage(ctx, req)
	if err == nil && resp.StopReason == "end_turn" && len(resp.Content) == 1 && onDelta != nil {
		onDelta(0, resp.Content[0].Text)
	}
	return resp, err
}

type interruptBlockingModel struct {
	started  chan struct{}
	canceled chan struct{}

	startOnce  sync.Once
	cancelOnce sync.Once
}

func newInterruptBlockingModel() *interruptBlockingModel {
	return &interruptBlockingModel{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (m *interruptBlockingModel) CreateMessage(
	ctx context.Context,
	req model.Request,
) (model.Response, error) {
	return m.CreateMessageStream(ctx, req, nil)
}

func (m *interruptBlockingModel) CreateMessageStream(
	ctx context.Context,
	_ model.Request,
	_ func(index int, text string),
) (model.Response, error) {
	m.startOnce.Do(func() { close(m.started) })
	<-ctx.Done()
	m.cancelOnce.Do(func() { close(m.canceled) })
	return model.Response{}, ctx.Err()
}

func hasType(events []domain.Event, t string) bool {
	for _, e := range events {
		if e.Type == t {
			return true
		}
	}
	return false
}

func eventsHaveOrder(events []domain.Event, types ...string) bool {
	idx := 0
	for _, event := range events {
		if idx < len(types) && event.Type == types[idx] {
			idx++
		}
	}
	return idx == len(types)
}

func firstFailureEvent(events []domain.Event) (domain.Event, bool) {
	for _, event := range events {
		if event.Type == domain.EvSessionError || event.Type == domain.EvSessionStatusTerminated {
			return event, true
		}
	}
	return domain.Event{}, false
}

func eventsOfType(events []domain.Event, eventType string) []domain.Event {
	var matches []domain.Event
	for _, event := range events {
		if event.Type == eventType {
			matches = append(matches, event)
		}
	}
	return matches
}

func typeList(events []domain.Event) string {
	s := ""
	for _, e := range events {
		s += e.Type + " "
	}
	return s
}

func firstEventOfTypeAfter(events []domain.Event, afterID, eventType string) (domain.Event, bool) {
	after := false
	for _, event := range events {
		if after && event.Type == eventType {
			return event, true
		}
		if event.ID == afterID {
			after = true
		}
	}
	return domain.Event{}, false
}

func eventText(event domain.Event) (text string, isError bool, ok bool) {
	isError, _ = event.Payload["is_error"].(bool)
	content, ok := event.Payload["content"].([]any)
	if !ok {
		return "", isError, false
	}
	var out strings.Builder
	for _, raw := range content {
		block, blockOK := raw.(map[string]any)
		part, textOK := block["text"].(string)
		if !blockOK || !textOK {
			return "", isError, false
		}
		out.WriteString(part)
	}
	return out.String(), isError, true
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
	if !eventsHaveOrder(events, types...) {
		t.Fatalf("events not in expected order %v; got %s", types, typeList(events))
	}
}
