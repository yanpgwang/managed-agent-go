package pg

import (
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// An interrupted turn keeps a tool round only when the round durably completed.
// The MCP pair correlates through mcp_tool_use_id, so the filter must join on
// that field rather than tool_use_id or a completed MCP call would be dropped
// from the public ledger.
func TestInterruptedTurnDrafts_KeepsCompletedMCPPair(t *testing.T) {
	drafts := []domain.EventDraft{
		{
			ID: "sevt_mcp_done", Type: domain.EvAgentMcpToolUse,
			Payload: map[string]any{
				"name": "list_issues", "mcp_server_name": "github",
			},
		},
		{
			Type: domain.EvAgentMcpToolResult,
			Payload: map[string]any{
				"mcp_tool_use_id": "sevt_mcp_done",
				"content":         []any{},
			},
		},
		{
			ID: "sevt_mcp_open", Type: domain.EvAgentMcpToolUse,
			Payload: map[string]any{
				"name": "create_issue", "mcp_server_name": "github",
				"evaluated_permission": "ask",
			},
		},
		{
			ID: "sevt_builtin_open", Type: domain.EvAgentToolUse,
			Payload: map[string]any{"name": "bash"},
		},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{}},
	}

	out, _ := interruptedTurnDrafts(drafts)

	got := make([]string, 0, len(out))
	for _, draft := range out {
		got = append(got, draft.Type+"/"+draft.ID)
	}
	want := []string{
		domain.EvAgentMcpToolUse + "/sevt_mcp_done",
		domain.EvAgentMcpToolResult + "/",
	}
	if len(got) != len(want) {
		t.Fatalf("interrupted drafts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interrupted drafts = %v, want %v", got, want)
		}
	}
}

// The MCP tool-use variant must also be recognized when pruning provider
// tool-use mappings down to what a turn actually committed.
func TestRetainCommittedProviderMappings_IncludesMCPToolUse(t *testing.T) {
	mappings := []domain.ProviderToolUseMapping{
		{PublicEventID: "sevt_mcp", ProviderToolUseID: "toolu_mcp"},
		{PublicEventID: "sevt_dropped", ProviderToolUseID: "toolu_dropped"},
	}
	drafts := []domain.EventDraft{{
		ID: "sevt_mcp", Type: domain.EvAgentMcpToolUse,
		Payload: map[string]any{"mcp_server_name": "github"},
	}}

	out := retainCommittedProviderMappings(mappings, drafts)

	if len(out) != 1 || out[0].PublicEventID != "sevt_mcp" {
		t.Fatalf("retained mappings = %#v", out)
	}
}
