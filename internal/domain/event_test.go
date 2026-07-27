package domain

import "testing"

func TestEventClassification(t *testing.T) {
	if !IsUserEvent(EvUserMessage) {
		t.Fatal("user.message should be a user event")
	}
	if IsUserEvent(EvAgentMessage) {
		t.Fatal("agent.message is not a user event")
	}
	if !ProcessedOnReceipt(EvUserCustomToolResult) {
		t.Fatal("custom_tool_result processed on receipt")
	}
	if ProcessedOnReceipt(EvUserMessage) {
		t.Fatal("user.message is queued, not processed on receipt")
	}
	if !ProcessedOnReceipt(EvAgentMessage) || !ProcessedOnReceipt(EvSessionDeleted) {
		t.Fatal("server-emitted events are complete when persisted")
	}
}
