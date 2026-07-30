package temporal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestPlanToolBatch_ClassifiesWholeRoundBeforeExecution(t *testing.T) {
	uses := []domain.ContentBlock{
		{
			Type: "tool_use", ToolUseID: "sevt_custom", ToolName: "ask_client",
			Input: map[string]any{"question": "continue?"},
		},
		{
			Type: "tool_use", ToolUseID: "sevt_builtin", ToolName: "bash",
			Input: map[string]any{"command": "pwd"},
		},
		{
			Type: "tool_use", ToolUseID: "sevt_ask", ToolName: "write",
			Input: map[string]any{"path": "a.txt", "content": "hello"},
		},
	}
	tools := indexTurnTools([]TurnTool{
		{Name: "ask_client", Kind: TurnToolCustom},
		{
			Name: "bash", Kind: TurnToolBuiltin,
			Permission: domain.PermissionPolicy{Type: "always_allow"},
		},
		{
			Name: "write", Kind: TurnToolBuiltin,
			Permission: domain.PermissionPolicy{Type: "always_ask"},
		},
	})
	steps := map[string]string{
		"sevt_custom":  "tstep_custom",
		"sevt_builtin": "tstep_builtin",
		"sevt_ask":     "tstep_ask",
	}

	plan, failure := planToolBatch(uses, tools, steps)

	require.Empty(t, failure)
	require.Equal(t, []string{
		domain.EvAgentCustomToolUse,
		domain.EvAgentToolUse,
		domain.EvAgentToolUse,
	}, draftTypes(plan.actionDrafts))
	require.Equal(t, []string{"sevt_custom", "sevt_ask"}, plan.pendingActionEventIDs)
	require.Equal(t, []plannedToolUse{{
		use:    uses[1],
		stepID: "tstep_builtin",
	}}, plan.executable)
	require.Equal(
		t,
		"ask",
		plan.actionDrafts[2].Payload["evaluated_permission"],
	)
}

func TestPlanToolBatch_RejectsInvalidRoundBeforePlanning(t *testing.T) {
	tests := []struct {
		name        string
		use         domain.ContentBlock
		tools       []TurnTool
		steps       map[string]string
		wantFailure turnFailure
	}{
		{
			name: "missing durable operation id",
			use: domain.ContentBlock{
				Type: "tool_use", ToolUseID: "sevt_bash", ToolName: "bash",
			},
			tools: []TurnTool{{
				Name: "bash", Kind: TurnToolBuiltin,
				Permission: domain.PermissionPolicy{Type: "always_allow"},
			}},
			steps:       map[string]string{},
			wantFailure: failTurn("model tool request has no durable operation id"),
		},
		{
			name: "tool not enabled",
			use: domain.ContentBlock{
				Type: "tool_use", ToolUseID: "sevt_missing", ToolName: "missing",
			},
			steps: map[string]string{"sevt_missing": "tstep_missing"},
			wantFailure: failTurn(
				"model requested a tool that is not enabled: missing",
			),
		},
		{
			name: "unsupported builtin permission",
			use: domain.ContentBlock{
				Type: "tool_use", ToolUseID: "sevt_bash", ToolName: "bash",
			},
			tools: []TurnTool{{
				Name: "bash", Kind: TurnToolBuiltin,
				Permission: domain.PermissionPolicy{Type: "always_deny"},
			}},
			steps: map[string]string{"sevt_bash": "tstep_bash"},
			wantFailure: failTurn(
				"built-in tool has unsupported permission policy: always_deny",
			),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, failure := planToolBatch(
				[]domain.ContentBlock{tc.use},
				indexTurnTools(tc.tools),
				tc.steps,
			)
			require.Equal(t, tc.wantFailure, failure)
			require.Empty(t, plan.actionDrafts)
			require.Empty(t, plan.executable)
			require.Empty(t, plan.pendingActionEventIDs)
		})
	}
}

func TestIndexTurnTools_PreservesFirstOwner(t *testing.T) {
	tools := indexTurnTools([]TurnTool{
		{
			Name: "read", Kind: TurnToolBuiltin,
			Permission: domain.PermissionPolicy{Type: "always_allow"},
		},
		{Name: "read", Kind: TurnToolCustom},
	})

	require.Equal(t, TurnToolBuiltin, tools["read"].Kind)
}
