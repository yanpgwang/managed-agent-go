package app

import (
	"context"
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

// newSessionServiceWithRuntime builds a SessionService over in-memory stores with
// a caller-supplied runtime, so interrupt tests can inject a runtime that blocks
// until its context is canceled.
func newSessionServiceWithRuntime(t *testing.T, rt agentruntime.AgentRuntime) (*SessionService, string, string) {
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
	agents := NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	ss := NewSessionService(
		store.NewSessionRepo(db), store.NewAgentRepo(db), store.NewEnvironmentRepo(db),
		events, store.NewRunStore(db, ids, clk), rt, sandbox.NewLocalProvider(), ids, clk,
	)
	ag, err := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := envs.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	return ss, ag.ID, env.ID
}

type blockEnd struct {
	sessionID string
	cause     error
}

// blockingRuntime blocks a user.message whose text contains "block" until its
// context is canceled, recording the cancellation cause per session. Every other
// user.message echoes and idles; a user.interrupt emits the single idle end_turn
// the way the real runtime's no-op interrupt handling does. It lets a test hold a
// run mid-execution, admit a user.interrupt, and observe the cancellation the
// interrupt propagates — scoped to the right session.
type blockingRuntime struct {
	started chan string   // sessionID when a blocking run begins
	ended   chan blockEnd // sessionID + observed cause when a blocking run unblocks
}

func newBlockingRuntime(buf int) *blockingRuntime {
	return &blockingRuntime{
		started: make(chan string, buf),
		ended:   make(chan blockEnd, buf),
	}
}

func (r *blockingRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	switch req.Trigger.Type {
	case domain.EvUserInterrupt:
		_, err := sink.Emit(ctx, []domain.EventDraft{idleEndTurnDraft()})
		return agentruntime.RunOutcome{}, err
	case domain.EvUserMessage:
		text := contentText(req.Trigger.Payload)
		if strings.Contains(text, "block") {
			r.started <- req.SessionID
			<-ctx.Done()
			r.ended <- blockEnd{sessionID: req.SessionID, cause: context.Cause(ctx)}
			return agentruntime.RunOutcome{}, ctx.Err()
		}
		_, err := sink.Emit(ctx, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": textBlocks("reply: " + text)}},
			idleEndTurnDraft(),
		})
		return agentruntime.RunOutcome{}, err
	default:
		return agentruntime.RunOutcome{}, nil
	}
}

// lateInterruptRuntime deterministically reproduces the late-interrupt window: a
// user.message run emits its authoritative output AND a terminal idle draft into
// the buffered sink, then signals the test and blocks in its return path until the
// test releases it. That lets the test admit a user.interrupt in the exact window
// after the runtime has produced its result but before drainRuns commits the
// completion — the window a context.Cause-only check could not linearize. A
// user.interrupt run emits the single idle end_turn like the real no-op handler.
type lateInterruptRuntime struct {
	atReturn chan struct{} // signaled once the message run has emitted and is about to return
	release  chan struct{} // closed by the test to let the message run return
}

func newLateInterruptRuntime() *lateInterruptRuntime {
	return &lateInterruptRuntime{
		atReturn: make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
}

func (r *lateInterruptRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	switch req.Trigger.Type {
	case domain.EvUserInterrupt:
		_, err := sink.Emit(ctx, []domain.EventDraft{idleEndTurnDraft()})
		return agentruntime.RunOutcome{}, err
	case domain.EvUserMessage:
		// Emit an authoritative agent.message plus a terminal idle draft into the
		// buffered sink — the runtime "finished" its work. Then signal and block in
		// the return path so the test can admit the interrupt before the completion
		// commits. The buffered idle draft must be stripped on the interrupted path.
		if _, err := sink.Emit(ctx, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": textBlocks("reply: " + contentText(req.Trigger.Payload))}},
			idleEndTurnDraft(),
		}); err != nil {
			return agentruntime.RunOutcome{}, err
		}
		r.atReturn <- struct{}{}
		<-r.release
		return agentruntime.RunOutcome{}, nil
	default:
		return agentruntime.RunOutcome{}, nil
	}
}

// TestSessionService_LateInterruptWinsClassifiesInterrupted proves the finish-vs-
// admit linearization at the integration level: an interrupt durably admitted in
// the narrow window after the runtime returned its result but before the run's
// completion commits is classified as interrupted. Because the token records the
// interrupt under the shard lock before finish observes it, the completing run
// commits its authoritative agent.message, its own buffered terminal idle draft is
// stripped, and it neither idles the session nor appends a redundant
// session.status_running; the interrupt's own control run produces the single
// public idle{end_turn}. Existing blocking-runtime tests cancel the runtime
// mid-call and so never exercise this post-return window.
func TestSessionService_LateInterruptWinsClassifiesInterrupted(t *testing.T) {
	rt := newLateInterruptRuntime()
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("go-late")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wait until the message run has emitted its output and is parked in its return
	// path — this is the late window.
	select {
	case <-rt.atReturn:
	case <-time.After(2 * time.Second):
		t.Fatal("message run did not reach its return path")
	}

	// Admit the interrupt now: it commits durably and cancels the (already
	// returning) run under the shard lock, marking the token interrupted BEFORE
	// drainRuns reaches finish.
	sent, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}})
	if err != nil {
		t.Fatalf("SendEvent interrupt: %v", err)
	}

	// Release the message run so its completion commits — now classified interrupted.
	close(rt.release)

	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)
	pollUntilEventProcessed(t, ss, sess.ID, sent[0].ID)

	history, err := ss.events.History(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var idleEndTurns, statusRunning int
	var sawReply bool
	for _, e := range history {
		switch e.Type {
		case domain.EvSessionError:
			t.Fatalf("late interrupt emitted session.error: %#v", e.Payload)
		case domain.EvSessionStatusTerminated:
			t.Fatalf("late interrupt terminated the session")
		case domain.EvSessionStatusRunning:
			statusRunning++
		case domain.EvSessionStatusIdle:
			stop, _ := e.Payload["stop_reason"].(map[string]any)
			if stop["type"] == "end_turn" {
				idleEndTurns++
			}
		case domain.EvAgentMessage:
			if contentBlockText(e.Payload["content"]) == "reply: go-late" {
				sawReply = true
			}
		}
	}
	// The interrupted run's authoritative output is committed honestly...
	if !sawReply {
		t.Fatal("interrupted run's buffered agent.message was not committed")
	}
	// ...but its buffered terminal idle draft was stripped, so exactly one idle
	// end_turn exists — from the interrupt's own control run.
	if idleEndTurns != 1 {
		t.Fatalf("idle end_turn count = %d, want exactly 1 (from the interrupt control run)", idleEndTurns)
	}
	// Exactly one session.status_running exists — the initial admission's. The
	// interrupted completion passed StatusRunning explicitly, so it appended NO
	// redundant session.status_running for the still-queued interrupt run.
	if statusRunning != 1 {
		t.Fatalf("session.status_running count = %d, want exactly 1 (no redundant running from the interrupted completion)", statusRunning)
	}
}

// pollUntilEventProcessed waits until the event with eventID in sessionID is
// marked processed_at, failing the test on timeout.
func pollUntilEventProcessed(t *testing.T, ss *SessionService, sessionID, eventID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hist, err := ss.events.History(context.Background(), sessionID, 0, 100000)
		if err == nil {
			for _, e := range hist {
				if e.ID == eventID && e.ProcessedAt != nil {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for event %s in session %s to be processed", eventID, sessionID)
}

func idleEndTurnDraft() domain.EventDraft {
	return domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
	}
}

// TestSessionService_InterruptCancelsActiveRunScopedAndCleansUp proves the core
// cancellation contract: after SendEvent durably admits a user.interrupt, the
// session's currently active run receives a context cancellation carrying the
// errInterrupted cause; the cancellation is scoped to that session only (a second
// session's blocking run is untouched); and the registry is cleaned up so no
// canceler leaks once the session settles.
func TestSessionService_InterruptCancelsActiveRunScopedAndCleansUp(t *testing.T) {
	rt := newBlockingRuntime(4)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	// Two sessions each start a run that blocks mid-execution.
	sessA, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-A")}}},
	})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}
	sessB, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-B")}}},
	})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	// Both blocking runs must be active before we interrupt, so the interrupt hits
	// a registered active run rather than a not-yet-claimed one.
	started := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case sid := <-rt.started:
			started[sid] = true
		case <-time.After(2 * time.Second):
			t.Fatal("blocking runs did not both start")
		}
	}
	if !started[sessA.ID] || !started[sessB.ID] {
		t.Fatalf("expected both sessions to start, got %v", started)
	}

	// Interrupt only session A.
	if _, err := ss.SendEvent(ctx, sessA.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}); err != nil {
		t.Fatalf("SendEvent interrupt A: %v", err)
	}

	// Exactly session A's run unblocks, and it observed the interrupt cause.
	select {
	case end := <-rt.ended:
		if end.sessionID != sessA.ID {
			t.Fatalf("wrong session canceled: got %s, want %s", end.sessionID, sessA.ID)
		}
		if end.cause != errInterrupted {
			t.Fatalf("cancel cause = %v, want errInterrupted", end.cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session A's run was not canceled by the interrupt")
	}

	// Session B must NOT be canceled by session A's interrupt: no further ended
	// signal arrives within a window.
	select {
	case end := <-rt.ended:
		t.Fatalf("session B run canceled by session A interrupt: %+v", end)
	case <-time.After(200 * time.Millisecond):
	}

	// Session A settles to idle (canceled run completed, then the interrupt run
	// idled). Its canceler must be cleaned up — no leak.
	pollUntilStatus(t, ss, sessA.ID, domain.StatusIdle)
	ss.cancelers.mu.Lock()
	_, present := ss.cancelers.m[sessA.ID]
	ss.cancelers.mu.Unlock()
	if present {
		t.Fatal("session A canceler leaked after the run settled")
	}

	// Clean up session B by interrupting it so its blocking goroutine exits.
	if _, err := ss.SendEvent(ctx, sessB.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}); err != nil {
		t.Fatalf("SendEvent interrupt B: %v", err)
	}
	select {
	case end := <-rt.ended:
		if end.sessionID != sessB.ID || end.cause != errInterrupted {
			t.Fatalf("session B cleanup cancel = %+v", end)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session B did not cancel on its own interrupt")
	}
	pollUntilStatus(t, ss, sessB.ID, domain.StatusIdle)
}

// TestSessionService_InterruptBatchRedirectRunsNormally is the end-to-end batch
// case: while a run is blocking, one events request carries [user.interrupt,
// user.message]. The blocking run cancels (no session.error / terminated); the
// interrupt event is marked processed; the redirect message then runs normally
// and its agent reply persists.
func TestSessionService_InterruptBatchRedirectRunsNormally(t *testing.T) {
	rt := newBlockingRuntime(4)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-me")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial blocking run did not start")
	}

	sent, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{
		{Type: domain.EvUserInterrupt, Payload: map[string]any{}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("redirect")}},
	})
	if err != nil {
		t.Fatalf("SendEvent batch: %v", err)
	}
	interruptID := sent[0].ID

	// The blocking run cancels on the interrupt.
	select {
	case end := <-rt.ended:
		if end.cause != errInterrupted {
			t.Fatalf("cancel cause = %v, want errInterrupted", end.cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking run was not canceled by the interrupt")
	}

	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	history, err := ss.events.History(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var sawRedirectReply, interruptProcessed bool
	for _, e := range history {
		switch e.Type {
		case domain.EvSessionError:
			t.Fatalf("interrupt cancellation emitted session.error: %#v", e.Payload)
		case domain.EvSessionStatusTerminated:
			t.Fatalf("interrupt cancellation terminated the session")
		case domain.EvAgentMessage:
			if contentBlockText(e.Payload["content"]) == "reply: redirect" {
				sawRedirectReply = true
			}
		}
		if e.ID == interruptID && e.ProcessedAt != nil {
			interruptProcessed = true
		}
	}
	if !interruptProcessed {
		t.Fatal("user.interrupt was not marked processed")
	}
	if !sawRedirectReply {
		t.Fatal("redirect message did not run to an agent reply")
	}
}

// TestSessionService_InterruptOnlyEndsSingleIdleAndAllowsArchiveDelete proves the
// public handoff shape for an interrupt with no redirect: the canceled work
// contributes NO idle terminal, and the interrupt's own run produces exactly one
// session.status_idle{end_turn}. Afterward the idle session can be archived and
// deleted.
func TestSessionService_InterruptOnlyEndsSingleIdleAndAllowsArchiveDelete(t *testing.T) {
	rt := newBlockingRuntime(4)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-solo")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking run did not start")
	}

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}); err != nil {
		t.Fatalf("SendEvent interrupt: %v", err)
	}
	select {
	case end := <-rt.ended:
		if end.cause != errInterrupted {
			t.Fatalf("cancel cause = %v, want errInterrupted", end.cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run was not canceled by the interrupt")
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	history, err := ss.events.History(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var idleEndTurns int
	for _, e := range history {
		if e.Type == domain.EvSessionError || e.Type == domain.EvSessionStatusTerminated {
			t.Fatalf("interrupt produced %s", e.Type)
		}
		if e.Type == domain.EvSessionStatusIdle {
			stop, _ := e.Payload["stop_reason"].(map[string]any)
			if stop["type"] != "end_turn" {
				t.Fatalf("idle stop_reason = %#v, want end_turn", stop)
			}
			idleEndTurns++
		}
	}
	if idleEndTurns != 1 {
		t.Fatalf("expected exactly one idle end_turn from the interruption, got %d", idleEndTurns)
	}

	// The interrupted session is idle: archive and delete must succeed.
	if _, err := ss.Archive(ctx, sess.ID); err != nil {
		t.Fatalf("Archive after interrupt: %v", err)
	}
	if err := ss.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete after interrupt: %v", err)
	}
}

// TestSessionService_InterruptWhileIdleIsNoOpControlEvent proves an interrupt that
// arrives with no active run is a safe control event: it is durably processed, the
// session stays idle, and the model is never invoked. Uses the real AgentCore over
// a recording fake model to prove no model call happens.
func TestSessionService_InterruptWhileIdleIsNoOpControlEvent(t *testing.T) {
	fakeModel := model.NewFake()
	ids := domain.NewSeqIDGen()
	rt := agentruntime.NewAgentCore(fakeModel, ids)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{AgentID: agID, EnvironmentID: envID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Status != domain.StatusIdle {
		t.Fatalf("session not idle at start: %s", sess.Status)
	}

	sent, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}})
	if err != nil {
		t.Fatalf("SendEvent interrupt: %v", err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// The interrupt run must not have called the model.
	if got := fakeModel.LastRequest(); len(got.Messages) != 0 {
		t.Fatalf("interrupt-while-idle invoked the model: %#v", got.Messages)
	}
	// The interrupt event is durably processed and the session did not terminate.
	history, err := ss.events.History(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var processed bool
	for _, e := range history {
		if e.Type == domain.EvSessionStatusTerminated || e.Type == domain.EvSessionError {
			t.Fatalf("idle interrupt produced %s", e.Type)
		}
		if e.Type == domain.EvAgentMessage {
			t.Fatalf("interrupt-while-idle unexpectedly drove a model turn: %#v", e.Payload)
		}
		if e.ID == sent[0].ID {
			processed = e.ProcessedAt != nil
		}
	}
	if !processed {
		t.Fatal("idle interrupt was not marked processed")
	}
}

// TestSessionService_DuplicateConcurrentInterruptsSafe fires several interrupts at
// a session concurrently — some batched, some from separate goroutines — while a
// run is blocking. It must not panic, leak, or cross-cancel; the session settles
// to idle. Run under -race to stress the registry synchronization.
func TestSessionService_DuplicateConcurrentInterruptsSafe(t *testing.T) {
	rt := newBlockingRuntime(64)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-dup")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking run did not start")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Some goroutines send a batch of two interrupts, some a single one.
			drafts := []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}
			if i%2 == 0 {
				drafts = append(drafts, domain.EventDraft{Type: domain.EvUserInterrupt, Payload: map[string]any{}})
			}
			_, _ = ss.SendEvent(ctx, sess.ID, drafts)
		}()
	}
	wg.Wait()

	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// No canceler leak after everything settles.
	ss.cancelers.mu.Lock()
	_, present := ss.cancelers.m[sess.ID]
	ss.cancelers.mu.Unlock()
	if present {
		t.Fatal("canceler leaked after concurrent interrupts settled")
	}
}

// TestRunCancelers_FinishBeforeCancelIsNormalCompletion is the deterministic unit
// proof of the finish-vs-interrupt linearization when NORMAL COMPLETION wins. It
// reproduces the exact late window blocker 1 described: the run reaches its
// completion (finish) and only afterward does an interrupt durably admit and
// cancel. finish must report NOT interrupted, and the later cancel must be an
// idempotent no-op that does not invoke the (already-finished) run's cancel func.
// Both transitions are the ones drainRuns/SendEvent run under the shard lock, so
// this asserts the state machine directly without any timing.
func TestRunCancelers_FinishBeforeCancelIsNormalCompletion(t *testing.T) {
	r := newRunCancelers()
	var canceled bool
	tok := r.register("sess", func(error) { canceled = true })

	// Completion wins first.
	if interrupted := r.finish("sess", tok); interrupted {
		t.Fatal("finish reported interrupted before any interrupt was admitted")
	}
	// A late interrupt now admits and cancels: it must not fire the finished run's
	// cancel func and must not retroactively mark it interrupted.
	r.cancel("sess", errInterrupted)
	if canceled {
		t.Fatal("cancel fired the already-finished run's cancel func")
	}
	if tok.interrupted {
		t.Fatal("a post-completion interrupt reclassified the finished run")
	}
	// The token was removed on finish: no canceler leak.
	r.mu.Lock()
	_, present := r.m["sess"]
	r.mu.Unlock()
	if present {
		t.Fatal("finish did not remove the canceler")
	}
}

// TestRunCancelers_CancelBeforeFinishIsInterrupt is the mirror: the interrupt
// wins first (cancel), then the run reaches finish. cancel must fire the run's
// cancel func exactly once and record the interrupt; finish must then report
// interrupted so drainRuns classifies the run interrupted and emits no idle of
// its own.
func TestRunCancelers_CancelBeforeFinishIsInterrupt(t *testing.T) {
	r := newRunCancelers()
	var cancels int
	var cause error
	tok := r.register("sess", func(c error) { cancels++; cause = c })

	r.cancel("sess", errInterrupted)
	if cancels != 1 || cause != errInterrupted {
		t.Fatalf("cancel = %d calls cause=%v, want 1 call errInterrupted", cancels, cause)
	}
	if interrupted := r.finish("sess", tok); !interrupted {
		t.Fatal("finish did not report the interrupt that won the race")
	}
	// Idempotent: a duplicate interrupt after finish must not fire cancel again.
	r.cancel("sess", errInterrupted)
	if cancels != 1 {
		t.Fatalf("cancel fired again after finish: %d calls", cancels)
	}
}

// TestRunCancelers_CancelWithNoActiveRunIsNoOp proves an interrupt admitted while
// the session has no registered active run (interrupt while idle) is a safe no-op:
// nothing to cancel, no panic.
func TestRunCancelers_CancelWithNoActiveRunIsNoOp(t *testing.T) {
	r := newRunCancelers()
	r.cancel("sess", errInterrupted) // must not panic
}

// bufferingBlockingRuntime emits an agent.message AND a terminal
// session.status_idle draft, then blocks until its context is canceled. It models
// a Fake/custom runtime that buffered a terminal status draft before an interrupt
// won the completion race (blocker 3): the app must strip that buffered idle so
// the interrupt's own control run is the single public idle, and must not append a
// synthetic session.status_running for the still-queued interrupt run (blocker 2).
type bufferingBlockingRuntime struct {
	started chan string
	ended   chan blockEnd
}

func newBufferingBlockingRuntime(buf int) *bufferingBlockingRuntime {
	return &bufferingBlockingRuntime{started: make(chan string, buf), ended: make(chan blockEnd, buf)}
}

func (r *bufferingBlockingRuntime) Run(
	ctx context.Context,
	req agentruntime.RunRequest,
	sink agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	switch req.Trigger.Type {
	case domain.EvUserInterrupt:
		_, err := sink.Emit(ctx, []domain.EventDraft{idleEndTurnDraft()})
		return agentruntime.RunOutcome{}, err
	case domain.EvUserMessage:
		// Buffer authoritative output AND a terminal idle before blocking, so the
		// interrupt races a runtime that already staged its own terminal status.
		if _, err := sink.Emit(ctx, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": textBlocks("partial")}},
			idleEndTurnDraft(),
		}); err != nil {
			return agentruntime.RunOutcome{}, err
		}
		r.started <- req.SessionID
		<-ctx.Done()
		r.ended <- blockEnd{sessionID: req.SessionID, cause: context.Cause(ctx)}
		return agentruntime.RunOutcome{}, ctx.Err()
	default:
		return agentruntime.RunOutcome{}, nil
	}
}

// TestSessionService_InterruptStripsBufferedTerminalAndNoRedundantRunning is the
// strongly-controlled event-shape regression for blockers 2 and 3. A runtime
// buffers agent.message + session.status_idle then blocks; an interrupt cancels
// it. The interrupted run's buffered idle must be stripped (so the interrupt's own
// control run is the single public session.status_idle{end_turn}) and its
// completion must NOT append a synthetic session.status_running for the queued
// interrupt run. The session's authoritative agent.message stays committed.
func TestSessionService_InterruptStripsBufferedTerminalAndNoRedundantRunning(t *testing.T) {
	rt := newBufferingBlockingRuntime(4)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-buf")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking run did not start")
	}

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}); err != nil {
		t.Fatalf("SendEvent interrupt: %v", err)
	}
	select {
	case end := <-rt.ended:
		if end.cause != errInterrupted {
			t.Fatalf("cancel cause = %v, want errInterrupted", end.cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run was not canceled by the interrupt")
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	history, err := ss.events.History(ctx, sess.ID, 0, 1000)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var idleEndTurns, statusRunning, agentMessages int
	for _, e := range history {
		switch e.Type {
		case domain.EvSessionError, domain.EvSessionStatusTerminated:
			t.Fatalf("interrupt produced %s", e.Type)
		case domain.EvSessionStatusRunning:
			statusRunning++
		case domain.EvAgentMessage:
			agentMessages++
		case domain.EvSessionStatusIdle:
			stop, _ := e.Payload["stop_reason"].(map[string]any)
			if stop["type"] != "end_turn" {
				t.Fatalf("idle stop_reason = %#v, want end_turn", stop)
			}
			idleEndTurns++
		}
	}
	// Exactly one status_running (admission of the original message); the canceled
	// run's completion must NOT add a second one for the queued interrupt run.
	if statusRunning != 1 {
		t.Fatalf("session.status_running count = %d, want 1 (no synthetic running on interrupted completion)", statusRunning)
	}
	// Exactly one idle end_turn (from the interrupt's control run); the runtime's
	// buffered idle draft must have been stripped from the interrupted run.
	if idleEndTurns != 1 {
		t.Fatalf("idle end_turn count = %d, want 1 (buffered terminal stripped)", idleEndTurns)
	}
	// The authoritative partial agent.message stays committed honestly.
	if agentMessages != 1 {
		t.Fatalf("agent.message count = %d, want 1 (buffered authoritative draft kept)", agentMessages)
	}
}

// TestSessionService_InterruptStreamPreservesCommitOrder guards the live-stream
// ordering guarantee the publish-under-lock change provides: for a single session,
// every committed Admit/ClaimNext/Complete result is published while holding the
// shard lock, in commit order, so a subscriber observes persisted events in
// strictly increasing durable Sequence across an interrupt+cancel. Here a run
// blocks after buffering higher-sequence output, then an interrupt is admitted and
// cancels it; the subscriber must see the lower-sequence user.interrupt before the
// canceled run's later-sequence agent.message/idle, and the sequence must never
// regress.
//
// This asserts the invariant rather than forcing the pre-fix interleave: the racy
// window (SendEvent publishing after unlocking while the canceled drain publishes
// its higher-sequence completion) cannot be driven deterministically without a
// hook inside RunStore.Complete, which is out of scope. With publishing under the
// lock the ordering holds structurally; the test is a standing guard that also
// runs under `-race`.
func TestSessionService_InterruptStreamPreservesCommitOrder(t *testing.T) {
	rt := newBufferingBlockingRuntime(4)
	ss, agID, envID := newSessionServiceWithRuntime(t, rt)
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("block-order")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Subscribe before the interrupt so every published frame from here on is
	// observed. A generous buffer avoids the slow-consumer drop policy.
	ch, cancel := ss.events.hub.Subscribe(sess.ID, nil)
	defer cancel()

	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking run did not start")
	}

	if _, err := ss.SendEvent(ctx, sess.ID, []domain.EventDraft{{Type: domain.EvUserInterrupt, Payload: map[string]any{}}}); err != nil {
		t.Fatalf("SendEvent interrupt: %v", err)
	}
	select {
	case end := <-rt.ended:
		if end.cause != errInterrupted {
			t.Fatalf("cancel cause = %v, want errInterrupted", end.cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run was not canceled by the interrupt")
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusIdle)

	// Drain the delivered frames and assert non-decreasing durable sequence, and
	// that the interrupt was delivered before the canceled run's later output.
	deadline := time.After(2 * time.Second)
	var lastSeq int64
	var sawInterrupt bool
	var interruptSeq int64
	// Collect until the terminal idle end_turn (the interrupt control run's) is seen.
	for {
		select {
		case f := <-ch:
			if f.Event == nil {
				continue
			}
			e := f.Event
			if e.Sequence <= lastSeq {
				t.Fatalf("live stream regressed in sequence: event %s (seq %d) delivered after seq %d", e.Type, e.Sequence, lastSeq)
			}
			lastSeq = e.Sequence
			switch e.Type {
			case domain.EvUserInterrupt:
				sawInterrupt = true
				interruptSeq = e.Sequence
			case domain.EvAgentMessage:
				// The canceled run's buffered authoritative output. It must arrive
				// AFTER the lower-sequence interrupt event on the live stream.
				if !sawInterrupt {
					t.Fatal("canceled run's agent.message delivered before the user.interrupt on the live stream")
				}
				if e.Sequence <= interruptSeq {
					t.Fatalf("agent.message seq %d not after interrupt seq %d", e.Sequence, interruptSeq)
				}
			case domain.EvSessionStatusIdle:
				stop, _ := e.Payload["stop_reason"].(map[string]any)
				if stop["type"] == "end_turn" {
					if !sawInterrupt {
						t.Fatal("idle end_turn delivered before the user.interrupt on the live stream")
					}
					return // done: the interrupt control run's terminal idle was delivered last
				}
			}
		case <-deadline:
			t.Fatalf("timed out draining stream; sawInterrupt=%v lastSeq=%d", sawInterrupt, lastSeq)
		}
	}
}

// ctxCanceledRuntime returns context.Canceled as an ordinary error WITHOUT any
// interrupt ever being admitted. It proves the interrupt detection keys on the
// registered cancel cause (errInterrupted), not merely on a context.Canceled
// error surfacing from the runtime: such a run must take the normal terminate
// path, never the graceful interrupt path.
type ctxCanceledRuntime struct{}

func (ctxCanceledRuntime) Run(
	context.Context,
	agentruntime.RunRequest,
	agentruntime.EventSink,
) (agentruntime.RunOutcome, error) {
	return agentruntime.RunOutcome{}, context.Canceled
}

// TestSessionService_NonInterruptCancellationStillTerminates proves constraint 7:
// a context.Canceled error that did NOT originate from a durably admitted
// interrupt is an ordinary runtime error and terminates the session
// (session.error + session.status_terminated), never a graceful interrupt.
func TestSessionService_NonInterruptCancellationStillTerminates(t *testing.T) {
	ss, agID, envID := newSessionServiceWithRuntime(t, ctxCanceledRuntime{})
	ctx := context.Background()

	sess, err := ss.Create(ctx, CreateSessionInput{
		AgentID: agID, EnvironmentID: envID,
		InitialEvents: []domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": textBlocks("go")}}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pollUntilStatus(t, ss, sess.ID, domain.StatusTerminated)

	if !hasEventType(t, ss, sess.ID, domain.EvSessionError) {
		t.Fatal("non-interrupt cancellation did not emit session.error")
	}
	if hasEventType(t, ss, sess.ID, domain.EvSessionStatusIdle) {
		t.Fatal("non-interrupt cancellation emitted session.status_idle")
	}
}
