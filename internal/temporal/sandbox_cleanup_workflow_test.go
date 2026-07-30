package temporal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

func TestSandboxCleanupWorkflow_ReleasesSessionSandbox(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	released := ""
	env.RegisterActivityWithOptions(
		func(_ context.Context, in ReleaseSandboxInput) error {
			released = in.SessionID
			return nil
		},
		activity.RegisterOptions{Name: ActivityReleaseSandbox},
	)

	env.ExecuteWorkflow(
		SandboxCleanupWorkflow,
		ReleaseSandboxInput{SessionID: "sesn_cleanup"},
	)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "sesn_cleanup", released)
}

func TestSandboxCleanupWorkflow_RetriesTransientReleaseFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	attempts := 0
	env.RegisterActivityWithOptions(
		func(context.Context, ReleaseSandboxInput) error {
			attempts++
			if attempts < 3 {
				return errors.New("provider temporarily unavailable")
			}
			return nil
		},
		activity.RegisterOptions{Name: ActivityReleaseSandbox},
	)

	env.ExecuteWorkflow(
		SandboxCleanupWorkflow,
		ReleaseSandboxInput{SessionID: "sesn_retry"},
	)

	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 3, attempts)
}

type permanentReleaseLease struct{}

func (permanentReleaseLease) Acquire(
	context.Context,
	string,
	sandbox.Spec,
) (sandbox.Sandbox, error) {
	return nil, nil
}

func (permanentReleaseLease) Release(context.Context, string) error {
	return sandbox.Permanent(errors.New("sandbox belongs to another provider"))
}

func TestReleaseSandbox_MapsPermanentProviderErrorToNonRetryable(t *testing.T) {
	activities := NewActivities(nil, nil, nil, permanentReleaseLease{}, nil)
	err := activities.ReleaseSandbox(
		context.Background(),
		ReleaseSandboxInput{SessionID: "sesn_permanent"},
	)
	var applicationError *temporalsdk.ApplicationError
	require.ErrorAs(t, err, &applicationError)
	require.True(t, applicationError.NonRetryable())
	require.Equal(t, sandboxPermanentErrorType, applicationError.Type())
}
