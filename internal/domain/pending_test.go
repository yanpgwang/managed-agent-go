package domain

import "testing"

func TestPendingActionKindForEvent(t *testing.T) {
	cases := []struct {
		eventType string
		payload   map[string]any
		want      PendingActionKind
		ok        bool
	}{
		{EvAgentCustomToolUse, nil, PendingCustomToolResult, true},
		// agent.tool_use parks only when its evaluated permission is "ask".
		{EvAgentToolUse, map[string]any{"evaluated_permission": "ask"}, PendingToolConfirmation, true},
		{EvAgentToolUse, map[string]any{InternalToolExecutionOwner: "self_hosted", "evaluated_permission": "allow"}, PendingToolResult, true},
		{EvAgentToolUse, map[string]any{"evaluated_permission": "always_allow"}, "", false},
		{EvAgentToolUse, map[string]any{"evaluated_permission": "always_deny"}, "", false},
		{EvAgentToolUse, map[string]any{}, "", false},
		{EvAgentToolUse, nil, "", false},
		{EvAgentMessage, nil, "", false},
		{EvUserMessage, nil, "", false},
	}
	for _, c := range cases {
		got, ok := PendingActionKindForEvent(c.eventType, c.payload)
		if got != c.want || ok != c.ok {
			t.Errorf("PendingActionKindForEvent(%q, %v) = %q,%v want %q,%v", c.eventType, c.payload, got, ok, c.want, c.ok)
		}
	}
}

func TestResolutionReference(t *testing.T) {
	id, kind, ok := ResolutionReference(EvUserCustomToolResult, map[string]any{"custom_tool_use_id": "sevt_1"})
	if !ok || id != "sevt_1" || kind != PendingCustomToolResult {
		t.Fatalf("custom_tool_result = %q,%q,%v", id, kind, ok)
	}
	id, kind, ok = ResolutionReference(EvUserToolConfirmation, map[string]any{"tool_use_id": "sevt_2"})
	if !ok || id != "sevt_2" || kind != PendingToolConfirmation {
		t.Fatalf("tool_confirmation = %q,%q,%v", id, kind, ok)
	}
	id, kind, ok = ResolutionReference(EvUserToolResult, map[string]any{"tool_use_id": "sevt_3"})
	if !ok || id != "sevt_3" || kind != PendingToolResult {
		t.Fatalf("tool_result = %q,%q,%v", id, kind, ok)
	}
	// A resolution event missing its reference field is not a valid resolution.
	if _, _, ok := ResolutionReference(EvUserCustomToolResult, map[string]any{}); ok {
		t.Error("custom_tool_result without custom_tool_use_id should not resolve")
	}
	// Non-resolution event types never resolve.
	if _, _, ok := ResolutionReference(EvUserMessage, map[string]any{"custom_tool_use_id": "x"}); ok {
		t.Error("user.message should not be a resolution")
	}
}
