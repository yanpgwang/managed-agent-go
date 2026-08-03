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
	return w.executeActivityWithPulse(activityName, input, output, 0, nil)
}

func (w *turnInterruptWatcher) executeActivityWithPulse(
	activityName string,
	input any,
	output any,
	pulseInterval time.Duration,
	pulse func() error,
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
	var activityErr error
	for {
		// Prefer an already-recorded Activity result. PostgreSQL completion still
		// performs the authoritative finish-vs-interrupt lock race afterward.
		if future.IsReady() {
			activityErr = future.Get(w.actx, output)
			return interruptibleActivityOutcome{Completed: activityErr == nil}, activityErr
		}

		selector := workflow.NewSelector(w.actx)
		activityReady := false
		wakeupReady := false
		pulseReady := false
		activityErr = nil
		selector.AddFuture(future, func(f workflow.Future) {
			activityReady = true
			activityErr = f.Get(w.actx, output)
		})
		selector.AddReceive(w.wakeupCh, func(ch workflow.ReceiveChannel, _ bool) {
			var signal WakeupSignal
			ch.Receive(w.actx, &signal)
			wakeupReady = true
		})
		timerCtx, cancelTimer := workflow.WithCancel(w.actx)
		if pulse != nil && pulseInterval > 0 {
			timer := workflow.NewTimer(timerCtx, pulseInterval)
			selector.AddFuture(timer, func(workflow.Future) { pulseReady = true })
		}
		selector.Select(w.actx)
		if future.IsReady() {
			cancelTimer()
			if !activityReady {
				activityErr = future.Get(w.actx, output)
			}
			return interruptibleActivityOutcome{Completed: activityErr == nil}, activityErr
		}
		if pulseReady {
			cancelTimer()
			if err := pulse(); err != nil {
				cancel()
				_ = future.Get(w.actx, output)
				return interruptibleActivityOutcome{}, err
			}
			continue
		}
		cancelTimer()
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

// wait sleeps for a Workflow retry delay while preserving the same durable
// interrupt semantics used by long-running Activities.
func (w *turnInterruptWatcher) wait(delay time.Duration) (bool, error) {
	pending, err := loadInterruptAfter(w.actx, w.sessionID, w.afterSeq)
	if err != nil {
		return false, err
	}
	if pending.Interrupt != nil {
		return true, nil
	}
	deadline := workflow.Now(w.actx).Add(delay)
	for {
		remaining := deadline.Sub(workflow.Now(w.actx))
		if remaining <= 0 {
			return false, nil
		}
		timerCtx, cancelTimer := workflow.WithCancel(w.actx)
		timer := workflow.NewTimer(timerCtx, remaining)
		timerReady := false
		wakeupReady := false
		selector := workflow.NewSelector(w.actx)
		selector.AddFuture(timer, func(workflow.Future) { timerReady = true })
		selector.AddReceive(w.wakeupCh, func(ch workflow.ReceiveChannel, _ bool) {
			var signal WakeupSignal
			ch.Receive(w.actx, &signal)
			wakeupReady = true
		})
		selector.Select(w.actx)
		if timerReady {
			cancelTimer()
			pending, err := loadInterruptAfter(w.actx, w.sessionID, w.afterSeq)
			if err != nil {
				return false, err
			}
			return pending.Interrupt != nil, nil
		}
		cancelTimer()
		if !wakeupReady {
			continue
		}
		pending, err := loadInterruptAfter(w.actx, w.sessionID, w.afterSeq)
		if err != nil {
			return false, err
		}
		if pending.Interrupt != nil {
			return true, nil
		}
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
