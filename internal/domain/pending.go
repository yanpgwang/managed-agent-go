package domain

import "time"

// PendingActionKind is the expected response kind that resolves a parked action.
// It is derived from the committed action event's type AND payload — never from
// an arbitrary caller string — so a client cannot claim a kind the server did
// not park.
type PendingActionKind string

const (
	// PendingCustomToolResult is parked by an agent.custom_tool_use event and is
	// resolved by a user.custom_tool_result whose custom_tool_use_id references
	// the parked event.
	PendingCustomToolResult PendingActionKind = "custom_tool_result"
	// PendingToolConfirmation is parked by an always_ask agent.tool_use event and
	// is resolved by a user.tool_confirmation whose tool_use_id references the
	// parked event. Allow executes the original server-owned built-in call; deny
	// produces a correlated error tool result without execution.
	PendingToolConfirmation PendingActionKind = "tool_confirmation"
	// PendingToolResult is parked by a self-hosted agent.tool_use. The client
	// executes the sandbox-routed tool and resolves it with user.tool_result.
	PendingToolResult PendingActionKind = "tool_result"
)

// PendingAction is a first-class durable record that a run parked awaiting a
// client response. It is internal-only and never serialized onto the public
// Managed Agents wire. ResolvedAt is nil while the action still blocks the
// session's ordinary queued work.
type PendingAction struct {
	ID               string
	SessionID        string
	ActionEventID    string
	Kind             PendingActionKind
	ResolvingEventID *string
	CreatedAt        time.Time
	ResolvedAt       *time.Time
}

// PrefixPendingAction is the id prefix for durable pending-action records.
const PrefixPendingAction = "pact_"

// PendingActionKindForEvent derives the expected response kind for a parked
// action event from its committed type AND payload. ok is false when the event
// cannot park a run.
//
// An agent.tool_use only parks when its evaluated permission is "ask": an
// always_allow built-in call is executed inline and its always_deny/missing
// counterpart is not a confirmation gate, so both share the agent.tool_use type
// but must never become a PendingToolConfirmation. agent.mcp_tool_use follows
// the same rule — the documented confirmation path is identical and the client
// still answers with a user.tool_confirmation carrying tool_use_id.
// agent.custom_tool_use always parks regardless of payload.
func PendingActionKindForEvent(eventType string, payload map[string]any) (PendingActionKind, bool) {
	switch eventType {
	case EvAgentCustomToolUse:
		return PendingCustomToolResult, true
	case EvAgentToolUse, EvAgentMcpToolUse:
		if owner, _ := payload[InternalToolExecutionOwner].(string); owner == "self_hosted" {
			return PendingToolResult, true
		}
		if perm, _ := payload["evaluated_permission"].(string); perm == "ask" {
			return PendingToolConfirmation, true
		}
		return "", false
	}
	return "", false
}

// ResolutionReference reports whether an event resolves a pending action and, if
// so, the parked action event id it references and the kind it satisfies. The
// referenced id lives in a type-specific payload field (custom_tool_use_id for
// user.custom_tool_result, tool_use_id for user.tool_confirmation); a resolution
// event missing that field returns ok=false.
//
// user.tool_confirmation keeps tool_use_id for both agent.tool_use and
// agent.mcp_tool_use parks: the documented confirmation input has exactly one id
// field and never a separate MCP spelling.
func ResolutionReference(eventType string, payload map[string]any) (actionEventID string, kind PendingActionKind, ok bool) {
	switch eventType {
	case EvUserCustomToolResult:
		id, _ := payload["custom_tool_use_id"].(string)
		if id == "" {
			return "", "", false
		}
		return id, PendingCustomToolResult, true
	case EvUserToolConfirmation:
		id, _ := payload["tool_use_id"].(string)
		if id == "" {
			return "", "", false
		}
		return id, PendingToolConfirmation, true
	case EvUserToolResult:
		id, _ := payload["tool_use_id"].(string)
		if id == "" {
			return "", "", false
		}
		return id, PendingToolResult, true
	}
	return "", "", false
}
