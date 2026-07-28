package temporal

import (
	"context"

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
	client client.Client
}

func NewSignaler(c client.Client) *Signaler { return &Signaler{client: c} }

// Wake starts-or-signals the SessionWorkflow for a session, delivering wakeup
// metadata (the highest known receipt sequence) only. The workflow's start
// cursor is 0 on first start; the durable cursor then advances as it consumes
// PostgreSQL.
func (s *Signaler) Wake(ctx context.Context, sessionID string, maxEventSeq int64) error {
	opts := client.StartWorkflowOptions{
		ID:        sessionID,
		TaskQueue: TaskQueue,
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
