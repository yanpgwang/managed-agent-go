package temporal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

type capturingWorkflowClient struct {
	client.Client
	options  client.StartWorkflowOptions
	workflow interface{}
}

func (*capturingWorkflowClient) TerminateWorkflow(
	context.Context,
	string,
	string,
	string,
	...interface{},
) error {
	return nil
}

func (c *capturingWorkflowClient) ExecuteWorkflow(
	_ context.Context,
	options client.StartWorkflowOptions,
	workflow interface{},
	_ ...interface{},
) (client.WorkflowRun, error) {
	c.options = options
	c.workflow = workflow
	return completedWorkflowRun{id: options.ID}, nil
}

type completedWorkflowRun struct {
	id string
}

func (r completedWorkflowRun) GetID() string  { return r.id }
func (completedWorkflowRun) GetRunID() string { return "run" }
func (completedWorkflowRun) Get(context.Context, interface{}) error {
	return nil
}
func (completedWorkflowRun) GetWithOptions(
	context.Context,
	interface{},
	client.WorkflowRunGetOptions,
) error {
	return nil
}

func TestTerminateSession_CleanupJoinsRunningWorkflowAndAllowsRetry(t *testing.T) {
	c := &capturingWorkflowClient{}
	signaler := NewSignalerOnTaskQueue(c, "sandbox-cleanup-test")

	require.NoError(t, signaler.TerminateSession(context.Background(), "sesn_delete"))
	require.Equal(t, "sandbox-cleanup:sesn_delete", c.options.ID)
	require.Equal(t, "sandbox-cleanup-test", c.options.TaskQueue)
	require.Equal(
		t,
		enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		c.options.WorkflowIDConflictPolicy,
	)
	require.Equal(
		t,
		enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		c.options.WorkflowIDReusePolicy,
	)
	require.Equal(t, SandboxCleanupWorkflowType, c.workflow)
}

func TestNewSignaler_CleanupUsesDefaultTaskQueue(t *testing.T) {
	c := &capturingWorkflowClient{}
	require.NoError(t, NewSignaler(c).TerminateSession(context.Background(), "sesn_default"))
	require.Equal(t, TaskQueue, c.options.TaskQueue)
}
