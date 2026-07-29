package temporal

import (
	"context"
	"errors"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// Signaler delivers a wakeup to a SessionWorkflow using Signal-With-Start, so a
// wakeup for a session with no live workflow starts one idempotently (Workflow
// ID = session ID) and a wakeup for a running one simply signals it.
//
// This is the single delivery primitive used by both the API's post-commit fast
// path and the outbox relay. Duplicate delivery is harmless: the workflow reads
// events by PostgreSQL receipt sequence after its durable cursor and ignores
// anything already observed.
type Signaler struct {
	client    client.Client
	taskQueue string
}

func NewSignaler(c client.Client) *Signaler {
	return NewSignalerOnTaskQueue(c, TaskQueue)
}

func NewSignalerOnTaskQueue(c client.Client, taskQueue string) *Signaler {
	if taskQueue == "" {
		taskQueue = TaskQueue
	}
	return &Signaler{client: c, taskQueue: taskQueue}
}

// Wake starts-or-signals the SessionWorkflow for a session, delivering wakeup
// metadata (the highest known receipt sequence) only. The workflow's start
// cursor is 0 on first start; the durable cursor then advances as it consumes
// PostgreSQL.
func (s *Signaler) Wake(ctx context.Context, sessionID string, maxEventSeq int64) error {
	opts := client.StartWorkflowOptions{
		ID:        sessionID,
		TaskQueue: s.taskQueue,
	}
	_, err := s.client.SignalWithStartWorkflow(
		ctx,
		sessionID,
		WakeupSignalName,
		WakeupSignal{MaxEventSeq: maxEventSeq},
		opts,
		SessionWorkflow,
		SessionWorkflowInput{SessionID: sessionID, StartCursor: 0},
	)
	return err
}

// TerminateSession stops the live Workflow execution for a session before its
// PostgreSQL projection is physically deleted. A session that never started a
// Workflow is already stopped, so Temporal NotFound is idempotent success.
func (s *Signaler) TerminateSession(ctx context.Context, sessionID string) error {
	err := s.client.TerminateWorkflow(
		ctx,
		sessionID,
		"",
		"session deleted through the public API",
	)
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}
