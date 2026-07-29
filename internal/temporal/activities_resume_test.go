package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestPrepareTurn_ProcessedOnReceiptCustomResultStillResumesPendingBarrier(t *testing.T) {
	processedAt := time.Now().UTC()
	resolutionID := "sevt_custom_result"
	source := newFakeSource([]domain.Event{
		{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "inspect"}},
			},
		},
		{
			ID: "sevt_custom", Sequence: 2, Type: domain.EvAgentCustomToolUse,
			Payload: map[string]any{
				"name":  "ask_client",
				"input": map[string]any{"question": "continue?"},
			},
		},
		{
			ID: "sevt_custom_result", Sequence: 3,
			Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{
				"custom_tool_use_id": "sevt_custom",
				"content": []any{
					map[string]any{"type": "text", "text": "yes"},
				},
			},
			ProcessedAt: &processedAt,
		},
	})
	source.pendingActions = []domain.PendingAction{{
		ID:               "pact_1",
		SessionID:        "sess_resume",
		ActionEventID:    "sevt_custom",
		Kind:             domain.PendingCustomToolResult,
		ResolvingEventID: &resolutionID,
	}}
	activities := NewActivities(nil, nil, source, nil, nil, &testIDGen{})

	selector, err := activities.LoadPendingActions(context.Background(), LoadPendingActionsInput{
		SessionID: "sess_resume",
	})
	require.NoError(t, err)
	require.Equal(t, []PendingActionRef{{
		ActionEventID:      "sevt_custom",
		ActionEventSeq:     2,
		Kind:               domain.PendingCustomToolResult,
		ResolutionEventID:  "sevt_custom_result",
		ResolutionEventSeq: 3,
	}}, selector.Actions)

	prepared, err := activities.PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID:          "sess_resume",
		TriggerEventID:     "sevt_custom_result",
		ResolutionEventIDs: []string{"sevt_custom_result"},
	})
	require.NoError(t, err)
	require.False(t, prepared.AlreadyCompleted)
	require.Empty(t, prepared.FatalError)
	require.Equal(t, []domain.Message{{
		Role: domain.RoleUser,
		Content: []domain.ContentBlock{{
			Type: "text", Text: "inspect",
		}},
	}}, prepared.Request.Messages, "parked action/result must be reconstructed by Workflow, not projected twice")
	require.Equal(t, []ResumeAction{{
		ActionEventID:     "sevt_custom",
		Kind:              domain.PendingCustomToolResult,
		ToolName:          "ask_client",
		Input:             map[string]any{"question": "continue?"},
		ResolutionEventID: "sevt_custom_result",
		Content: []any{
			map[string]any{"type": "text", "text": "yes"},
		},
	}}, prepared.ResumeActions)
}
