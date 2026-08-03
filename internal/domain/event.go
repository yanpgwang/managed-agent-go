package domain

import "time"

// Public event type constants. Every event is a top-level tagged union keyed on
// the "type" field following the {domain}.{action} convention. These are the
// wire values from the official Managed Agents events reference.
const (
	// Client-submittable input events.
	EvUserMessage          = "user.message"
	EvUserInterrupt        = "user.interrupt"
	EvUserToolConfirmation = "user.tool_confirmation"
	EvUserCustomToolResult = "user.custom_tool_result"
	EvUserToolResult       = "user.tool_result"
	EvUserDefineOutcome    = "user.define_outcome"
	EvSystemMessage        = "system.message"

	// Agent/server-emitted events (never accepted from clients).
	EvAgentMessage       = "agent.message"
	EvAgentCustomToolUse = "agent.custom_tool_use"
	EvAgentToolUse       = "agent.tool_use"
	EvAgentToolResult    = "agent.tool_result"

	// MCP tool calls are a distinct documented variant of the tool-use pair. The
	// use event additionally carries a required mcp_server_name and reports the
	// bare tool name as the server published it (not the namespaced model-facing
	// alias). The result event correlates through mcp_tool_use_id and carries no
	// server name: a client attributes a result to a server by joining back to
	// its use event.
	EvAgentMcpToolUse    = "agent.mcp_tool_use"
	EvAgentMcpToolResult = "agent.mcp_tool_result"

	EvSessionStatusIdle         = "session.status_idle"
	EvSessionStatusRunning      = "session.status_running"
	EvSessionStatusTerminated   = "session.status_terminated"
	EvSessionStatusRescheduling = "session.status_rescheduled"
	EvSessionError              = "session.error"
	EvSessionUpdated            = "session.updated"
	EvSessionDeleted            = "session.deleted"

	EvSpanOutcomeEvaluationStart   = "span.outcome_evaluation_start"
	EvSpanOutcomeEvaluationOngoing = "span.outcome_evaluation_ongoing"
	EvSpanOutcomeEvaluationEnd     = "span.outcome_evaluation_end"
	EvSpanModelRequestStart        = "span.model_request_start"
	EvSpanModelRequestEnd          = "span.model_request_end"
)

// Internal event payload keys support server-side causal linking without
// changing the public Managed Agents event shape. HTTP projections must never
// expose keys with this prefix.
const (
	InternalCompanionSystemEventID = "__companion_system_event_id"
	InternalCompanionSystemContent = "__companion_system_content"
	InternalToolExecutionOwner     = "__tool_execution_owner"
)

// EventDraft is an event about to be persisted. Payload holds the type-specific
// top-level fields (everything except "type"); it is flattened onto the wire
// object, never nested under a "payload" key.
//
// ID is normally empty: the store assigns the committed id at persist time. A
// server-side emitter (the runtime sink) may pre-assign the committed id so it
// can reference the event before the persist transaction runs — for example to
// correlate a tool_result to its tool_use, or to name the parked
// agent.custom_tool_use / agent.tool_use events in a requires_action stop
// reason. Client-submitted events never carry an id (rejected at the edge).
type EventDraft struct {
	ID      string
	Type    string
	Payload map[string]any
}

// Event is the internal representation of a persisted session event. Sequence
// is an internal ordering key and must never appear on the compatibility wire.
type Event struct {
	ID          string
	SessionID   string
	Sequence    int64
	Type        string
	Payload     map[string]any
	CreatedAt   time.Time
	ProcessedAt *time.Time
}

// IsUserEvent reports whether t is one of the user.* input event types.
func IsUserEvent(t string) bool {
	switch t {
	case EvUserMessage, EvUserInterrupt, EvUserToolConfirmation,
		EvUserCustomToolResult, EvUserToolResult, EvUserDefineOutcome:
		return true
	}
	return false
}

// IsClientSubmittable reports whether a client may POST an event of this type to
// the send-events endpoint. The set is exactly the documented user.* inputs plus
// system.message. Agent-, session-, and span-scoped types are server-only and
// must be rejected; that includes agent.mcp_tool_use and agent.mcp_tool_result,
// which are emitted by the runtime and never accepted from a caller.
func IsClientSubmittable(t string) bool {
	return IsUserEvent(t) || t == EvSystemMessage
}

// IsAgentToolUse reports whether a type is one of the server-emitted tool-call
// announcements. Both agent.tool_use and agent.mcp_tool_use name a call the
// runtime made on the model's behalf; agent.custom_tool_use names one the client
// must execute.
func IsAgentToolUse(t string) bool {
	switch t {
	case EvAgentToolUse, EvAgentMcpToolUse, EvAgentCustomToolUse:
		return true
	}
	return false
}

// AgentToolResultReference returns the tool-use event id a server-emitted tool
// result correlates to, reading the id field the documented variant uses:
// tool_use_id for agent.tool_result and mcp_tool_use_id for
// agent.mcp_tool_result. ok is false for any other event type.
func AgentToolResultReference(
	eventType string,
	payload map[string]any,
) (toolUseEventID string, ok bool) {
	switch eventType {
	case EvAgentToolResult:
		id, _ := payload["tool_use_id"].(string)
		return id, true
	case EvAgentMcpToolResult:
		id, _ := payload["mcp_tool_use_id"].(string)
		return id, true
	}
	return "", false
}

// IsInitialEventType reports whether a type is allowed in a session's
// initial_events. Only user.message and user.define_outcome are accepted there;
// unlike scheduled deployments, initial_events does not accept system.message.
func IsInitialEventType(t string) bool {
	return t == EvUserMessage || t == EvUserDefineOutcome
}

// ProcessedOnReceipt reports whether an event is stamped processed_at at persist
// time rather than when a later turn consumes it. Server-only events are already
// processed when emitted; selected client events are acknowledged on receipt.
func ProcessedOnReceipt(t string) bool {
	if !IsClientSubmittable(t) {
		return true
	}
	switch t {
	case EvUserDefineOutcome, EvUserCustomToolResult, EvUserToolResult:
		return true
	}
	return false
}
