package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/model"
	"github.com/yanpgwang/mango/internal/sandbox"
)

func TestPrepareTurn_EnablesSessionOutputsWhenPublisherIsAvailable(t *testing.T) {
	source := newFakeSource([]domain.Event{userMsg("sevt_trigger", 1)})
	publisher := &recordingSessionOutputPublisher{supported: true}
	activities := NewActivities(nil, source, nil, nil, &testIDGen{}).
		WithSessionOutputPublisher(publisher)

	prepared, err := activities.PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID: "sesn_1", TriggerEventID: "sevt_trigger",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.SessionOutputsEnabled {
		t.Fatal("PrepareTurn did not record the Session output capability")
	}
}

func TestPublishSessionOutputs_AttachesExistingSandboxWithoutProvisioning(t *testing.T) {
	source := newFakeSource(nil)
	box := &activityOutputSandbox{}
	lease := &existingOutputLease{box: box, found: true}
	publisher := &recordingSessionOutputPublisher{supported: true}
	activities := NewActivities(nil, source, nil, lease, &testIDGen{}).
		WithSessionOutputPublisher(publisher)

	result, err := activities.PublishSessionOutputs(
		context.Background(), PublishSessionOutputsInput{SessionID: "sesn_1"},
	)
	if err != nil || result.FatalError != "" {
		t.Fatalf("PublishSessionOutputs = %+v, %v", result, err)
	}
	if lease.acquireCalls != 0 || lease.existingCalls != 1 {
		t.Fatalf(
			"lease Acquire calls=%d AcquireExisting calls=%d",
			lease.acquireCalls, lease.existingCalls,
		)
	}
	if publisher.calls != 1 || publisher.box != box {
		t.Fatalf("publisher calls=%d box=%p", publisher.calls, publisher.box)
	}
}

func TestWorkflowTurn_PublishesOutputsBeforeIdleCommit(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	var mu sync.Mutex
	order := make([]string, 0, 3)
	record := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}
	registerWorkflowTurnActivities(
		env,
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{
				SessionOutputsEnabled: true,
				Request:               model.Request{Model: "test-model"},
			}, nil
		},
		func(_ context.Context, in CallModelInput) (CallModelResult, error) {
			record("model")
			return CallModelResult{
				ModelRequestStartID: in.ModelRequestStartID,
				ModelRequestEndID:   in.ModelRequestEndID,
				MessageEventID:      "sevt_answer",
				Response: model.Response{
					StopReason: "end_turn",
					Content:    []domain.ContentBlock{{Type: "text", Text: "done"}},
				},
			}, nil
		},
		func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
			return ExecuteToolResult{}, errors.New("unexpected tool")
		},
		func(context.Context, CompleteWorkflowTurnInput) (RunTurnResult, error) {
			record("complete")
			return RunTurnResult{Disposition: TurnCompleted}, nil
		},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, PublishSessionOutputsInput) (PublishSessionOutputsResult, error) {
			record("outputs")
			return PublishSessionOutputsResult{}, nil
		},
		activity.RegisterOptions{Name: ActivityPublishSessionOutputs},
	)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sesn_1", TriggerEventID: "sevt_trigger",
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"model", "outputs", "complete"}, order)
}

type recordingSessionOutputPublisher struct {
	supported bool
	calls     int
	box       sandbox.Sandbox
}

func (p *recordingSessionOutputPublisher) SupportsSessionOutputs() bool {
	return p.supported
}

func (p *recordingSessionOutputPublisher) PublishSessionOutputs(
	_ context.Context,
	_ string,
	box sandbox.Sandbox,
) error {
	p.calls++
	p.box = box
	return nil
}

type existingOutputLease struct {
	box           sandbox.Sandbox
	found         bool
	acquireCalls  int
	existingCalls int
}

func (l *existingOutputLease) Acquire(
	context.Context,
	string,
	sandbox.Spec,
) (sandbox.Sandbox, error) {
	l.acquireCalls++
	return nil, errors.New("Acquire must not be called")
}

func (l *existingOutputLease) AcquireExisting(
	context.Context,
	string,
	sandbox.Spec,
) (sandbox.Sandbox, bool, error) {
	l.existingCalls++
	return l.box, l.found, nil
}

func (*existingOutputLease) Release(context.Context, string) error { return nil }

type activityOutputSandbox struct{}

func (*activityOutputSandbox) Exec(context.Context, sandbox.Command) (*sandbox.Result, error) {
	return nil, errors.New("not implemented")
}
func (*activityOutputSandbox) ReadFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (*activityOutputSandbox) WriteFile(context.Context, string, []byte) error {
	return errors.New("not implemented")
}
func (*activityOutputSandbox) Root() string                  { return "/workspace" }
func (*activityOutputSandbox) Destroy(context.Context) error { return nil }
