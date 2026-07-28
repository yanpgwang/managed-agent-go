// Package temporal implements the platform-spine's durable session
// orchestration: a minimal SessionWorkflow keyed by the public session ID, the
// Activities that invoke the existing agent runtime and commit authoritative
// completion through idempotent PostgreSQL operations, and the outbox relay that
// wakes the workflow with Signal-With-Start.
//
// This is the first bounded vertical slice. It routes one user.message end to
// end and does not cut over all traffic. PostgreSQL remains the source of truth
// for public events; Temporal owns only in-flight orchestration. Signals carry
// wakeup metadata only — never event payloads.
package temporal

const (
	// TaskQueue is the single task queue the session worker listens on for this
	// slice. Split queues (session vs thread vs webhook) are a later concern.
	TaskQueue = "managed-agent-session"

	// WakeupSignalName is the Signal the outbox relay (and the API fast path)
	// send to wake a SessionWorkflow. Its payload is metadata only.
	WakeupSignalName = "session-wakeup"

	// SessionWorkflowType is the registered workflow name. The Workflow ID is the
	// public session ID, so starting the same session twice is idempotent.
	SessionWorkflowType = "SessionWorkflow"
)

// WakeupSignal is the wakeup metadata delivered to a SessionWorkflow. It carries
// only the highest known public receipt sequence, never event payloads. The
// workflow loads authoritative events from PostgreSQL after its own durable
// cursor and ignores anything at or below it, so a duplicate or out-of-order
// signal cannot reorder public events or double-process a turn.
type WakeupSignal struct {
	MaxEventSeq int64 `json:"max_event_seq"`
}

// SessionWorkflowInput starts (or restarts, via Continue-As-New) a
// SessionWorkflow. StartCursor is the durable last-observed event sequence; a
// fresh session starts at 0, and Continue-As-New carries the current cursor
// forward so a new history run does not reprocess consumed events.
type SessionWorkflowInput struct {
	SessionID   string `json:"session_id"`
	StartCursor int64  `json:"start_cursor"`
}

// RunTurnInput asks the RunTurn Activity to execute one model turn for a trigger
// event and commit its authoritative output idempotently.
type RunTurnInput struct {
	SessionID      string `json:"session_id"`
	TriggerEventID string `json:"trigger_event_id"`
}

// RunTurnResult reports the outcome of a turn to the workflow. Terminated is true
// when the turn ended the session (an honest termination: ambiguous tool replay
// refusal or a misconfiguration); the workflow then stops processing the rest of
// the loaded batch and does not resurrect the session.
type RunTurnResult struct {
	Terminated bool `json:"terminated"`
}

// LoadEventsInput requests the ordered public events after a cursor.
type LoadEventsInput struct {
	SessionID string `json:"session_id"`
	Cursor    int64  `json:"cursor"`
	Limit     int    `json:"limit"`
}

// EventRef is the minimal projection of a public event the workflow needs to
// decide what to do. Payloads never enter workflow history; the Activity holds
// the authoritative event.
type EventRef struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	Type string `json:"type"`
}

// LoadEventsResult carries the ordered event references after the cursor.
type LoadEventsResult struct {
	Events []EventRef `json:"events"`
}
