package temporal

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func interruptibleActivityHarness(
	ctx workflow.Context,
) (interruptibleActivityOutcome, error) {
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporalsdk.RetryPolicy{
			MaximumAttempts: 1,
		},
	})
	watcher := newTurnInterruptWatcher(
		actx,
		workflow.GetSignalChannel(ctx, WakeupSignalName),
		"sess_interrupt",
		1,
	)
	var ignored string
	return watcher.executeActivity("BlockingActivity", struct{}{}, &ignored)
}

func TestTurnInterruptWatcher_RequestsHeartbeatActivityCancellation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(interruptibleActivityHarness)

	release := make(chan struct{})
	defer close(release)
	env.RegisterActivityWithOptions(
		func(ctx context.Context) (string, error) {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				activity.RecordHeartbeat(ctx)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-release:
					return "", nil
				case <-ticker.C:
				}
			}
		},
		activity.RegisterOptions{Name: "BlockingActivity"},
	)
	var interruptReads atomic.Int32
	env.RegisterActivityWithOptions(
		func(context.Context, LoadInterruptInput) (LoadInterruptResult, error) {
			if interruptReads.Add(1) == 1 {
				return LoadInterruptResult{}, nil
			}
			return LoadInterruptResult{Interrupt: &EventRef{
				ID: "sevt_interrupt", Seq: 2, Type: "user.interrupt",
			}}, nil
		},
		activity.RegisterOptions{Name: ActivityLoadInterrupt},
	)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(
			WakeupSignalName,
			WakeupSignal{MaxEventSeq: 2},
		)
	}, time.Millisecond)

	env.SetTestTimeout(5 * time.Second)
	env.ExecuteWorkflow(interruptibleActivityHarness)

	require.NoError(t, env.GetWorkflowError())
	var outcome interruptibleActivityOutcome
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.True(t, outcome.Interrupted)
	require.False(t, outcome.Completed)
}

func TestTurnInterruptWatcher_PreflightFindsAlreadyDurableInterrupt(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(interruptibleActivityHarness)

	var activityCalls atomic.Int32
	env.RegisterActivityWithOptions(
		func(context.Context) (string, error) {
			activityCalls.Add(1)
			return "must not run", nil
		},
		activity.RegisterOptions{Name: "BlockingActivity"},
	)
	env.RegisterActivityWithOptions(
		func(context.Context, LoadInterruptInput) (LoadInterruptResult, error) {
			return LoadInterruptResult{Interrupt: &EventRef{
				ID: "sevt_interrupt", Seq: 2, Type: "user.interrupt",
			}}, nil
		},
		activity.RegisterOptions{Name: ActivityLoadInterrupt},
	)

	env.SetTestTimeout(5 * time.Second)
	env.ExecuteWorkflow(interruptibleActivityHarness)

	require.NoError(t, env.GetWorkflowError())
	var outcome interruptibleActivityOutcome
	require.NoError(t, env.GetWorkflowResult(&outcome))
	require.True(t, outcome.Interrupted)
	require.False(t, outcome.Completed)
	require.Zero(t, activityCalls.Load())
}
