package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// continueAsNewThreshold bounds how many turns one workflow history run drives
// before it carries its small cursor state into a fresh history via
// Continue-As-New.
const continueAsNewThreshold = 500

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
//   - State carried across Continue-As-New is small: just the cursor. Completed
//     model and tool Activity results do enter Workflow history (that is what
//     makes exact round replay possible), while PostgreSQL remains authoritative
//     for the public event ledger and projection.
func SessionWorkflow(ctx workflow.Context, in SessionWorkflowInput) error {
	return sessionWorkflow(ctx, in, continueAsNewThreshold)
}

// sessionWorkflow contains the implementation behind SessionWorkflow. The
// threshold is an argument so workflow tests can exercise Continue-As-New after
// one turn without a mutable package variable. Production always passes the
// compile-time constant above, keeping the value deterministic across replay.
func sessionWorkflow(ctx workflow.Context, in SessionWorkflowInput, canThreshold int) error {
	logger := workflow.GetLogger(ctx)
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			// Keep infrastructure work durable through an operator-recoverable
			// outage. Permanent application failures remain visible as a stuck
			// Activity and require intervention; silently exhausting retries would
			// strand an admitted turn just as surely.
			MaximumAttempts: 0,
		},
	}
	actx := workflow.WithActivityOptions(ctx, ao)
	agentLoopVersion := workflow.GetVersion(
		ctx,
		workflowAgentLoopChangeID,
		workflow.DefaultVersion,
		1,
	)

	cursor := in.StartCursor
	highestSignaled := cursor
	turnsThisRun := 0

	wakeupCh := workflow.GetSignalChannel(ctx, WakeupSignalName)
	// coalesceWakeups deterministically consumes every wakeup currently buffered
	// in Workflow history and retains only the highest sequence metadata. Temporal
	// rejects a close/Continue-As-New command when a Signal arrived during the
	// current Workflow Task. Consuming at both sides of Activity-driven draining
	// means that rejection replays into this code, consumes the now-visible Signal,
	// and makes progress instead of proposing the same close forever.
	coalesceWakeups := func() bool {
		sawSignal := false
		for {
			var sig WakeupSignal
			if ok := wakeupCh.ReceiveAsync(&sig); !ok {
				return sawSignal
			}
			sawSignal = true
			if sig.MaxEventSeq > highestSignaled {
				highestSignaled = sig.MaxEventSeq
			}
		}
	}

	// drain applies every currently-available event after the cursor, driving one
	// turn per user.message and advancing the cursor past each turn's own output.
	// It sets `terminated` and returns early when a turn ended the session, so the
	// caller can stop the whole workflow rather than just this drain pass.
	terminated := false
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
					var res RunTurnResult
					if agentLoopVersion == workflow.DefaultVersion {
						// Replay compatibility for Workflow histories created
						// before the Workflow-owned loop was introduced.
						if err := workflow.ExecuteActivity(actx, ActivityRunTurn, RunTurnInput{
							SessionID:      in.SessionID,
							TriggerEventID: e.ID,
						}).Get(actx, &res); err != nil {
							return err
						}
					} else {
						var err error
						res, err = runWorkflowTurn(actx, in.SessionID, e.ID)
						if err != nil {
							return err
						}
					}
					turnsThisRun++
					// A turn that terminated the session ends orchestration: stop
					// draining the rest of the loaded batch. Later queued user.message
					// events stay unprocessed and the session stays terminated — the
					// workflow must never resurrect a terminated session by processing a
					// message queued behind the terminating one. Signal the caller to
					// end the whole workflow (a bare return here would only end this
					// drain pass and the workflow would block on the next wakeup).
					if res.Terminated {
						logger.Info("session terminated by turn; stopping",
							"session_id", in.SessionID, "trigger", e.ID)
						terminated = true
						return nil
					}
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
		// On a normal iteration this picks up a burst before any database work. On
		// replay after Temporal rejected a close/CAN due to a buffered Signal, this
		// is what consumes that Signal before we propose the boundary again.
		coalesceWakeups()

		// Process everything already committed up to the highest wakeup we have
		// seen. Loading from PostgreSQL (not the signal) is what makes a gap — a
		// signal that names a sequence not yet visible — self-correcting: we apply
		// what exists and wait for the next wakeup to bring the rest.
		if highestSignaled > cursor {
			if err := drain(); err != nil {
				return err
			}
		}

		// A Signal may have arrived while an Activity was running. Consume it before
		// either terminal completion or Continue-As-New. If it advances the known
		// sequence, loop once more so PostgreSQL is drained before CAN. A signal that
		// races after this check is still safe: Temporal rejects the close command
		// and replay consumes it at this same deterministic boundary.
		sawSignalDuringDrain := coalesceWakeups()

		// A turn terminated the session: end the workflow. Any events still queued
		// behind the terminating message stay unprocessed and the session stays
		// terminated. Wakeups are consumed above but deliberately do not resurrect
		// the session or drive the queued events.
		if terminated {
			return nil
		}

		if sawSignalDuringDrain && highestSignaled > cursor {
			continue
		}

		// Continue-As-New with the small cursor once this history run has driven
		// enough turns. Draining first guarantees we do not strand already-visible
		// work across the boundary.
		info := workflow.GetInfo(ctx)
		serverSuggestsContinueAsNew :=
			agentLoopVersion != workflow.DefaultVersion && info.GetContinueAsNewSuggested()
		if turnsThisRun >= canThreshold || serverSuggestsContinueAsNew {
			logger.Info(
				"continue-as-new",
				"session_id", in.SessionID,
				"cursor", cursor,
				"turns", turnsThisRun,
				"history_length", info.GetCurrentHistoryLength(),
				"history_size", info.GetCurrentHistorySize(),
			)
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
	}
}
