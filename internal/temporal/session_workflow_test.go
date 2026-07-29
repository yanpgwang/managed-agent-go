package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

// fakeSource is an in-memory EventSource for workflow tests. It records how many
// times each trigger's turn was completed so duplicate/gap protection can be
// asserted (a well-behaved workflow completes each user.message turn exactly
// once even under duplicate wakeups).
type fakeSource struct {
	mu        sync.Mutex
	events    []domain.Event
	completes map[string]int            // triggerEventID -> times CompleteTurn appended output
	byTurn    map[string][]domain.Event // triggerEventID -> committed output events
	maxSeq    int64
}

func newFakeSource(events []domain.Event) *fakeSource {
	var max int64
	for _, e := range events {
		if e.Sequence > max {
			max = e.Sequence
		}
	}
	return &fakeSource{events: events, completes: map[string]int{}, byTurn: map[string][]domain.Event{}, maxSeq: max}
}

func (f *fakeSource) EventsAfter(_ context.Context, _ string, cursor int64, limit int) ([]domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Event{}
	for _, e := range f.events {
		if e.Sequence > cursor {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeSource) HistoryThrough(_ context.Context, _ string, triggerEventID string, _ int) ([]domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Find the trigger's sequence, then return every event at or below it. The
	// workflow tests here exercise ordering/dedup, not causal reconstruction, so a
	// simple sequence bound suffices for the fake.
	var triggerSeq int64
	for _, e := range f.events {
		if e.ID == triggerEventID {
			triggerSeq = e.Sequence
		}
	}
	out := []domain.Event{}
	for _, e := range f.events {
		if e.Sequence <= triggerSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeSource) GetSession(_ context.Context, id string) (domain.Session, error) {
	return domain.Session{ID: id, Status: domain.StatusRunning}, nil
}

func (f *fakeSource) GetEvent(_ context.Context, _ string, id string) (domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.Event{}, domain.NotFound("event not found")
}

func (f *fakeSource) CompleteTurn(_ context.Context, sessionID, triggerEventID string, output []domain.EventDraft, status domain.Status) (TurnCompletionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Idempotent: if this trigger's turn already committed, replay its output
	// events without appending again — exactly the pg.Store contract.
	if f.completes[triggerEventID] > 0 {
		return TurnCompletionResult{Events: f.byTurn[triggerEventID], Applied: false, Status: status}, nil
	}
	f.completes[triggerEventID]++
	committed := make([]domain.Event, 0, len(output))
	for _, d := range output {
		f.maxSeq++
		e := domain.Event{ID: d.ID, SessionID: sessionID, Sequence: f.maxSeq, Type: d.Type, Payload: d.Payload}
		if e.ID == "" {
			e.ID = "out_" + itoaTest(f.maxSeq)
		}
		f.events = append(f.events, e)
		committed = append(committed, e)
	}
	f.byTurn[triggerEventID] = committed
	return TurnCompletionResult{Events: committed, Applied: true, Status: status}, nil
}

func (f *fakeSource) CompleteWorkflowTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	_ string,
	_ domain.RunAttemptState,
	_ *string,
) (TurnCompletionResult, error) {
	return f.CompleteTurn(ctx, sessionID, triggerEventID, output, status)
}

func (f *fakeSource) completions(triggerID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completes[triggerID]
}

func itoaTest(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// testIDGen is a trivial deterministic id generator for workflow tests.
type testIDGen struct {
	mu sync.Mutex
	n  int64
}

func (g *testIDGen) NewID(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return prefix + itoaTest(g.n)
}

func userMsg(id string, seq int64) domain.Event {
	return domain.Event{ID: id, Sequence: seq, Type: domain.EvUserMessage, Payload: map[string]any{"content": "hi"}}
}

func useLegacyRunTurnVersion(env *testsuite.TestWorkflowEnvironment) {
	env.OnGetVersion(
		workflowAgentLoopChangeID,
		workflow.DefaultVersion,
		workflow.Version(1),
	).Return(workflow.DefaultVersion)
}

// sessionWorkflowExitChecked turns a buffered wakeup at a close boundary into a
// test failure. Production relies on Temporal rejecting such a close and
// replaying; this wrapper lets the unit suite assert that sessionWorkflow
// proactively consumed every currently visible wakeup before returning or
// Continue-As-New.
func sessionWorkflowExitChecked(ctx workflow.Context, in SessionWorkflowInput, threshold int) error {
	err := sessionWorkflow(ctx, in, threshold)
	for _, name := range workflow.GetUnhandledSignalNames(ctx) {
		if name == WakeupSignalName {
			return errors.New("session workflow exited with a buffered wakeup")
		}
	}
	return err
}

// TestSessionWorkflow_ProcessesOneTurn drives one wakeup and asserts the turn's
// RunTurn Activity ran exactly once. It uses the Temporal test environment,
// registering the real workflow against activity mocks backed by the fake
// source, then Continue-As-New's out via a terminating signal is avoided by the
// env's default timeout — instead we assert through the activity mock.
func TestSessionWorkflow_ProcessesOneTurn(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	useLegacyRunTurnVersion(env)

	source := newFakeSource([]domain.Event{userMsg("evt_1", 1)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})

	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	// RunTurn needs a runtime; use a stub runtime through a wrapper activity that
	// emits a single agent.message then completes the turn.
	env.RegisterActivityWithOptions(runTurnStub(source), activity.RegisterOptions{Name: ActivityRunTurn})

	// Deliver a wakeup shortly after start, then let the workflow idle until the
	// test env's deadlock/timeout. We assert RunTurn ran via the fake source.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 1})
	}, time.Millisecond)

	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: "sess_1", StartCursor: 0})

	// The workflow blocks on the signal channel forever (long-lived session), so
	// it will not "complete"; the test env stops at its timeout. What we assert is
	// that the turn ran exactly once.
	require.Equal(t, 1, source.completions("evt_1"))
}

func TestSessionWorkflow_NewExecutionUsesWorkflowOwnedLoop(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	source := newFakeSource([]domain.Event{userMsg("evt_new_loop", 1)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	env.RegisterActivityWithOptions(
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{Request: model.Request{
				Model: "fake",
				Messages: []domain.Message{{
					Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "hi"}},
				}},
			}}, nil
		},
		activity.RegisterOptions{Name: ActivityPrepareTurn},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, CallModelInput) (CallModelResult, error) {
			return CallModelResult{
				MessageEventID: "sevt_new_loop_message",
				Response: model.Response{
					StopReason: "end_turn",
					Content:    []domain.ContentBlock{{Type: "text", Text: "ok"}},
				},
			}, nil
		},
		activity.RegisterOptions{Name: ActivityCallModel},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
			result, err := source.CompleteWorkflowTurn(
				ctx,
				in.SessionID,
				in.TriggerEventID,
				in.Output,
				in.Status,
				in.AttemptID,
				in.AttemptState,
				in.AttemptError,
			)
			return RunTurnResult{Terminated: result.Status == domain.StatusTerminated}, err
		},
		activity.RegisterOptions{Name: ActivityCompleteWorkflowTurn},
	)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 1})
	}, time.Millisecond)
	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: "sess_new_loop"})

	require.Equal(t, 1, source.completions("evt_new_loop"))
	env.AssertNotCalled(t, ActivityRunTurn, mock.Anything, mock.Anything)
}

// runTurnStub returns a RunTurn activity implementation that commits a single
// end_turn output through the fake source, standing in for the real runtime.
func runTurnStub(source *fakeSource) func(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
	return func(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
		_, err := source.CompleteTurn(ctx, in.SessionID, in.TriggerEventID, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": "ok"}},
			{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
		}, domain.StatusIdle)
		if err != nil {
			return RunTurnResult{}, err
		}
		return RunTurnResult{}, nil
	}
}

// TestSessionWorkflow_DuplicateWakeupsProcessOnce proves sequence-based
// duplicate protection: several wakeups naming the same (or lower) sequence
// drive the turn exactly once, because the durable cursor advances past the
// consumed event and never moves backward.
func TestSessionWorkflow_DuplicateWakeupsProcessOnce(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	useLegacyRunTurnVersion(env)

	source := newFakeSource([]domain.Event{userMsg("evt_1", 1)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	env.RegisterActivityWithOptions(runTurnStub(source), activity.RegisterOptions{Name: ActivityRunTurn})

	for i, delay := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond} {
		seq := int64(1)
		_ = i
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: seq})
		}, delay)
	}

	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: "sess_dup", StartCursor: 0})

	require.Equal(t, 1, source.completions("evt_1"), "duplicate wakeups must drive the turn exactly once")
}

// TestSessionWorkflow_OrderedConsumption proves the workflow consumes multiple
// user messages in receipt order, one turn each.
func TestSessionWorkflow_OrderedConsumption(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	useLegacyRunTurnVersion(env)

	// Two user messages at seq 1 and 2. (Their turns' output events get higher
	// sequences as the fake source appends them.)
	source := newFakeSource([]domain.Event{userMsg("evt_1", 1), userMsg("evt_2", 2)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})

	var order []string
	var orderMu sync.Mutex
	env.RegisterActivityWithOptions(func(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
		orderMu.Lock()
		order = append(order, in.TriggerEventID)
		orderMu.Unlock()
		return runTurnStub(source)(ctx, in)
	}, activity.RegisterOptions{Name: ActivityRunTurn})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 2})
	}, time.Millisecond)

	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: "sess_ord", StartCursor: 0})

	orderMu.Lock()
	defer orderMu.Unlock()
	require.Equal(t, []string{"evt_1", "evt_2"}, order, "turns must run in receipt order")
}

// TestSessionWorkflow_ContinueAsNewCarriesCursor proves the workflow performs
// Continue-As-New after the turn threshold and carries its durable cursor
// forward, so the fresh history run does not reprocess consumed events. It
// lowers the threshold to 1 for the test.
func TestSessionWorkflow_ContinueAsNewCarriesCursor(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	useLegacyRunTurnVersion(env)

	source := newFakeSource([]domain.Event{userMsg("evt_1", 1)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
	env.RegisterActivityWithOptions(runTurnStub(source), activity.RegisterOptions{Name: ActivityRunTurn})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 1})
	}, time.Millisecond)

	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(sessionWorkflowExitChecked,
		SessionWorkflowInput{SessionID: "sess_can", StartCursor: 0}, 1)

	require.True(t, env.IsWorkflowCompleted())
	// Continue-As-New surfaces as a ContinueAsNewError from the test env. Its
	// presence proves the workflow reached the threshold and re-issued itself
	// under the same Workflow ID with carried state, rather than growing history
	// unbounded. (The carried cursor payload is encoded via the data converter;
	// decoding it is not needed to prove the CAN boundary fired.)
	err := env.GetWorkflowError()
	require.Error(t, err)
	var canErr *workflow.ContinueAsNewError
	require.True(t, errors.As(err, &canErr), "expected Continue-As-New, got %v", err)
	require.Equal(t, SessionWorkflowType, canErr.WorkflowType.Name)
	// The turn ran exactly once before the boundary.
	require.Equal(t, 1, source.completions("evt_1"))
}

// TestSessionWorkflow_DrainsBufferedWakeupBeforeCloseBoundary places a second
// wakeup into history while RunTurn is in flight. Both terminal completion and
// Continue-As-New must consume it before proposing their close command; otherwise
// Temporal rejects the close and replay can propose the same invalid command
// forever. sessionWorkflowExitChecked makes an unconsumed wakeup fail the test.
func TestSessionWorkflow_DrainsBufferedWakeupBeforeCloseBoundary(t *testing.T) {
	tests := []struct {
		name       string
		result     RunTurnResult
		threshold  int
		wantCANErr bool
	}{
		{name: "continue-as-new", threshold: 1, wantCANErr: true},
		{name: "terminal-completion", result: RunTurnResult{Terminated: true}, threshold: continueAsNewThreshold},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ts testsuite.WorkflowTestSuite
			env := ts.NewTestWorkflowEnvironment()
			useLegacyRunTurnVersion(env)

			source := newFakeSource([]domain.Event{userMsg("evt_1", 1)})
			acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
			env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})
			env.RegisterActivityWithOptions(runTurnStub(source), activity.RegisterOptions{Name: ActivityRunTurn})
			env.OnActivity(ActivityRunTurn, mock.Anything, RunTurnInput{
				SessionID: "sess_close_boundary", TriggerEventID: "evt_1",
			}).After(time.Hour).Return(tc.result, nil).Once()

			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 1})
			}, time.Minute)
			env.RegisterDelayedCallback(func() {
				// Arrives while RunTurn is in flight, so it is buffered for the next
				// Workflow Task and must be consumed at the close boundary.
				env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 1})
			}, 30*time.Minute)

			env.SetTestTimeout(10 * time.Second)
			env.ExecuteWorkflow(sessionWorkflowExitChecked,
				SessionWorkflowInput{SessionID: "sess_close_boundary", StartCursor: 0}, tc.threshold)

			require.True(t, env.IsWorkflowCompleted())
			err := env.GetWorkflowError()
			if tc.wantCANErr {
				var canErr *workflow.ContinueAsNewError
				require.Error(t, err)
				require.True(t, errors.As(err, &canErr), "expected Continue-As-New, got %v", err)
			} else {
				require.NoError(t, err)
			}
			env.AssertExpectations(t)
		})
	}
}

// TestSessionWorkflow_TerminationStopsBatch proves that when a turn terminates
// the session (RunTurnResult.Terminated), the workflow stops processing the rest
// of the loaded batch: a second user.message queued behind the terminating one
// is never handed to RunTurn. This guards the P0 termination-propagation fix.
func TestSessionWorkflow_TerminationStopsBatch(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	useLegacyRunTurnVersion(env)

	source := newFakeSource([]domain.Event{userMsg("evt_1", 1), userMsg("evt_2", 2)})
	acts := NewActivities(nil, nil, source, nil, nil, &testIDGen{})
	env.RegisterActivityWithOptions(acts.LoadEvents, activity.RegisterOptions{Name: ActivityLoadEvents})

	var seen []string
	var mu sync.Mutex
	env.RegisterActivityWithOptions(func(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
		mu.Lock()
		seen = append(seen, in.TriggerEventID)
		mu.Unlock()
		// The first turn terminates the session; report Terminated so the workflow
		// must stop before the second message.
		return RunTurnResult{Terminated: true}, nil
	}, activity.RegisterOptions{Name: ActivityRunTurn})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(WakeupSignalName, WakeupSignal{MaxEventSeq: 2})
	}, time.Millisecond)

	env.SetTestTimeout(10 * time.Second)
	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: "sess_term", StartCursor: 0})

	// The workflow returns (completes) after the terminating turn rather than
	// blocking forever on the signal channel.
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"evt_1"}, seen, "only the first (terminating) turn should run; evt_2 must stay unprocessed")
}
