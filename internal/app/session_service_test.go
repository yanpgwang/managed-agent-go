package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func newSessionService(t *testing.T) (*SessionService, *AgentService, *EnvironmentService) {
	t.Helper()
	db, _ := store.OpenMemory()
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	es := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	as := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	ss := NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		es, store.NewRunStore(db, ids, clk), agentruntime.NewFake(), sandbox.NewLocalProvider(), ids, clk)
	return ss, as, envs
}

type failingRuntime struct{}

func (failingRuntime) Run(
	context.Context,
	agentruntime.RunRequest,
	agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	return agentruntime.RunOutcome{}, fmt.Errorf("runtime exploded")
}

type recordingRuntime struct {
	requests chan agentruntime.RunRequest
}

func (r recordingRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	r.requests <- req
	_, err := sink.Emit(ctx, []domain.EventDraft{{
		Type: domain.EvAgentMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "ok"}},
		},
	}})
	return agentruntime.RunOutcome{}, err
}

type blockingSessionIDGen struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *blockingSessionIDGen) NewID(prefix string) string {
	if prefix != domain.PrefixSession {
		return prefix + "dependency_race"
	}
	g.once.Do(func() { close(g.reached) })
	<-g.release
	return "sesn_dependency_race"
}

func TestSessionService_CreateCannotRaceEnvironmentDeleteIntoOrphan(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	clock := domain.FixedClock{T: now}
	agentRepo := store.NewAgentRepo(db)
	environmentRepo := store.NewEnvironmentRepo(db)
	sessionRepo := store.NewSessionRepo(db)
	if err := agentRepo.PutVersion(ctx, domain.Agent{
		ID: "agent_dependency_race", Version: 1, Name: "agent",
		Model: domain.Model{ID: "claude-opus-4-8"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := environmentRepo.Put(ctx, domain.Environment{
		ID: "env_dependency_race", Name: "environment", ConfigType: "cloud",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	blockingIDs := &blockingSessionIDGen{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	eventIDs := domain.NewSeqIDGen()
	events := NewEventService(store.NewEventStore(db, eventIDs, clock), NewHub(8))
	sessions := NewSessionService(
		sessionRepo, agentRepo, environmentRepo, events,
		store.NewRunStore(db, blockingIDs, clock), agentruntime.NewFake(), sandbox.NewLocalProvider(), blockingIDs, clock,
	)
	environments := NewEnvironmentService(environmentRepo, eventIDs, clock)

	createDone := make(chan error, 1)
	go func() {
		_, createErr := sessions.Create(ctx, CreateSessionInput{
			AgentID: "agent_dependency_race", EnvironmentID: "env_dependency_race",
		})
		createDone <- createErr
	}()

	// NewID is reached only after Create has read and validated both
	// dependencies. Delete in this window reproduced the old check-then-insert
	// orphan deterministically.
	<-blockingIDs.reached
	deleteErr := environments.Delete(ctx, "env_dependency_race")
	close(blockingIDs.release)
	if deleteErr != nil {
		t.Fatalf("delete while create paused: %v", deleteErr)
	}
	if createErr := <-createDone; createErr == nil {
		t.Fatal("create succeeded after its environment was deleted")
	}
	if _, err := sessionRepo.Get(ctx, "sesn_dependency_race"); err == nil {
		t.Fatal("orphan session was persisted")
	}
	if _, err := environmentRepo.Get(ctx, "env_dependency_race"); err == nil {
		t.Fatal("environment should have been deleted")
	}
}

func TestSessionService_CreateRequiresEnvAndAgent(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	// no initial events -> idle
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil || sess.Status != domain.StatusIdle {
		t.Fatalf("create idle: %+v err=%v", sess, err)
	}
	// missing env -> error
	if _, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: "nope"}); err == nil {
		t.Fatal("expected error for missing environment")
	}
}

func TestSessionService_SelfHostedUnsupported(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "self_hosted"})
	_, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	de, ok := err.(*domain.DomainError)
	if !ok || de.Kind != domain.KindUnsupported {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

// pollUntilStatus polls ss.Get(id) every 10ms for up to 2s, returning
// when the session reaches wantStatus or the deadline expires.
func pollUntilStatus(t *testing.T, ss *SessionService, id string, wantStatus domain.Status) domain.Session {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := ss.Get(context.Background(), id)
		if err == nil && s.Status == wantStatus {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session %s to reach status %s", id, wantStatus)
	return domain.Session{}
}

// hasEventType returns true if the event history for sessionID contains an event of evType.
func hasEventType(t *testing.T, ss *SessionService, sessionID, evType string) bool {
	t.Helper()
	hist, err := ss.events.History(context.Background(), sessionID, 0, 100000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, e := range hist {
		if e.Type == evType {
			return true
		}
	}
	return false
}

// TestSessionService_InitialEventsRunToIdle verifies that a session created
// with initial events drives the fake runtime to agent.message + status_idle.
func TestSessionService_InitialEventsRunToIdle(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID:       ag.ID,
		EnvironmentID: env.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"text": "hello"}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Returned status should be running.
	if sess.Status != domain.StatusRunning {
		t.Fatalf("expected running, got %s", sess.Status)
	}

	// Poll until fake drives to idle.
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	if !hasEventType(t, ss, sess.ID, domain.EvAgentMessage) {
		t.Error("expected agent.message in history")
	}
	if !hasEventType(t, ss, sess.ID, domain.EvSessionStatusIdle) {
		t.Error("expected session.status_idle in history")
	}
	if !hasEventType(t, ss, sess.ID, domain.EvSessionStatusRunning) {
		t.Error("expected session.status_running in history")
	}
}

func TestSessionService_InitialEventBatchProcessesEveryEventInOrder(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID:       ag.ID,
		EnvironmentID: env.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("first")}},
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("second")}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	assertBatchProcessedInOrder(t, ss, sess.ID, nil, []string{"echo: first", "echo: second"})
}

// TestSessionService_SendEventDrivesRun verifies that SendEvent on an idle
// session triggers the fake runtime and drives the session back to idle.
func TestSessionService_SendEventDrivesRun(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	// Create an idle session.
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Status != domain.StatusIdle {
		t.Fatalf("expected idle, got %s", sess.Status)
	}

	sent, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"text": "hello"}},
	})
	if err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	if len(sent) != 1 || sent[0].ProcessedAt != nil {
		t.Fatalf("sent trigger should initially be queued: %+v", sent)
	}

	// Poll until fake drives back to idle.
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	if !hasEventType(t, ss, sess.ID, domain.EvAgentMessage) {
		t.Error("expected agent.message in history")
	}
	if !hasEventType(t, ss, sess.ID, domain.EvSessionStatusRunning) {
		t.Error("expected session.status_running in history")
	}
	history, err := ss.events.History(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var processed bool
	for _, event := range history {
		if event.ID == sent[0].ID {
			processed = event.ProcessedAt != nil
		}
	}
	if !processed {
		t.Fatal("runtime-completed trigger still has nil processed_at")
	}
}

func TestSessionService_RuntimeReceivesImmutableAgentSnapshot(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(16))
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	runtimeRequests := make(chan agentruntime.RunRequest, 1)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, store.NewRunStore(db, ids, clk),
		recordingRuntime{requests: runtimeRequests}, sandbox.NewLocalProvider(), ids, clk,
	)

	system := "saved system prompt"
	agent, err := agents.Create(ctx, domain.Agent{
		Name: "snapshot-agent",
		Model: domain.Model{
			ID: "claude-opus-4-8",
		},
		System: &system,
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := environments.Create(ctx, domain.Environment{
		Name: "snapshot-env", ConfigType: "cloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID: agent.ID, EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	select {
	case req := <-runtimeRequests:
		if req.SessionID != session.ID {
			t.Fatalf("runtime session = %q, want %q", req.SessionID, session.ID)
		}
		if req.AgentSnapshot.Model.ID != agent.Model.ID {
			t.Fatalf("runtime model = %q, want %q",
				req.AgentSnapshot.Model.ID, agent.Model.ID)
		}
		if req.AgentSnapshot.System == nil || *req.AgentSnapshot.System != system {
			t.Fatalf("runtime system = %#v, want %q", req.AgentSnapshot.System, system)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not receive the admitted turn")
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)
}

func TestSessionService_UpdateTitleEmitsOnlyOnChange(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: ag.ID, EnvironmentID: env.ID, Title: "before",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	unchanged, err := ss.UpdateTitle(ctx, sess.ID, "before")
	if err != nil {
		t.Fatalf("no-op UpdateTitle: %v", err)
	}
	if !unchanged.UpdatedAt.Equal(sess.UpdatedAt) {
		t.Fatalf("no-op changed updated_at: got %s want %s", unchanged.UpdatedAt, sess.UpdatedAt)
	}
	if hasEventType(t, ss, sess.ID, domain.EvSessionUpdated) {
		t.Fatal("no-op update emitted session.updated")
	}

	updated, err := ss.UpdateTitle(ctx, sess.ID, "after")
	if err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}
	if updated.Title != "after" {
		t.Fatalf("updated title = %q", updated.Title)
	}
	history, err := ss.events.History(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var update *domain.Event
	for i := range history {
		if history[i].Type == domain.EvSessionUpdated {
			update = &history[i]
			break
		}
	}
	if update == nil || update.Payload["title"] != "after" || update.ProcessedAt == nil {
		t.Fatalf("session.updated = %#v", update)
	}
}

func TestSessionService_RuntimeFailureClosesDurableRun(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	hub := NewHub(64)
	events := NewEventService(store.NewEventStore(db, ids, clk), hub)
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, failingRuntime{}, sandbox.NewLocalProvider(), ids, clk,
	)
	agent, _ := agents.Create(ctx, domain.Agent{
		Name: "a", Model: domain.Model{ID: "claude-opus-4-8"},
	})
	environment, _ := environments.Create(ctx, domain.Environment{
		Name: "e", ConfigType: "cloud",
	})
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID: agent.ID, EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "fail"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// An unrecoverable runtime failure terminates the session. We have no retry
	// mechanism, so calling it "rescheduling" would be a lie; the honest public
	// projection is `terminated`.
	pollUntilStatus(t, sessions, session.ID, domain.StatusTerminated)

	var failed int
	if err := db.QueryRow(`
SELECT count(*) FROM session_runs WHERE session_id=? AND state=?`,
		session.ID, string(domain.RunFailed)).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Fatalf("failed run count = %d, want 1", failed)
	}
	history, err := events.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawError, sawTerminated, triggerProcessed bool
	for _, event := range history {
		if event.Type == domain.EvSessionError {
			sawError = true
		}
		if event.Type == domain.EvSessionStatusTerminated {
			sawTerminated = true
		}
		// The runtime error must never land on the wire as a status_idle with a
		// fabricated stop_reason. The documented stop_reason.type union is only
		// end_turn | requires_action.
		if event.Type == domain.EvSessionStatusIdle {
			t.Fatalf("runtime failure emitted session.status_idle: %#v", event.Payload)
		}
		if event.ID == sent[0].ID && event.ProcessedAt != nil {
			triggerProcessed = true
		}
	}
	if !sawError || !sawTerminated || !triggerProcessed {
		t.Fatalf("failure history: saw_error=%v saw_terminated=%v trigger_processed=%v",
			sawError, sawTerminated, triggerProcessed)
	}
}

func TestSessionService_RuntimeWithoutTerminalOutputGetsIdleEvent(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	agent, _ := as.Create(ctx, domain.Agent{
		Name: "a", Model: domain.Model{ID: "claude-opus-4-8"},
	})
	environment, _ := envs.Create(ctx, domain.Environment{
		Name: "e", ConfigType: "cloud",
	})
	session, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agent.ID, EnvironmentID: environment.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ss.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserDefineOutcome,
		Payload: map[string]any{
			"description": "quality",
			"rubric":      map[string]any{"type": "text", "content": "good"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, ss, session.ID, domain.StatusIdle)
	if !hasEventType(t, ss, session.ID, domain.EvSessionStatusIdle) {
		t.Fatal("run without terminal output did not emit session.status_idle")
	}
}

func TestSessionService_SendEventBatchProcessesEveryEventInOrder(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sent, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("first")}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("second")}},
	})
	if err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	assertBatchProcessedInOrder(t, ss, sess.ID, sent, []string{"echo: first", "echo: second"})
}

func textBlocks(text string) []any {
	return []any{map[string]any{"type": "text", "text": text}}
}

// projectingRuntime records the projected Messages it receives for each run and
// emits a distinct agent.message per trigger so a later run's projection can be
// asserted to include an earlier run's committed output.
type projectingRuntime struct {
	projections chan []domain.Message
}

func (r projectingRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	r.projections <- req.Messages
	_, err := sink.Emit(ctx, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{
			"content": []any{map[string]any{
				"type": "text",
				"text": "reply-to: " + contentText(req.Trigger.Payload),
			}},
		}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{
			"stop_reason": map[string]any{"type": "end_turn"},
		}},
	})
	return agentruntime.RunOutcome{}, err
}

func contentText(payload map[string]any) string {
	blocks, _ := payload["content"].([]any)
	if len(blocks) == 0 {
		return ""
	}
	block, _ := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

// TestSessionService_SecondUserEventObservesFirstAgentOutput proves the
// completion-before-next-claim guarantee end to end with EXACT projections: two
// user events admitted in one batch produce two runs. Run A's projected Messages
// are exactly user(A). Run B's are exactly user(A), assistant(reply-to:A),
// user(B) — the second run observes the first run's committed reply, in causal
// order, and nothing more. The public event history preserves receipt/commit
// order and is not rewritten.
func TestSessionService_SecondUserEventObservesFirstAgentOutput(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	projections := make(chan []domain.Message, 4)
	ss := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, store.NewRunStore(db, ids, clk),
		projectingRuntime{projections: projections}, sandbox.NewLocalProvider(), ids, clk,
	)
	ag, _ := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID:       ag.ID,
		EnvironmentID: env.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("first")}},
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("second")}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	first := receiveProjection(t, projections)
	second := receiveProjection(t, projections)
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// Run A's projection is exactly the first user message.
	assertMessages(t, "run A", first, []wantMessage{
		{domain.RoleUser, "first"},
	})
	// Run B's projection is exactly user(A), assistant(reply-to:A), user(B).
	assertMessages(t, "run B", second, []wantMessage{
		{domain.RoleUser, "first"},
		{domain.RoleAssistant, "reply-to: first"},
		{domain.RoleUser, "second"},
	})

	// The public event history preserves receipt/commit order and is not
	// rewritten: both user triggers are committed (with ascending seq) before the
	// agent replies they caused.
	assertUserTriggersBeforeOutputs(t, ss, sess.ID, []string{"first", "second"})
}

// TestSessionService_BatchedTriplePerRunCausalProjection admits A,B,C in one
// batch and asserts each run's projection contains ONLY the completed prior
// trigger/output turns plus its current trigger, in exact role/content order —
// never a later, still-queued trigger.
func TestSessionService_BatchedTriplePerRunCausalProjection(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	projections := make(chan []domain.Message, 8)
	ss := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, store.NewRunStore(db, ids, clk),
		projectingRuntime{projections: projections}, sandbox.NewLocalProvider(), ids, clk,
	)
	ag, _ := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID:       ag.ID,
		EnvironmentID: env.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("A")}},
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("B")}},
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("C")}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	runA := receiveProjection(t, projections)
	runB := receiveProjection(t, projections)
	runC := receiveProjection(t, projections)
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	assertMessages(t, "run A", runA, []wantMessage{
		{domain.RoleUser, "A"},
	})
	assertMessages(t, "run B", runB, []wantMessage{
		{domain.RoleUser, "A"},
		{domain.RoleAssistant, "reply-to: A"},
		{domain.RoleUser, "B"},
	})
	assertMessages(t, "run C", runC, []wantMessage{
		{domain.RoleUser, "A"},
		{domain.RoleAssistant, "reply-to: A"},
		{domain.RoleUser, "B"},
		{domain.RoleAssistant, "reply-to: B"},
		{domain.RoleUser, "C"},
	})

	// Public history keeps receipt/commit order: A,B,C triggers are all committed
	// before any of the agent replies they produced.
	assertUserTriggersBeforeOutputs(t, ss, sess.ID, []string{"A", "B", "C"})
}

type wantMessage struct {
	role domain.Role
	text string
}

// assertMessages requires msgs to equal want exactly: same length, same role
// and single-text-block content per message, in order.
func assertMessages(t *testing.T, label string, msgs []domain.Message, want []wantMessage) {
	t.Helper()
	if len(msgs) != len(want) {
		t.Fatalf("%s: projection has %d messages, want %d: %#v", label, len(msgs), len(want), msgs)
	}
	for i, w := range want {
		m := msgs[i]
		if m.Role != w.role {
			t.Fatalf("%s: message[%d] role = %s, want %s: %#v", label, i, m.Role, w.role, msgs)
		}
		if len(m.Content) != 1 || m.Content[0].Text != w.text {
			t.Fatalf("%s: message[%d] content = %#v, want single text %q", label, i, m.Content, w.text)
		}
	}
}

// assertUserTriggersBeforeOutputs proves the public event history is authentic
// receipt/commit order: the user.message triggers (in wantUserText order, with
// strictly ascending sequence) all precede the agent.message replies. Nothing
// rewrites event seq to match causal projection.
func assertUserTriggersBeforeOutputs(t *testing.T, ss *SessionService, sessionID string, wantUserText []string) {
	t.Helper()
	history, err := ss.events.History(context.Background(), sessionID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var userText []string
	var lastSeq int64
	var firstAgentSeq int64 = -1
	var lastUserTriggerSeq int64
	for _, e := range history {
		if e.Sequence <= lastSeq {
			t.Fatalf("event history not in ascending seq order: %d after %d", e.Sequence, lastSeq)
		}
		lastSeq = e.Sequence
		switch e.Type {
		case domain.EvUserMessage:
			userText = append(userText, contentBlockText(e.Payload["content"]))
			lastUserTriggerSeq = e.Sequence
		case domain.EvAgentMessage:
			if firstAgentSeq < 0 {
				firstAgentSeq = e.Sequence
			}
		}
	}
	if len(userText) != len(wantUserText) {
		t.Fatalf("user triggers = %q, want %q", userText, wantUserText)
	}
	for i := range wantUserText {
		if userText[i] != wantUserText[i] {
			t.Fatalf("user trigger order = %q, want %q", userText, wantUserText)
		}
	}
	if firstAgentSeq >= 0 && lastUserTriggerSeq >= firstAgentSeq {
		t.Fatalf("public history reordered: a user trigger (seq %d) landed after an agent reply (seq %d)",
			lastUserTriggerSeq, firstAgentSeq)
	}
}

func receiveProjection(t *testing.T, ch chan []domain.Message) []domain.Message {
	t.Helper()
	select {
	case msgs := <-ch:
		return msgs
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not receive a projected turn")
		return nil
	}
}

// blockingDestroySandbox is a sandbox whose Destroy blocks until the test
// releases it, and which records the state of the context it was destroyed
// under. It lets a test hold a teardown mid-flight, cancel the original request
// context, and then assert on the context the teardown actually ran with.
type blockingDestroySandbox struct {
	started chan struct{} // closed when Destroy begins
	release chan struct{} // closed by the test to let Destroy finish

	// Captured inside Destroy, after the test has cancelled the original request
	// context. Read only after Destroy has returned (via the delete result
	// channel), so no synchronization beyond that happens-before is needed.
	errAfterReqCancel error
	hasDeadline       bool
}

func (b *blockingDestroySandbox) Exec(context.Context, sandbox.Command) (*sandbox.Result, error) {
	return &sandbox.Result{}, nil
}
func (b *blockingDestroySandbox) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (b *blockingDestroySandbox) WriteFile(context.Context, string, []byte) error  { return nil }
func (b *blockingDestroySandbox) Root() string                                     { return "" }

func (b *blockingDestroySandbox) Destroy(ctx context.Context) error {
	close(b.started)
	<-b.release
	// The test cancels the original request context before closing release, so
	// this read observes the teardown context after that cancellation. Because
	// Delete detaches teardown from the request context, Err must still be nil.
	b.errAfterReqCancel = ctx.Err()
	_, b.hasDeadline = ctx.Deadline()
	return nil
}

// stubSandboxProvider provisions a single pre-built sandbox, so a test can
// inject a controllable box into a SessionService's SessionManager.
type stubSandboxProvider struct{ box sandbox.Sandbox }

func (p stubSandboxProvider) Provision(context.Context, sandbox.Spec) (sandbox.Sandbox, error) {
	return p.box, nil
}

// TestSessionService_DeleteTeardownSurvivesRequestCancellation proves that once
// sandbox teardown has started during Delete, cancelling the original request
// context does not cancel that teardown, while the teardown context is still
// bounded by a deadline. Coordination is by channels only — no timing sleeps.
func TestSessionService_DeleteTeardownSurvivesRequestCancellation(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)

	box := &blockingDestroySandbox{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ss := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, store.NewRunStore(db, ids, clk),
		agentruntime.NewFake(), stubSandboxProvider{box: box}, ids, clk,
	)
	ag, _ := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	// An idle session (no initial events) that Delete is allowed to remove.
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Provision the session's sandbox so Delete's Release has a box to destroy.
	if _, err := ss.sandbox.Acquire(ctx, sess.ID, sandbox.Spec{}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- ss.Delete(reqCtx, sess.ID) }()

	// Wait for teardown to start under a timeout so a regression (Delete never
	// reaching Release, or Release never invoking Destroy) fails the test instead
	// of hanging it forever.
	select {
	case <-box.started: // teardown is now in flight
	case <-time.After(2 * time.Second):
		cancelReq()
		t.Fatal("sandbox teardown never started during Delete")
	}
	cancelReq()        // cancel the ORIGINAL request context mid-teardown
	close(box.release) // let Destroy observe its context and return

	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Delete did not return after teardown was released")
	}
	if box.errAfterReqCancel != nil {
		t.Fatalf("teardown context was cancelled by request cancellation: %v", box.errAfterReqCancel)
	}
	if !box.hasDeadline {
		t.Fatal("teardown context was not bounded by a deadline")
	}
}

func assertBatchProcessedInOrder(
	t *testing.T,
	ss *SessionService,
	sessionID string,
	sent []domain.Event,
	wantAgentText []string,
) {
	t.Helper()
	history, err := ss.events.History(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	if sent == nil {
		for _, event := range history {
			if event.Type == domain.EvUserMessage {
				sent = append(sent, event)
			}
		}
	}
	if len(sent) != len(wantAgentText) {
		t.Fatalf("client event count=%d want=%d", len(sent), len(wantAgentText))
	}
	for _, event := range sent {
		var found *domain.Event
		for i := range history {
			if history[i].ID == event.ID {
				found = &history[i]
				break
			}
		}
		if found == nil || found.ProcessedAt == nil {
			t.Fatalf("client event %s was not processed: %+v", event.ID, found)
		}
	}

	var gotAgentText []string
	for _, event := range history {
		if event.Type != domain.EvAgentMessage {
			continue
		}
		gotAgentText = append(gotAgentText, contentBlockText(event.Payload["content"]))
		if event.ProcessedAt == nil {
			t.Fatalf("server event %s has nil processed_at", event.ID)
		}
	}
	if len(gotAgentText) != len(wantAgentText) {
		t.Fatalf("agent messages=%q want=%q", gotAgentText, wantAgentText)
	}
	for i := range wantAgentText {
		if gotAgentText[i] != wantAgentText[i] {
			t.Fatalf("agent message order=%q want=%q", gotAgentText, wantAgentText)
		}
	}
}

func contentBlockText(value any) string {
	blocks, _ := value.([]any)
	if len(blocks) == 0 {
		return ""
	}
	block, _ := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

// TestSessionService_ArchiveWhileRunningConflict puts a session row directly
// into StatusRunning via the repo and asserts Archive returns KindConflict.
func TestSessionService_ArchiveWhileRunningConflict(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force the session to running via the repo directly (deterministic, no goroutine timing).
	sess.Status = domain.StatusRunning
	if err := ss.sess.Put(ctx, sess); err != nil {
		t.Fatalf("force running: %v", err)
	}

	_, archErr := ss.Archive(ctx, sess.ID)
	de, ok := archErr.(*domain.DomainError)
	if !ok || de.Kind != domain.KindConflict {
		t.Fatalf("expected KindConflict, got %v", archErr)
	}
}

// TestSessionService_DeleteWhileRunningConflict puts a session row directly
// into StatusRunning via the repo and asserts Delete returns KindConflict.
func TestSessionService_DeleteWhileRunningConflict(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force the session to running via the repo directly (deterministic, no goroutine timing).
	sess.Status = domain.StatusRunning
	if err := ss.sess.Put(ctx, sess); err != nil {
		t.Fatalf("force running: %v", err)
	}

	delErr := ss.Delete(ctx, sess.ID)
	de, ok := delErr.(*domain.DomainError)
	if !ok || de.Kind != domain.KindConflict {
		t.Fatalf("expected KindConflict, got %v", delErr)
	}
}

func TestSessionService_DeletePublishesTerminalEventAndClosesStream(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ch, cancel := ss.events.hub.Subscribe(sess.ID, nil)
	defer cancel()

	if err := ss.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	f, open := <-ch
	if !open {
		t.Fatal("stream closed before session.deleted was delivered")
	}
	deleted := f.Event
	if deleted == nil {
		t.Fatalf("terminal frame carried no event: %#v", f)
	}
	if deleted.Type != domain.EvSessionDeleted || deleted.SessionID != sess.ID {
		t.Fatalf("terminal event=%+v", deleted)
	}
	if deleted.ID == "" || deleted.ProcessedAt == nil {
		t.Fatalf("terminal event lacks persisted-event fields: %+v", deleted)
	}
	if _, open := <-ch; open {
		t.Fatal("stream remained open after session.deleted")
	}
	if _, err := ss.Get(ctx, sess.ID); err == nil {
		t.Fatal("deleted session is still readable")
	}
}

// TestSessionService_SendEventToArchivedRejected verifies that SendEvent on an
// archived session returns KindConflict and does NOT wedge the session into
// running (Delete must still succeed).
func TestSessionService_SendEventToArchivedRejected(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Archive the session.
	if _, err := ss.Archive(ctx, sess.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// SendEvent must be rejected with KindConflict.
	_, sendErr := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"text": "hello"}},
	})
	de, ok := sendErr.(*domain.DomainError)
	if !ok || de.Kind != domain.KindConflict {
		t.Fatalf("expected KindConflict, got %v", sendErr)
	}

	// Session must NOT be wedged into running — Delete must succeed.
	if err := ss.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete after rejected SendEvent failed (session wedged?): %v", err)
	}
}

// TestSessionService_SendEventToTerminatedRejected verifies that SendEvent on a
// terminated session returns KindConflict.
func TestSessionService_SendEventToTerminatedRejected(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: ag.ID, EnvironmentID: env.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force the session into terminated status via the repo directly.
	sess.Status = domain.StatusTerminated
	if err := ss.sess.Put(ctx, sess); err != nil {
		t.Fatalf("force terminated: %v", err)
	}

	_, sendErr := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"text": "hello"}},
	})
	de, ok := sendErr.(*domain.DomainError)
	if !ok || de.Kind != domain.KindConflict {
		t.Fatalf("expected KindConflict, got %v", sendErr)
	}
}

// TestSessionService_ConcurrentTitleAndRunNoClobber is a race-detector
// regression test. It creates a session with initial events (triggering an
// admitRun goroutine) and concurrently calls UpdateTitle in a tight loop.
// After the run settles, the title set by UpdateTitle must be preserved and
// the session must reach idle status (no status corruption).
func TestSessionService_ConcurrentTitleAndRunNoClobber(t *testing.T) {
	ss, as, envs := newSessionService(t)
	ctx := context.Background()
	ag, _ := as.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	env, _ := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})

	const wantTitle = "concurrent-title"

	// Create with initial events: spawns admitRun in background.
	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID:       ag.ID,
		EnvironmentID: env.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"text": "race"}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Hammer UpdateTitle concurrently with the running admitRun goroutine.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ss.UpdateTitle(ctx, sess.ID, wantTitle)
		}()
	}
	wg.Wait()

	// Wait for the fake runtime to drive the session back to idle.
	final := pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// The title written by UpdateTitle must survive (not clobbered by reconcile).
	if final.Title != wantTitle {
		t.Errorf("title clobbered: got %q, want %q", final.Title, wantTitle)
	}
}

func TestSessionService_MissingIDsDoNotGrowLockState(t *testing.T) {
	ss, _, _ := newSessionService(t)
	ctx := context.Background()

	allowed := make(map[*sync.Mutex]struct{}, sessionLockShardCount)
	for i := range ss.lockShards {
		allowed[&ss.lockShards[i]] = struct{}{}
	}
	if len(allowed) != sessionLockShardCount {
		t.Fatalf("lock shard count = %d, want %d", len(allowed), sessionLockShardCount)
	}

	const missingIDs = 10_000
	used := make(map[*sync.Mutex]struct{}, sessionLockShardCount)
	for i := 0; i < missingIDs; i++ {
		id := fmt.Sprintf("sesn_missing_%d", i)
		lock := ss.lockFor(id)
		if _, ok := allowed[lock]; !ok {
			t.Fatalf("missing id %q allocated a lock outside the fixed shard set", id)
		}
		used[lock] = struct{}{}

		if _, err := ss.UpdateTitle(ctx, id, "ignored"); err == nil {
			t.Fatalf("missing id %q unexpectedly updated", id)
		}
	}
	if len(ss.lockShards) != sessionLockShardCount {
		t.Fatalf("lock state grew to %d shards", len(ss.lockShards))
	}
	if len(used) > sessionLockShardCount {
		t.Fatalf("%d missing IDs created %d locks", missingIDs, len(used))
	}
}

func TestSessionService_ShardedLocksSerializeSameID(t *testing.T) {
	ss, _, _ := newSessionService(t)
	const sessionID = "sesn_same"

	first := ss.lockFor(sessionID)
	if second := ss.lockFor(sessionID); second != first {
		t.Fatal("the same session id mapped to different lock shards")
	}

	first.Lock()
	acquired := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		lock := ss.lockFor(sessionID)
		lock.Lock()
		close(acquired)
		<-release
		lock.Unlock()
		close(finished)
	}()

	select {
	case <-acquired:
		first.Unlock()
		close(release)
		<-finished
		t.Fatal("second same-session writer acquired the lock concurrently")
	case <-time.After(25 * time.Millisecond):
		// Expected: the first writer still owns the shard.
	}
	first.Unlock()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("same-session writer did not proceed after unlock")
	}
	close(release)
	<-finished
}

func TestSessionService_MultiTurnProjectsPriorHistory(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	hub := NewHub(64)
	events := NewEventService(store.NewEventStore(db, ids, clk), hub)
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	fakeModel := model.NewFake()
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(fakeModel, ids), sandbox.NewLocalProvider(), ids, clk,
	)
	agent, _ := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "m"}})
	environment, _ := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	session, err := sessions.Create(ctx, CreateSessionInput{AgentID: agent.ID, EnvironmentID: environment.ID})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "first"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "second"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// The second turn's model request must include the first turn: user "first",
	// assistant "echo: first", user "second".
	last := fakeModel.LastRequest()
	if len(last.Messages) != 3 {
		t.Fatalf("second-turn projection has %d messages, want 3: %#v", len(last.Messages), last.Messages)
	}
	if last.Messages[0].Role != domain.RoleUser || last.Messages[0].Content[0].Text != "first" {
		t.Fatalf("messages[0] = %#v, want user 'first'", last.Messages[0])
	}
	if last.Messages[1].Role != domain.RoleAssistant || last.Messages[1].Content[0].Text != "echo: first" {
		t.Fatalf("messages[1] = %#v, want assistant 'echo: first'", last.Messages[1])
	}
	if last.Messages[2].Role != domain.RoleUser || last.Messages[2].Content[0].Text != "second" {
		t.Fatalf("messages[2] = %#v, want user 'second'", last.Messages[2])
	}
}

func TestSessionService_DifferentShardsCanProceedConcurrently(t *testing.T) {
	ss, _, _ := newSessionService(t)
	first := ss.lockFor("sesn_first")

	var other *sync.Mutex
	for i := 0; i < sessionLockShardCount*2; i++ {
		candidate := ss.lockFor(fmt.Sprintf("sesn_other_%d", i))
		if candidate != first {
			other = candidate
			break
		}
	}
	if other == nil {
		t.Fatal("could not find IDs assigned to distinct shards")
	}

	first.Lock()
	acquired := make(chan struct{})
	go func() {
		other.Lock()
		close(acquired)
		other.Unlock()
	}()
	select {
	case <-acquired:
		// A different shard is not blocked by the first session.
	case <-time.After(time.Second):
		first.Unlock()
		t.Fatal("different lock shard was unnecessarily serialized")
	}
	first.Unlock()
}

// TestSessionService_BuiltinToolRunEndToEnd drives a full built-in tool round
// through the real AgentCore + local sandbox: the fake model requests a tool on
// the first turn, the core executes it in a provisioned sandbox, feeds the
// result back, and the second model turn ends the turn. The public event
// history must contain the paired tool_use/tool_result plus a final
// agent.message and session.status_idle.
func TestSessionService_BuiltinToolRunEndToEnd(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(model.NewFake(), ids), sandbox.NewLocalProvider(), ids, clk,
	)

	agent, err := agents.Create(ctx, domain.Agent{
		Name:  "tool-agent",
		Model: domain.Model{ID: "claude-opus-4-8"},
		Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID:       agent.ID,
		EnvironmentID: environment.ID,
		InitialEvents: []domain.EventDraft{{
			Type:    domain.EvUserMessage,
			Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "run a tool"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.sandbox.Release(context.Background(), session.ID) })

	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	for _, evType := range []string{
		domain.EvAgentToolUse,
		domain.EvAgentToolResult,
		domain.EvAgentMessage,
		domain.EvSessionStatusIdle,
	} {
		if !hasEventType(t, sessions, session.ID, evType) {
			t.Errorf("expected %s in event history", evType)
		}
	}

	var runID string
	if err := db.QueryRowContext(ctx, `
SELECT id FROM session_runs WHERE session_id=? ORDER BY rowid LIMIT 1`,
		session.ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	attempts, err := runs.ListAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].State != domain.RunAttemptCompleted {
		t.Fatalf("attempts = %#v, want one completed attempt", attempts)
	}
	steps, err := runs.ListToolSteps(ctx, attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].State != domain.ToolStepCompleted || steps[0].Result == nil {
		t.Fatalf("tool steps = %#v, want one completed step with a durable result", steps)
	}
	history, err := events.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var publicToolUseID string
	for _, event := range history {
		if event.Type == domain.EvAgentToolUse {
			publicToolUseID = event.ID
			break
		}
	}
	if publicToolUseID == "" || steps[0].ToolUseEventID != publicToolUseID {
		t.Fatalf(
			"journal tool_use_event_id = %q, want public event id %q",
			steps[0].ToolUseEventID, publicToolUseID,
		)
	}
}

// TestSessionService_CustomToolParksAndResumes drives the full park/resume loop
// through the real AgentCore + model.Fake: an agent with a custom tool receives
// a user.message; model.Fake calls the (first-offered) custom tool, so the run
// parks — the session goes idle with a session.status_idle carrying
// stop_reason.type == "requires_action" and event_ids naming the emitted
// agent.custom_tool_use. A user.custom_tool_result then resumes a fresh run that
// pairs the result in history and reaches end_turn.
func TestSessionService_CustomToolParksAndResumes(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(model.NewFake(), ids), sandbox.NewLocalProvider(), ids, clk,
	)

	agent, err := agents.Create(ctx, domain.Agent{
		Name:  "sre-agent",
		Model: domain.Model{ID: "claude-opus-4-8"},
		Tools: []any{map[string]any{
			"type": "custom", "name": "get_metrics", "description": "d",
			"input_schema": map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID:       agent.ID,
		EnvironmentID: environment.ID,
		InitialEvents: []domain.EventDraft{{
			Type:    domain.EvUserMessage,
			Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "metrics?"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.sandbox.Release(context.Background(), session.ID) })

	// The run parks: session goes idle awaiting the custom tool result.
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	history, err := events.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var customToolUseID string
	var parkedIdle *domain.Event
	for i := range history {
		switch history[i].Type {
		case domain.EvAgentCustomToolUse:
			customToolUseID = history[i].ID
		case domain.EvSessionStatusIdle:
			parkedIdle = &history[i]
		}
	}
	if customToolUseID == "" {
		t.Fatal("no agent.custom_tool_use in history")
	}
	if parkedIdle == nil {
		t.Fatal("no session.status_idle in history")
	}
	stop, _ := parkedIdle.Payload["stop_reason"].(map[string]any)
	if stop == nil || stop["type"] != "requires_action" {
		t.Fatalf("stop_reason = %#v, want type requires_action", parkedIdle.Payload["stop_reason"])
	}
	eventIDs, _ := stop["event_ids"].([]any)
	if len(eventIDs) != 1 || eventIDs[0] != customToolUseID {
		t.Fatalf("stop_reason.event_ids = %#v, want [%s]", stop["event_ids"], customToolUseID)
	}

	// Resume: return the custom tool result referencing the parked use id.
	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": customToolUseID,
			"content":            []any{map[string]any{"type": "text", "text": "cpu 99%"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// The resumed run must reach a normal end_turn.
	resumed, err := events.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawEndTurn, sawAgentMessage bool
	for _, e := range resumed {
		if e.Type == domain.EvAgentMessage {
			sawAgentMessage = true
		}
		if e.Type == domain.EvSessionStatusIdle {
			if stop, _ := e.Payload["stop_reason"].(map[string]any); stop != nil && stop["type"] == "end_turn" {
				sawEndTurn = true
			}
		}
	}
	if !sawAgentMessage {
		t.Error("resumed run produced no agent.message")
	}
	if !sawEndTurn {
		t.Error("resumed run did not reach end_turn")
	}
}

// TestSessionService_PendingGateReleasesPriorQueuedWork drives the full pending
// gate through the real AgentCore + model.Fake. A batch admits two user
// messages: the first parks on the custom tool, the second is ordinary work
// queued BEFORE the park. While the pending action is unresolved the second run
// must stay gated (no second agent.custom_tool_use appears and the session stays
// idle). A matching user.custom_tool_result resumes and clears the gate; only
// then does the previously queued ordinary run execute to end_turn.
func TestSessionService_PendingGateReleasesPriorQueuedWork(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(model.NewFake(), ids), sandbox.NewLocalProvider(), ids, clk,
	)

	agent, err := agents.Create(ctx, domain.Agent{
		Name:  "sre-agent",
		Model: domain.Model{ID: "claude-opus-4-8"},
		Tools: []any{map[string]any{
			"type": "custom", "name": "get_metrics", "description": "d",
			"input_schema": map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	// Two messages in one admission: run 1 parks, run 2 is prior-queued ordinary work.
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID:       agent.ID,
		EnvironmentID: environment.ID,
		InitialEvents: []domain.EventDraft{
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "metrics?"}}}},
			{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "ordinary-queued"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.sandbox.Release(context.Background(), session.ID) })

	// Run 1 parks; the session goes idle awaiting the custom tool result.
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// The gate holds: exactly one agent.custom_tool_use so far (run 2 did not run
	// and park a second time). Give the drain loop a moment to prove it stays put.
	time.Sleep(100 * time.Millisecond)
	history, err := events.History(ctx, session.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var customToolUseID string
	var toolUseCount int
	for i := range history {
		if history[i].Type == domain.EvAgentCustomToolUse {
			toolUseCount++
			customToolUseID = history[i].ID
		}
	}
	if toolUseCount != 1 {
		t.Fatalf("agent.custom_tool_use count while gated = %d, want 1 (prior-queued run must not run)", toolUseCount)
	}

	// Resolve the pending action; the resume run reaches end_turn and clears the
	// gate. Only then is the previously queued ordinary run claimed. With the
	// deterministic fake it re-requests the same custom tool (tools are offered
	// and its own history has no tool_result yet), so releasing the gate is proven
	// by a SECOND agent.custom_tool_use appearing.
	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": customToolUseID,
			"content":            []any{map[string]any{"type": "text", "text": "cpu 99%"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// Wait until the prior-queued ordinary run has executed (a second tool_use).
	deadline := time.Now().Add(3 * time.Second)
	released := false
	for time.Now().Before(deadline) && !released {
		hist, err := events.History(ctx, session.ID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range hist {
			if e.Type == domain.EvAgentCustomToolUse {
				count++
			}
		}
		// Releasing the gate lets the prior-queued ordinary run be claimed; with the
		// deterministic fake it re-requests the same custom tool, so a SECOND
		// agent.custom_tool_use is the proof the gate cleared and prior work ran.
		if count == 2 {
			released = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !released {
		t.Fatal("prior-queued ordinary run never executed after the gate cleared")
	}
}

// run through the real AgentCore + model.Fake and proves the preview contract:
//   - a subscriber opted into agent.message previews receives event_start and at
//     least one event_delta, all carrying the same event id, followed by the
//     persisted agent.message with that identical id.
//   - a subscriber that did NOT opt in receives only the persisted agent.message
//     (no preview frames at all).
//   - after the run, the event history (List Events) contains the agent.message
//     but ZERO preview frames — the never-persisted proof.
func TestSessionService_PreviewStreamsToOptedInOnlyNeverPersisted(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	hub := NewHub(256)
	events := NewEventService(store.NewEventStore(db, ids, clk), hub)
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(model.NewFake(), ids), sandbox.NewLocalProvider(), ids, clk,
	)
	agent, _ := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	environment, _ := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	session, err := sessions.Create(ctx, CreateSessionInput{AgentID: agent.ID, EnvironmentID: environment.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Subscribe both consumers before driving a turn so neither can miss a frame.
	optedCh, optedCancel := hub.Subscribe(session.ID, map[string]bool{domain.EvAgentMessage: true})
	defer optedCancel()
	plainCh, plainCancel := hub.Subscribe(session.ID, nil)
	defer plainCancel()

	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// Drain the opted-in stream until we see the persisted agent.message.
	var startID, deltaID, persistedID string
	var startCount, deltaCount int
	deadline := time.After(2 * time.Second)
optedLoop:
	for {
		select {
		case f, open := <-optedCh:
			if !open {
				break optedLoop
			}
			switch {
			case f.Preview != nil:
				switch f.Preview.Kind {
				case domain.PreviewEventStart:
					startCount++
					startID = f.Preview.EventID
					if f.Preview.EventType != domain.EvAgentMessage {
						t.Fatalf("event_start EventType = %q, want agent.message", f.Preview.EventType)
					}
				case domain.PreviewEventDelta:
					deltaCount++
					deltaID = f.Preview.EventID
				}
			case f.Event != nil:
				if f.Event.Type == domain.EvAgentMessage {
					persistedID = f.Event.ID
					break optedLoop
				}
			}
		case <-deadline:
			t.Fatal("opted-in stream did not deliver a persisted agent.message in time")
		}
	}

	if startCount < 1 {
		t.Errorf("opted-in stream received %d event_start frames, want >=1", startCount)
	}
	if deltaCount < 1 {
		t.Errorf("opted-in stream received %d event_delta frames, want >=1", deltaCount)
	}
	if persistedID == "" {
		t.Fatal("opted-in stream never saw the persisted agent.message")
	}
	if startID != persistedID || deltaID != persistedID {
		t.Fatalf("preview/persist id mismatch: start=%q delta=%q persisted=%q", startID, deltaID, persistedID)
	}

	// The non-opted subscriber must never see a preview frame; it sees only the
	// persisted agent.message.
	deadline2 := time.After(2 * time.Second)
plainLoop:
	for {
		select {
		case f, open := <-plainCh:
			if !open {
				t.Fatal("plain stream closed before persisted agent.message")
			}
			if f.Preview != nil {
				t.Fatalf("non-opted subscriber received a preview frame: %#v", *f.Preview)
			}
			if f.Event != nil && f.Event.Type == domain.EvAgentMessage {
				if f.Event.ID != persistedID {
					t.Fatalf("plain stream agent.message id=%q, want %q", f.Event.ID, persistedID)
				}
				break plainLoop
			}
		case <-deadline2:
			t.Fatal("plain stream did not deliver the persisted agent.message")
		}
	}

	// Never-persisted proof: history holds the agent.message and NO preview.
	hist, err := events.History(ctx, session.ID, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var sawAgentMessage bool
	for _, e := range hist {
		switch e.Type {
		case domain.EvAgentMessage:
			sawAgentMessage = true
			if e.ID != persistedID {
				t.Fatalf("history agent.message id=%q, want %q", e.ID, persistedID)
			}
		case domain.PreviewEventStart, domain.PreviewEventDelta:
			t.Fatalf("preview frame leaked into persisted history: %+v", e)
		}
	}
	if !sawAgentMessage {
		t.Fatal("history is missing the persisted agent.message")
	}
}

// confirmClient is a scripted model.Client for the confirmation-resume tests. On
// the first turn (no tool_result yet) it emits assistant text AND requests the
// always_ask built-in with a real input, which parks the run. Emitting text
// alongside the tool_use exercises the alternation-merge path: the dangling
// tool_use is dropped from projected history, so the resume seeds the recovered
// tool_use into the trailing assistant text message. Once the projected history
// carries a tool_result for that call (the confirmation was resolved), it ends
// the turn with a plain text reply. This lets an end-to-end test observe a
// genuine side effect on allow and its absence on deny.
//
// It is strict about the Messages contract: every request it receives must have
// strictly alternating roles, so a merge regression that produces two
// consecutive assistant messages fails the test here rather than passing under a
// lenient fake. Failures are recorded via t.
type confirmClient struct {
	t        *testing.T
	toolName string
	input    map[string]any
}

func (c confirmClient) CreateMessage(_ context.Context, req model.Request) (model.Response, error) {
	if c.t != nil {
		c.t.Helper()
		for i := 1; i < len(req.Messages); i++ {
			if req.Messages[i].Role == req.Messages[i-1].Role {
				c.t.Errorf("model received consecutive %s messages at index %d: %#v",
					req.Messages[i].Role, i, req.Messages)
			}
		}
	}
	if !hasToolResultMsg(req.Messages) {
		return model.Response{
			Content: []domain.ContentBlock{
				{Type: "text", Text: "I'll write the file."},
				{Type: "tool_use", ToolUseID: "fake_use", ToolName: c.toolName, Input: c.input},
			},
			StopReason: "tool_use",
		}, nil
	}
	return model.Response{
		Content:    []domain.ContentBlock{{Type: "text", Text: "done"}},
		StopReason: "end_turn",
	}, nil
}

func (c confirmClient) CreateMessageStream(ctx context.Context, req model.Request, _ func(int, string)) (model.Response, error) {
	return c.CreateMessage(ctx, req)
}

func hasToolResultMsg(msgs []domain.Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}

// setupConfirmSession builds a session whose agent enables exactly the write
// built-in under an always_ask policy, using the given scripted model client. It
// returns the wired services and the parked session, having driven the initial
// turn to the requires_action park.
func setupConfirmSession(t *testing.T, client model.Client) (*SessionService, *EventService, domain.Session) {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	events := NewEventService(store.NewEventStore(db, ids, clk), NewHub(64))
	runs := store.NewRunStore(db, ids, clk)
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	environments := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, runs, agentruntime.NewAgentCore(client, ids), sandbox.NewLocalProvider(), ids, clk,
	)

	enabled := true
	agent, err := agents.Create(ctx, domain.Agent{
		Name:  "ask-agent",
		Model: domain.Model{ID: "claude-opus-4-8"},
		Tools: []any{map[string]any{
			"type":           domain.BuiltinToolsetType,
			"default_config": map[string]any{"enabled": false},
			"configs": []any{map[string]any{
				"name":              "write",
				"enabled":           enabled,
				"permission_policy": map[string]any{"type": "always_ask"},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := environments.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Create(ctx, CreateSessionInput{
		AgentID:       agent.ID,
		EnvironmentID: environment.ID,
		InitialEvents: []domain.EventDraft{{
			Type:    domain.EvUserMessage,
			Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "write the file"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.sandbox.Release(context.Background(), session.ID) })
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)
	return sessions, events, session
}

// parkedToolUseID returns the committed always_ask agent.tool_use id from the
// session history and asserts the park's requires_action stop_reason names it.
func parkedToolUseID(t *testing.T, events *EventService, sessionID string) string {
	t.Helper()
	hist, err := events.History(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var useID string
	var parked *domain.Event
	for i := range hist {
		switch hist[i].Type {
		case domain.EvAgentToolUse:
			useID = hist[i].ID
		case domain.EvSessionStatusIdle:
			e := hist[i]
			parked = &e
		}
	}
	if useID == "" {
		t.Fatal("no agent.tool_use in history")
	}
	if parked == nil {
		t.Fatal("no session.status_idle in history")
	}
	stop, _ := parked.Payload["stop_reason"].(map[string]any)
	if stop == nil || stop["type"] != "requires_action" {
		t.Fatalf("stop_reason = %#v, want requires_action", parked.Payload["stop_reason"])
	}
	eventIDs, _ := stop["event_ids"].([]any)
	if len(eventIDs) != 1 || eventIDs[0] != useID {
		t.Fatalf("stop_reason.event_ids = %#v, want [%s]", stop["event_ids"], useID)
	}
	return useID
}

// TestSessionService_ConfirmationAllowResumeExecutesSideEffect drives the full
// always_ask allow flow end-to-end: park → durable confirmation → resume →
// agent.tool_result → end_turn. The allowed side effect (the file write) occurs
// and the pending gate clears.
func TestSessionService_ConfirmationAllowResumeExecutesSideEffect(t *testing.T) {
	ctx := context.Background()
	sessions, events, session := setupConfirmSession(t, confirmClient{
		t:        t,
		toolName: "write",
		input:    map[string]any{"path": "confirmed.txt", "file_text": "allowed content"},
	})
	useID := parkedToolUseID(t, events, session.ID)

	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserToolConfirmation,
		Payload: map[string]any{"tool_use_id": useID, "result": "allow"},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// The allowed side effect occurred: the file is present in the session sandbox.
	box, err := sessions.sandbox.Acquire(ctx, session.ID, sandbox.Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	data, err := box.ReadFile(ctx, "confirmed.txt")
	if err != nil {
		t.Fatalf("allowed write did not occur: %v", err)
	}
	if string(data) != "allowed content" {
		t.Fatalf("written content = %q, want %q", data, "allowed content")
	}

	// The resumed run emitted an agent.tool_result correlated to the original id
	// and reached end_turn.
	hist, err := events.History(ctx, session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var sawResult, sawEndTurn bool
	for _, e := range hist {
		if e.Type == domain.EvAgentToolResult && e.Payload["tool_use_id"] == useID {
			sawResult = true
			if isErr, _ := e.Payload["is_error"].(bool); isErr {
				t.Fatalf("allow tool_result is_error = true, want false")
			}
		}
		if e.Type == domain.EvSessionStatusIdle {
			if stop, _ := e.Payload["stop_reason"].(map[string]any); stop != nil && stop["type"] == "end_turn" {
				sawEndTurn = true
			}
		}
	}
	if !sawResult {
		t.Error("no agent.tool_result correlated to the original tool_use id")
	}
	if !sawEndTurn {
		t.Error("resumed run did not reach end_turn")
	}
}

// TestSessionService_ConfirmationDenyResumeSkipsSideEffect drives the full
// always_ask deny flow end-to-end: park → durable confirmation → resume →
// rejection agent.tool_result → end_turn. The side effect does NOT occur, the
// tool_result is an error carrying the deny_message, and the gate clears.
func TestSessionService_ConfirmationDenyResumeSkipsSideEffect(t *testing.T) {
	ctx := context.Background()
	sessions, events, session := setupConfirmSession(t, confirmClient{
		t:        t,
		toolName: "write",
		input:    map[string]any{"path": "denied.txt", "file_text": "should not be written"},
	})
	useID := parkedToolUseID(t, events, session.ID)

	if _, err := sessions.SendEvent(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolConfirmation,
		Payload: map[string]any{
			"tool_use_id": useID, "result": "deny", "deny_message": "policy forbids writes",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	pollUntilStatus(t, sessions, session.ID, domain.StatusIdle)

	// The denied side effect did NOT occur: the file is absent from the sandbox.
	box, err := sessions.sandbox.Acquire(ctx, session.ID, sandbox.Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.ReadFile(ctx, "denied.txt"); err == nil {
		t.Fatal("denied write occurred, want no side effect")
	}

	hist, err := events.History(ctx, session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var sawRejection, sawEndTurn bool
	for _, e := range hist {
		if e.Type == domain.EvAgentToolResult && e.Payload["tool_use_id"] == useID {
			isErr, _ := e.Payload["is_error"].(bool)
			if !isErr {
				t.Fatalf("deny tool_result is_error = false, want true")
			}
			content, _ := e.Payload["content"].([]any)
			var text string
			for _, item := range content {
				if m, ok := item.(map[string]any); ok {
					if s, _ := m["text"].(string); s != "" {
						text += s
					}
				}
			}
			if !contains(text, "policy forbids writes") {
				t.Fatalf("deny tool_result text = %q, want it to include deny_message", text)
			}
			sawRejection = true
		}
		if e.Type == domain.EvSessionStatusIdle {
			if stop, _ := e.Payload["stop_reason"].(map[string]any); stop != nil && stop["type"] == "end_turn" {
				sawEndTurn = true
			}
		}
	}
	if !sawRejection {
		t.Error("no rejection agent.tool_result correlated to the original tool_use id")
	}
	if !sawEndTurn {
		t.Error("resumed run did not reach end_turn")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
