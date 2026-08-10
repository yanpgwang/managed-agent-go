package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// SessionThreadWorkflow is the independent durable loop for one child Agent.
// PostgreSQL owns its Thread identity and ledger; the Workflow owns only the
// in-flight cursor and model/tool command sequence.
func SessionThreadWorkflow(
	ctx workflow.Context,
	in SessionThreadWorkflowInput,
) error {
	return sessionThreadWorkflow(ctx, in, continueAsNewThreshold)
}

func sessionThreadWorkflow(
	ctx workflow.Context,
	in SessionThreadWorkflowInput,
	continueThreshold int,
) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second, BackoffCoefficient: 2,
			MaximumInterval: time.Minute, MaximumAttempts: 0,
		},
	}
	actx := workflow.WithActivityOptions(ctx, ao)
	wakeupCh := workflow.GetSignalChannel(ctx, WakeupSignalName)
	cursor := in.StartCursor
	turns := 0

	coalesce := func() bool {
		saw := false
		for {
			var signal WakeupSignal
			if !wakeupCh.ReceiveAsync(&signal) {
				return saw
			}
			saw = true
		}
	}
	drain := func() (bool, error) {
		for {
			var loaded LoadEventsResult
			if err := workflow.ExecuteActivity(
				actx,
				ActivityLoadEvents,
				LoadEventsInput{
					SessionID: in.SessionID, ThreadID: in.ThreadID,
					Cursor: cursor, Limit: loadBatchLimit,
				},
			).Get(actx, &loaded); err != nil {
				return false, err
			}
			if len(loaded.Events) == 0 {
				return false, nil
			}
			for _, event := range loaded.Events {
				if event.Seq <= cursor {
					continue
				}
				if event.Type == domain.EvAgentThreadMessageReceived {
					completed, err := runWorkflowTurnInternal(
						actx, in.SessionID, event.ID, nil, nil,
					)
					if err != nil {
						return false, err
					}
					turns++
					if completed.Disposition == TurnTerminated {
						return true, nil
					}
				}
				cursor = event.Seq
			}
		}
	}

	drainRequested := false
	for {
		if coalesce() {
			drainRequested = true
		}
		if drainRequested {
			terminated, err := drain()
			if err != nil {
				return err
			}
			if terminated {
				return nil
			}
		}
		if coalesce() {
			drainRequested = true
			continue
		}
		info := workflow.GetInfo(ctx)
		if turns >= continueThreshold || info.GetContinueAsNewSuggested() {
			return workflow.NewContinueAsNewError(
				ctx,
				SessionThreadWorkflow,
				SessionThreadWorkflowInput{
					SessionID: in.SessionID, ThreadID: in.ThreadID,
					StartCursor: cursor,
				},
			)
		}
		wakeupCh.Receive(ctx, nil)
		drainRequested = true
	}
}

func sessionThreadWorkflowID(threadID string) string {
	return "session-thread:" + threadID
}
