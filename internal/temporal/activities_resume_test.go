package temporal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestPrepareTurnCompactsRequestButKeepsLosslessTranscriptDelta(t *testing.T) {
	processedAt := time.Now().UTC()
	base := newFakeSource([]domain.Event{
		{
			ID: "sevt_prior", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "old request"},
			}},
			ProcessedAt: &processedAt,
		},
		{
			ID: "sevt_current", Sequence: 2, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "current request"},
			}},
		},
	})
	largeImage := json.RawMessage(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 30000) + `"}}`)
	source := &configuredTranscriptFakeSource{
		transcriptFakeSource: &transcriptFakeSource{
			fakeSource: base,
			transcript: domain.ProviderTranscript{
				TriggerEventIDs: []string{"sevt_prior"},
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "image", Raw: largeImage}}},
					{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Type: "text", Text: strings.Repeat("old response ", 3000)}}},
				},
			},
		},
		session: domain.Session{
			ID: "sess_context", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{Model: domain.Model{ID: "model"}},
		},
	}

	prepared, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).WithContextTokenBudget(500).PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID: "sess_context", TriggerEventID: "sevt_current",
	})
	require.NoError(t, err)
	require.True(t, prepared.UsesProviderTranscript)
	require.True(t, prepared.ContextProjection.Compacted)
	require.Equal(t, "current request", prepared.TranscriptDelta[0].Content[0].Text)
	require.Contains(t, prepared.Request.Messages[0].Content[0].Text, "compacted")
	require.NotEmpty(t, source.transcript.Messages[0].Content[0].Raw,
		"request projection must not mutate the durable provider transcript")
}

type transcriptFakeSource struct {
	*fakeSource
	transcript domain.ProviderTranscript
}

func (s *transcriptFakeSource) LoadProviderTranscript(
	context.Context,
	string,
) (domain.ProviderTranscript, error) {
	return s.transcript, nil
}

type configuredTranscriptFakeSource struct {
	*transcriptFakeSource
	session domain.Session
}

func (s *configuredTranscriptFakeSource) GetSession(
	context.Context,
	string,
) (domain.Session, error) {
	return s.session, nil
}

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
	activities := NewActivities(nil, source, nil, nil, &testIDGen{})

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

func TestPrepareTurn_UsesLosslessTranscriptAndMapsResumeToProviderID(t *testing.T) {
	processedAt := time.Now().UTC()
	resolutionID := "sevt_custom_result"
	base := newFakeSource([]domain.Event{
		{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "inspect"},
			}},
			ProcessedAt: &processedAt,
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
	base.pendingActions = []domain.PendingAction{{
		ID:               "pact_1",
		SessionID:        "sess_resume",
		ActionEventID:    "sevt_custom",
		Kind:             domain.PendingCustomToolResult,
		ResolvingEventID: &resolutionID,
	}}
	rawToolUse := json.RawMessage(
		`{"type":"tool_use","id":"toolu_provider","name":"ask_client","input":{"question":"continue?"},"future_field":"keep"}`,
	)
	source := &transcriptFakeSource{
		fakeSource: base,
		transcript: domain.ProviderTranscript{
			TriggerEventIDs: []string{"sevt_user"},
			Messages: []domain.Message{
				{
					Role: domain.RoleUser,
					Content: []domain.ContentBlock{{
						Type: "text", Text: "inspect",
					}},
				},
				{
					Role: domain.RoleAssistant,
					Content: []domain.ContentBlock{{
						Type:      "tool_use",
						ToolUseID: "toolu_provider",
						ToolName:  "ask_client",
						Input: map[string]any{
							"question": "continue?",
						},
						Raw: rawToolUse,
					}},
				},
			},
			ToolUseMappings: []domain.ProviderToolUseMapping{{
				PublicEventID:     "sevt_custom",
				ProviderToolUseID: "toolu_provider",
				ToolName:          "ask_client",
			}},
		},
	}
	activities := NewActivities(nil, source, nil, nil, &testIDGen{})
	prepared, err := activities.PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID:          "sess_resume",
			TriggerEventID:     "sevt_custom_result",
			ResolutionEventIDs: []string{"sevt_custom_result"},
		},
	)
	require.NoError(t, err)
	require.True(t, prepared.UsesProviderTranscript)
	require.Len(t, prepared.Request.Messages, 2)
	require.Equal(
		t,
		"toolu_provider",
		prepared.ResumeActions[0].ProviderToolUseID,
	)

	turn := &workflowTurnState{
		usesProviderTranscript: true,
	}
	messages, _, failure, err := resumeWorkflowTurn(
		turn,
		prepared,
		map[string]TurnTool{
			"ask_client": {Name: "ask_client", Kind: TurnToolCustom},
		},
		prepared.Request.Messages,
	)
	require.NoError(t, err)
	require.Empty(t, failure)
	require.Len(t, messages, 3)
	result := messages[2].Content[0]
	require.Equal(t, "toolu_provider", result.ToolResultFor)
	require.Len(t, turn.transcriptDelta, 1)
	require.Equal(
		t,
		"toolu_provider",
		turn.transcriptDelta[0].Content[0].ToolResultFor,
	)
}

func TestPrepareTurn_MultiActionResumeKeepsLosslessTranscript(t *testing.T) {
	processedAt := time.Now().UTC()
	resolutionA := "sevt_result_a"
	resolutionB := "sevt_result_b"
	base := newFakeSource([]domain.Event{
		{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "do both"},
			}},
			ProcessedAt: &processedAt,
		},
		{
			ID: "sevt_action_a", Sequence: 2, Type: domain.EvAgentCustomToolUse,
			Payload: map[string]any{
				"name": "tool_a", "input": map[string]any{"value": "a"},
			},
		},
		{
			ID: "sevt_action_b", Sequence: 3, Type: domain.EvAgentCustomToolUse,
			Payload: map[string]any{
				"name": "tool_b", "input": map[string]any{"value": "b"},
			},
		},
		{
			ID: resolutionA, Sequence: 4, Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{
				"custom_tool_use_id": "sevt_action_a",
				"content":            []any{map[string]any{"type": "text", "text": "A"}},
			},
			ProcessedAt: &processedAt,
		},
		{
			ID: resolutionB, Sequence: 5, Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{
				"custom_tool_use_id": "sevt_action_b",
				"content":            []any{map[string]any{"type": "text", "text": "B"}},
			},
			ProcessedAt: &processedAt,
		},
	})
	base.pendingActions = []domain.PendingAction{
		{
			ID: "pact_a", SessionID: "sess_multi",
			ActionEventID: "sevt_action_a", Kind: domain.PendingCustomToolResult,
			ResolvingEventID: &resolutionA,
		},
		{
			ID: "pact_b", SessionID: "sess_multi",
			ActionEventID: "sevt_action_b", Kind: domain.PendingCustomToolResult,
			ResolvingEventID: &resolutionB,
		},
	}
	source := &configuredTranscriptFakeSource{
		transcriptFakeSource: &transcriptFakeSource{
			fakeSource: base,
			transcript: domain.ProviderTranscript{
				TriggerEventIDs: []string{"sevt_user"},
				Messages: []domain.Message{
					{
						Role:    domain.RoleUser,
						Content: []domain.ContentBlock{{Type: "text", Text: "do both"}},
					},
					{
						Role: domain.RoleAssistant,
						Content: []domain.ContentBlock{
							{Type: "tool_use", ToolUseID: "provider_a", ToolName: "tool_a", Input: map[string]any{"value": "a"}},
							{Type: "tool_use", ToolUseID: "provider_b", ToolName: "tool_b", Input: map[string]any{"value": "b"}},
						},
					},
				},
				ToolUseMappings: []domain.ProviderToolUseMapping{
					{PublicEventID: "sevt_action_a", ProviderToolUseID: "provider_a", ToolName: "tool_a"},
					{PublicEventID: "sevt_action_b", ProviderToolUseID: "provider_b", ToolName: "tool_b"},
				},
			},
		},
		session: domain.Session{
			ID: "sess_multi", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				Tools: []any{
					map[string]any{"type": "custom", "name": "tool_a"},
					map[string]any{"type": "custom", "name": "tool_b"},
				},
			},
		},
	}

	prepared, err := NewActivities(
		nil,
		source,
		nil,
		nil,
		&testIDGen{},
	).PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID:          "sess_multi",
		TriggerEventID:     resolutionB,
		ResolutionEventIDs: []string{resolutionA, resolutionB},
	})

	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.True(t, prepared.UsesProviderTranscript)
	require.Len(t, prepared.ResumeActions, 2)
	require.Equal(t, "provider_a", prepared.ResumeActions[0].ProviderToolUseID)
	require.Equal(t, "provider_b", prepared.ResumeActions[1].ProviderToolUseID)
}
