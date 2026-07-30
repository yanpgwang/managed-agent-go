// Package temporal implements the platform-spine's durable session
// orchestration: a SessionWorkflow keyed by the public session ID, granular
// model/tool Activities behind a Workflow-owned agent loop, idempotent
// PostgreSQL completion, and the outbox relay that wakes the workflow with
// Signal-With-Start.
//
// PostgreSQL remains the source of truth for public events; Temporal owns only
// in-flight orchestration. Signals carry wakeup metadata only — never event
// payloads.
package temporal

import (
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

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

	// SandboxCleanupWorkflowType is a short durable teardown workflow started
	// after the long-lived SessionWorkflow has been stopped for public deletion.
	SandboxCleanupWorkflowType = "SandboxCleanupWorkflow"
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

type ReleaseSandboxInput struct {
	SessionID string `json:"session_id"`
}

// RunTurnResult reports whether the workflow-owned turn completed, parked on a
// client-action barrier, or terminated the session.
type RunTurnResult struct {
	Disposition TurnDisposition `json:"disposition"`
}

// TurnDisposition tells the SessionWorkflow whether a completed turn finished,
// parked on a newly committed client-action barrier, or terminated the session.
type TurnDisposition string

const (
	TurnCompleted  TurnDisposition = "completed"
	TurnParked     TurnDisposition = "parked"
	TurnTerminated TurnDisposition = "terminated"
)

// TurnToolKind is the execution owner recorded by PrepareTurn. The Workflow
// consumes this durable Activity result rather than consulting a mutable
// process-global registry while replaying.
type TurnToolKind string

const (
	TurnToolBuiltin TurnToolKind = "builtin"
	TurnToolCustom  TurnToolKind = "custom"
)

// TurnTool is the immutable, Workflow-facing classification of an offered tool.
type TurnTool struct {
	Name       string                  `json:"name"`
	Kind       TurnToolKind            `json:"kind"`
	Permission domain.PermissionPolicy `json:"permission"`
}

// PrepareTurnInput identifies one public trigger whose model turn should run.
type PrepareTurnInput struct {
	SessionID          string   `json:"session_id"`
	TriggerEventID     string   `json:"trigger_event_id"`
	ResolutionEventIDs []string `json:"resolution_event_ids,omitempty"`
}

// PrepareTurnResult is the immutable starting state for a Workflow-owned turn.
// The projected messages and tool definitions are Activity output, so Temporal
// records them in history and deterministic replay never rereads PostgreSQL.
type PrepareTurnResult struct {
	AlreadyCompleted bool           `json:"already_completed"`
	Terminated       bool           `json:"terminated"`
	FatalError       string         `json:"fatal_error,omitempty"`
	AttemptID        string         `json:"attempt_id,omitempty"`
	Request          model.Request  `json:"request"`
	Tools            []TurnTool     `json:"tools,omitempty"`
	ResumeActions    []ResumeAction `json:"resume_actions,omitempty"`
}

// ResumeAction is the server-owned reconstruction of one parked tool call and
// its admitted client result. The Activity validates and normalizes the raw
// event payloads before they enter Workflow history. Confirmations also carry a
// stable journal step id for an allowed built-in execution.
type ResumeAction struct {
	ActionEventID     string                   `json:"action_event_id"`
	Kind              domain.PendingActionKind `json:"kind"`
	ToolName          string                   `json:"tool_name"`
	Input             map[string]any           `json:"input"`
	ResolutionEventID string                   `json:"resolution_event_id"`
	Content           []any                    `json:"content,omitempty"`
	IsError           bool                     `json:"is_error,omitempty"`
	Confirmation      string                   `json:"confirmation,omitempty"`
	DenyMessage       string                   `json:"deny_message,omitempty"`
	ToolStepID        string                   `json:"tool_step_id,omitempty"`
}

// CallModelInput is one plan/observe step. Each call is its own Activity so its
// completed response is recorded independently in Workflow history.
type CallModelInput struct {
	SessionID string        `json:"session_id"`
	Request   model.Request `json:"request"`
}

// PlannedToolStep binds one public tool-use event to its internal journal step.
// Both ids are Activity output recorded in Workflow history; retries therefore
// reuse explicit operation ids rather than deriving one namespace from another.
type PlannedToolStep struct {
	ToolUseEventID string `json:"tool_use_event_id"`
	ToolStepID     string `json:"tool_step_id"`
}

// CallModelResult carries a normalized model response. ToolUseID values have
// been replaced with server-owned public event IDs, and MessageEventID names the
// public agent.message when the response contains non-empty text.
type CallModelResult struct {
	Response       model.Response    `json:"response"`
	MessageEventID string            `json:"message_event_id,omitempty"`
	ToolSteps      []PlannedToolStep `json:"tool_steps,omitempty"`
	FatalError     string            `json:"fatal_error,omitempty"`
}

// ExecuteToolInput identifies one logical built-in tool step. ToolUseEventID is
// stable because it came from the completed CallModel Activity result.
type ExecuteToolInput struct {
	SessionID      string         `json:"session_id"`
	TriggerEventID string         `json:"trigger_event_id"`
	AttemptID      string         `json:"attempt_id"`
	Ordinal        int            `json:"ordinal"`
	ToolUseEventID string         `json:"tool_use_event_id"`
	ToolStepID     string         `json:"tool_step_id"`
	ToolName       string         `json:"tool_name"`
	Input          map[string]any `json:"input"`
}

// ExecuteToolResult is the durable result of one tool Activity. Ambiguous is a
// successful Activity result (not a retryable error): the Workflow must
// terminate the turn honestly without scheduling the side effect again.
type ExecuteToolResult struct {
	Result     domain.ToolStepResult `json:"result"`
	Ambiguous  bool                  `json:"ambiguous"`
	FatalError string                `json:"fatal_error,omitempty"`
}

// CompleteWorkflowTurnInput atomically finalizes the optional tool attempt and
// commits the public turn output in PostgreSQL.
type CompleteWorkflowTurnInput struct {
	SessionID      string                 `json:"session_id"`
	TriggerEventID string                 `json:"trigger_event_id"`
	Output         []domain.EventDraft    `json:"output"`
	Status         domain.Status          `json:"status"`
	AttemptID      string                 `json:"attempt_id,omitempty"`
	AttemptState   domain.RunAttemptState `json:"attempt_state,omitempty"`
	AttemptError   *string                `json:"attempt_error,omitempty"`
	// PendingActionEventIDs names action events in Output that park this turn
	// awaiting client input.
	PendingActionEventIDs []string `json:"pending_action_event_ids,omitempty"`
	// ResolutionEventIDs names every client event that closes the current
	// pending-action barrier. PostgreSQL validates the set atomically.
	ResolutionEventIDs []string `json:"resolution_event_ids,omitempty"`
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

// LoadInterruptInput asks the Activity read side for the first still-unprocessed
// interrupt after a turn's trigger sequence. Wakeup Signals remain metadata
// only; PostgreSQL decides whether an interrupt was durably admitted.
type LoadInterruptInput struct {
	SessionID string `json:"session_id"`
	AfterSeq  int64  `json:"after_seq"`
}

type LoadInterruptResult struct {
	Interrupt *EventRef `json:"interrupt,omitempty"`
}

// LoadPendingActionsInput asks PostgreSQL for the current durable
// requires_action barrier. The result, rather than mutable database state read
// directly by Workflow code, drives the Workflow selector.
type LoadPendingActionsInput struct {
	SessionID string `json:"session_id"`
}

// PendingActionRef is the minimal deterministic selector projection for one
// unresolved pending action. Payloads remain behind the Activity boundary.
type PendingActionRef struct {
	ActionEventID      string                   `json:"action_event_id"`
	ActionEventSeq     int64                    `json:"action_event_seq"`
	Kind               domain.PendingActionKind `json:"kind"`
	ResolutionEventID  string                   `json:"resolution_event_id,omitempty"`
	ResolutionEventSeq int64                    `json:"resolution_event_seq,omitempty"`
}

type LoadPendingActionsResult struct {
	Actions []PendingActionRef `json:"actions,omitempty"`
}
