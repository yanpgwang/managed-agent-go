package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SandboxCleanupWorkflow durably releases a session's provider resource and
// persisted binding. It runs only after the public deletion fence is committed
// and the long-lived SessionWorkflow has been terminated.
func SandboxCleanupWorkflow(ctx workflow.Context, in ReleaseSandboxInput) error {
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    0,
		},
	})
	return workflow.ExecuteActivity(
		actx,
		ActivityReleaseSandbox,
		in,
	).Get(actx, nil)
}
