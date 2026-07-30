package temporal

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	durableInterruptChangeID = "durable-cross-process-interrupt"
	durableInterruptVersion  = 1

	// Long model/tool Activities heartbeat every 500ms. This timeout is stored
	// in new Workflow histories and bounds how quickly Temporal can deliver a
	// cancellation request through a heartbeat response.
	interruptActivityHeartbeatTimeout = 3 * time.Second
)

type interruptibleActivityOutcome struct {
	Interrupted bool
	Completed   bool
}

// turnInterruptWatcher keeps public event payloads out of Workflow Signals.
// A wakeup only prompts a small Activity read of PostgreSQL; cancellation starts
// only after that read proves a durable, unprocessed interrupt exists after the
// active trigger.
type turnInterruptWatcher struct {
	actx              workflow.Context
	interruptibleActx workflow.Context
	wakeupCh          workflow.ReceiveChannel
	sessionID         string
	afterSeq          int64
	preflightDone     bool
}

func newTurnInterruptWatcher(
	actx workflow.Context,
	wakeupCh workflow.ReceiveChannel,
	sessionID string,
	afterSeq int64,
) *turnInterruptWatcher {
	interruptibleActx := workflow.WithHeartbeatTimeout(
		actx,
		interruptActivityHeartbeatTimeout,
	)
	interruptibleActx = workflow.WithWaitForCancellation(
		interruptibleActx,
		true,
	)
	return &turnInterruptWatcher{
		actx:              actx,
		interruptibleActx: interruptibleActx,
		wakeupCh:          wakeupCh,
		sessionID:         sessionID,
		afterSeq:          afterSeq,
	}
}

func (w *turnInterruptWatcher) executeActivity(
	activityName string,
	input any,
	output any,
) (interruptibleActivityOutcome, error) {
	// A Signal-With-Start wakeup can already have been coalesced by the outer
	// Workflow loop before this turn begins. Check PostgreSQL once before the
	// turn's first model/tool Activity so an already-durable interrupt cannot be
	// stranded waiting for another Signal that may never arrive.
	if !w.preflightDone {
		w.preflightDone = true
		pending, err := loadInterruptAfter(w.actx, w.sessionID, w.afterSeq)
		if err != nil {
			return interruptibleActivityOutcome{}, err
		}
		if pending.Interrupt != nil {
			return interruptibleActivityOutcome{Interrupted: true}, nil
		}
	}

	cctx, cancel := workflow.WithCancel(w.interruptibleActx)
	defer cancel()

	future := workflow.ExecuteActivity(cctx, activityName, input)
	selector := workflow.NewSelector(w.actx)
	activityReady := false
	wakeupReady := false
	var activityErr error
	selector.AddFuture(future, func(f workflow.Future) {
		activityReady = true
		activityErr = f.Get(w.actx, output)
	})
	selector.AddReceive(w.wakeupCh, func(ch workflow.ReceiveChannel, _ bool) {
		var signal WakeupSignal
		ch.Receive(w.actx, &signal)
		wakeupReady = true
	})

	for {
		// Prefer an already-recorded Activity result. PostgreSQL completion still
		// performs the authoritative finish-vs-interrupt lock race afterward.
		if future.IsReady() {
			activityErr = future.Get(w.actx, output)
			return interruptibleActivityOutcome{Completed: activityErr == nil}, activityErr
		}

		wakeupReady = false
		selector.Select(w.actx)
		if activityReady {
			return interruptibleActivityOutcome{Completed: activityErr == nil}, activityErr
		}
		if !wakeupReady {
			continue
		}

		pending, err := loadInterruptAfter(w.actx, w.sessionID, w.afterSeq)
		if err != nil {
			return interruptibleActivityOutcome{}, err
		}
		if pending.Interrupt == nil {
			continue
		}

		cancel()
		// WaitForCancellation keeps the Workflow here until the model/tool
		// Activity has acknowledged cancellation. A successful result that won a
		// very close race remains available to the caller and may be committed as
		// authoritative partial output. Any error after the requested
		// cancellation is classified by the PostgreSQL attempt fence, not retried
		// as fresh side-effecting work.
		err = future.Get(w.actx, output)
		return interruptibleActivityOutcome{
			Interrupted: true,
			Completed:   err == nil,
		}, nil
	}
}

func loadInterruptAfter(
	actx workflow.Context,
	sessionID string,
	afterSeq int64,
) (LoadInterruptResult, error) {
	var result LoadInterruptResult
	err := workflow.ExecuteActivity(
		actx,
		ActivityLoadInterrupt,
		LoadInterruptInput{SessionID: sessionID, AfterSeq: afterSeq},
	).Get(actx, &result)
	return result, err
}

// acknowledgeIdleInterrupt processes an interrupt encountered when no turn is
// active. PostgreSQL keeps an existing requires_action barrier idle, keeps a
// queued redirect running, and otherwise leaves an idle session idle. No model
// call or duplicate terminal event is produced.
func acknowledgeIdleInterrupt(
	actx workflow.Context,
	sessionID string,
	interruptEventID string,
) (RunTurnResult, error) {
	var result RunTurnResult
	err := workflow.ExecuteActivity(
		actx,
		ActivityCompleteWorkflowTurn,
		CompleteWorkflowTurnInput{
			SessionID:      sessionID,
			TriggerEventID: interruptEventID,
			Status:         domain.StatusIdle,
		},
	).Get(actx, &result)
	return result, err
}
