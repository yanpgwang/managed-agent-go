package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	childPendingActionRoutingChangeID = "child-pending-action-routing"
	childPendingActionRoutingVersion  = 1
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
	pendingActionRouting := workflow.GetVersion(
		ctx,
		childPendingActionRoutingChangeID,
		workflow.DefaultVersion,
		childPendingActionRoutingVersion,
	) == childPendingActionRoutingVersion

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
			if pendingActionRouting {
				var pending LoadPendingActionsResult
				if err := workflow.ExecuteActivity(
					actx,
					ActivityLoadPendingActions,
					LoadPendingActionsInput{
						SessionID: in.SessionID, ThreadID: in.ThreadID,
					},
				).Get(actx, &pending); err != nil {
					return false, err
				}
				if len(pending.Actions) > 0 {
					trigger := EventRef{}
					resolutionIDs := make([]string, 0, len(pending.Actions))
					for _, action := range pending.Actions {
						if action.ResolutionEventID == "" {
							// The Thread remains parked until every action in this
							// model round has a client result.
							return false, nil
						}
						resolutionIDs = append(resolutionIDs, action.ResolutionEventID)
						if action.ResolutionEventSeq > trigger.Seq ||
							(action.ResolutionEventSeq == trigger.Seq &&
								action.ResolutionEventID > trigger.ID) {
							trigger = EventRef{
								ID: action.ResolutionEventID, Seq: action.ResolutionEventSeq,
							}
						}
					}
					completed, err := runWorkflowTurnInternal(
						actx, in.SessionID, trigger.ID, resolutionIDs, nil,
					)
					if err != nil {
						return false, err
					}
					turns++
					switch completed.Disposition {
					case TurnTerminated:
						return true, nil
					case TurnParked:
						return false, nil
					}
					// Completion may have installed a new barrier. Re-evaluate it
					// before consuming any queued follow-up message.
					continue
				}
			}

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
