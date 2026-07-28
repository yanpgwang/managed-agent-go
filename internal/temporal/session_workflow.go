package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// continueAsNewThreshold bounds how many turns one workflow history run drives
// before it carries its small cursor state into a fresh history via
// Continue-As-New. It is a package var (not a const) only so tests can lower it;
// it is read once per history run and never changes during a run, so it does not
// affect determinism.
var continueAsNewThreshold = 500

// loadBatchLimit bounds how many event references one LoadEvents call returns.
const loadBatchLimit = 100

// SessionWorkflow is the durable, long-lived orchestrator for one session. Its
// Workflow ID is the public session ID, so Signal-With-Start is idempotent.
//
// Design invariants:
//   - PostgreSQL is the event source of truth. Signals are wakeups carrying only
//     the highest known receipt sequence; the workflow loads authoritative events
//     after its own durable cursor.
//   - The durable cursor advances monotonically. A duplicate or out-of-order
//     wakeup whose sequence is at or below the cursor is a harmless no-op
//     (sequence-based duplicate/gap protection).
//   - State carried across Continue-As-New is small: just the cursor. Large model
//     and tool payloads never travel through workflow history — Activities persist
//     them to PostgreSQL and return references.
func SessionWorkflow(ctx workflow.Context, in SessionWorkflowInput) error {
	logger := workflow.GetLogger(ctx)
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    0, // unbounded; the workflow owns higher-level giving up
		},
	}
	actx := workflow.WithActivityOptions(ctx, ao)

	cursor := in.StartCursor
	highestSignaled := cursor
	turnsThisRun := 0

	wakeupCh := workflow.GetSignalChannel(ctx, WakeupSignalName)

	// drain applies every currently-available event after the cursor, driving one
	// turn per user.message and advancing the cursor past each turn's own output.
	drain := func() error {
		for {
			var loaded LoadEventsResult
			if err := workflow.ExecuteActivity(actx, ActivityLoadEvents, LoadEventsInput{
				SessionID: in.SessionID,
				Cursor:    cursor,
				Limit:     loadBatchLimit,
			}).Get(actx, &loaded); err != nil {
				return err
			}
			if len(loaded.Events) == 0 {
				return nil
			}
			for _, e := range loaded.Events {
				// Sequence-based duplicate protection: never move the cursor
				// backward, and never reprocess an event at or below it.
				if e.Seq <= cursor {
					continue
				}
				if e.Type == domain.EvUserMessage {
					if err := workflow.ExecuteActivity(actx, ActivityRunTurn, RunTurnInput{
						SessionID:      in.SessionID,
						TriggerEventID: e.ID,
					}).Get(actx, nil); err != nil {
						return err
					}
					turnsThisRun++
					// Advance the cursor only to the trigger's own sequence, NOT to
					// the turn's output sequence. The turn's agent.message /
					// session.status_idle events take higher sequences than any input
					// admitted before the turn ran, so jumping the cursor to the output
					// would skip a lower-sequence user.message queued behind this one.
					// Those output events are re-loaded on the next iteration and
					// skipped there (they are not user.message), so the cursor still
					// ends up past them without stranding earlier input.
				}
				cursor = e.Seq
			}
		}
	}

	for {
		// Process everything already committed up to the highest wakeup we have
		// seen. Loading from PostgreSQL (not the signal) is what makes a gap — a
		// signal that names a sequence not yet visible — self-correcting: we apply
		// what exists and wait for the next wakeup to bring the rest.
		if highestSignaled > cursor {
			if err := drain(); err != nil {
				return err
			}
		}

		// Continue-As-New with the small cursor once this history run has driven
		// enough turns. Draining first guarantees we do not strand already-visible
		// work across the boundary.
		if turnsThisRun >= continueAsNewThreshold {
			logger.Info("continue-as-new", "session_id", in.SessionID, "cursor", cursor)
			return workflow.NewContinueAsNewError(ctx, SessionWorkflow, SessionWorkflowInput{
				SessionID:   in.SessionID,
				StartCursor: cursor,
			})
		}

		// Block for the next wakeup. A wakeup carries only metadata; we track the
		// highest sequence any signal has named and coalesce a burst by draining
		// the channel non-blockingly before looping.
		var sig WakeupSignal
		wakeupCh.Receive(ctx, &sig)
		if sig.MaxEventSeq > highestSignaled {
			highestSignaled = sig.MaxEventSeq
		}
		for {
			var more WakeupSignal
			if ok := wakeupCh.ReceiveAsync(&more); !ok {
				break
			}
			if more.MaxEventSeq > highestSignaled {
				highestSignaled = more.MaxEventSeq
			}
		}
	}
}
