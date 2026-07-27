package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

// fileToolRuntime is a test AgentRuntime that exercises the request sandbox
// directly. It interprets the trigger's user text as a tiny script:
//
//	"write <path> <data>" writes data to path in the sandbox, then
//	"read <path>"         reads path and emits its contents as an agent.message.
//
// It lets a test prove that filesystem state a tool creates in one run is (or is
// not) visible to a later run through the sandbox the app hands the runtime.
type fileToolRuntime struct{}

func (fileToolRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	text := triggerText(req.Trigger)
	fields := strings.Fields(text)
	reply := ""
	if len(fields) >= 2 && req.Sandbox != nil {
		switch fields[0] {
		case "write":
			data := ""
			if len(fields) >= 3 {
				data = strings.Join(fields[2:], " ")
			}
			if err := req.Sandbox.WriteFile(ctx, fields[1], []byte(data)); err != nil {
				reply = "write-error: " + err.Error()
			} else {
				reply = "wrote " + fields[1]
			}
		case "read":
			data, err := req.Sandbox.ReadFile(ctx, fields[1])
			if err != nil {
				reply = "read-error: " + err.Error()
			} else {
				reply = "read: " + string(data)
			}
		}
	}
	_, err := sink.Emit(ctx, []domain.EventDraft{{
		Type:    domain.EvAgentMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}},
	}})
	return agentruntime.RunOutcome{}, err
}

func triggerText(ev domain.Event) string {
	if text, ok := ev.Payload["text"].(string); ok {
		return text
	}
	blocks, _ := ev.Payload["content"].([]any)
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			if t, _ := m["text"].(string); t != "" {
				return t
			}
		}
	}
	return ""
}

// toolAgent creates an agent whose toolset is non-empty so the session provisions
// a sandbox, plus a cloud environment. Returns their ids.
func toolAgentAndEnv(t *testing.T, as *AgentService, envs *EnvironmentService) (string, string) {
	t.Helper()
	ctx := context.Background()
	ag, err := as.Create(ctx, domain.Agent{
		Name:  "tool-agent",
		Model: domain.Model{ID: "claude-opus-4-8"},
		Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	return ag.ID, env.ID
}

// lastAgentText returns the text of the most recent agent.message in history.
func lastAgentText(t *testing.T, ss *SessionService, sessionID string) string {
	t.Helper()
	hist, err := ss.events.History(context.Background(), sessionID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, e := range hist {
		if e.Type == domain.EvAgentMessage {
			text = contentBlockText(e.Payload["content"])
		}
	}
	return text
}

func newFileToolService(t *testing.T) (*SessionService, *AgentService, *EnvironmentService, *sandbox.SessionManager) {
	t.Helper()
	db, _ := store.OpenMemory()
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	es := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	as := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	ss := NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		es, store.NewRunStore(db, ids, clk), fileToolRuntime{}, sandbox.NewLocalProvider(), ids, clk)
	return ss, as, envs, ss.sandbox
}

// TestSessionService_SandboxPersistsAcrossRuns proves the core session-scoped
// property: a file written by the first run is readable by a later run in the
// same session.
func TestSessionService_SandboxPersistsAcrossRuns(t *testing.T) {
	ss, as, envs, _ := newFileToolService(t)
	ctx := context.Background()
	agID, envID := toolAgentAndEnv(t, as, envs)

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	// Release the session's sandbox so its temp dir does not leak; Release is
	// idempotent, so a later Delete in the test is still fine.
	t.Cleanup(func() { _ = ss.sandbox.Release(context.Background(), sess.ID) })

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "write state.txt hello-across-runs"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "read state.txt"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	if got := lastAgentText(t, ss, sess.ID); got != "read: hello-across-runs" {
		t.Fatalf("second run read %q, want the file written by the first run", got)
	}
}

// TestSessionService_SandboxIsolatedBetweenSessions proves a file written in one
// session's sandbox is not visible from a different session, even when both use
// the same agent and environment.
func TestSessionService_SandboxIsolatedBetweenSessions(t *testing.T) {
	ss, as, envs, _ := newFileToolService(t)
	ctx := context.Background()
	agID, envID := toolAgentAndEnv(t, as, envs)

	writer, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.sandbox.Release(context.Background(), writer.ID) })
	if _, err := ss.SendEvent(ctx, writer.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "write secret.txt only-in-writer"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, writer.ID, domain.StatusIdle)

	other, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.sandbox.Release(context.Background(), other.ID) })
	if _, err := ss.SendEvent(ctx, other.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "read secret.txt"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, other.ID, domain.StatusIdle)

	got := lastAgentText(t, ss, other.ID)
	if !strings.HasPrefix(got, "read-error:") {
		t.Fatalf("different session saw %q, want a read error (isolated sandbox)", got)
	}
}

// TestSessionService_SandboxProvisionedOncePerSession proves repeated runs in one
// session reuse a single logical sandbox instead of provisioning a new one each
// run.
func TestSessionService_SandboxProvisionedOncePerSession(t *testing.T) {
	db, _ := store.OpenMemory()
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	es := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	as := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	counting := &provisionCountingProvider{inner: sandbox.NewLocalProvider()}
	ss := NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		es, store.NewRunStore(db, ids, clk), fileToolRuntime{}, counting, ids, clk)

	ctx := context.Background()
	agID, envID := toolAgentAndEnv(t, as, envs)
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.sandbox.Release(context.Background(), sess.ID) })

	for i := 0; i < 3; i++ {
		if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{
			Type: domain.EvUserMessage, Payload: map[string]any{"text": "write f.txt data"},
		}}); err != nil {
			t.Fatal(err)
		}
		pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)
	}

	if got := counting.count(); got != 1 {
		t.Fatalf("provisioned %d sandboxes across 3 runs, want 1", got)
	}
}

// TestSessionService_IdleDoesNotDestroySandbox proves that reaching idle after a
// run leaves the session's sandbox intact (still holding its files), rather than
// destroying it.
func TestSessionService_IdleDoesNotDestroySandbox(t *testing.T) {
	ss, as, envs, mgr := newFileToolService(t)
	ctx := context.Background()
	agID, envID := toolAgentAndEnv(t, as, envs)
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Release(context.Background(), sess.ID) })

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "write keep.txt still-here"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// After idle the sandbox must still exist and hold the file: acquiring it
	// again returns the same live instance rather than a fresh empty one.
	box, err := mgr.Acquire(ctx, sess.ID, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := box.ReadFile(ctx, "keep.txt")
	if err != nil || string(data) != "still-here" {
		t.Fatalf("sandbox lost its file after idle: data=%q err=%v", data, err)
	}
}

// TestSessionService_DeleteReleasesSandboxExactlyOnce proves deleting a session
// tears its sandbox down exactly once.
func TestSessionService_DeleteReleasesSandboxExactlyOnce(t *testing.T) {
	db, _ := store.OpenMemory()
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	es := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	as := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	counting := &destroyCountingProvider{inner: sandbox.NewLocalProvider()}
	ss := NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		es, store.NewRunStore(db, ids, clk), fileToolRuntime{}, counting, ids, clk)

	ctx := context.Background()
	agID, envID := toolAgentAndEnv(t, as, envs)
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage, Payload: map[string]any{"text": "write f.txt data"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	if err := ss.Delete(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := counting.destroyCount(); got != 1 {
		t.Fatalf("session delete destroyed the sandbox %d times, want 1", got)
	}
}
