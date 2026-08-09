package temporal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
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
	skills  []domain.SkillVersion
}

type threadConfiguredTranscriptFakeSource struct {
	*configuredTranscriptFakeSource
	thread  domain.SessionThread
	runtime domain.SkillRuntime
}

func (s *threadConfiguredTranscriptFakeSource) GetSessionThread(
	context.Context,
	string,
	string,
) (domain.SessionThread, error) {
	return s.thread, nil
}

func (s *threadConfiguredTranscriptFakeSource) SessionThreadSkillRuntime(
	context.Context,
	string,
	string,
) (domain.SkillRuntime, error) {
	return s.runtime, nil
}

func (s *configuredTranscriptFakeSource) GetSession(
	context.Context,
	string,
) (domain.Session, error) {
	return s.session, nil
}

func (s *configuredTranscriptFakeSource) SessionSkillsForRuntime(
	context.Context,
	string,
) ([]domain.SkillVersion, error) {
	return append([]domain.SkillVersion(nil), s.skills...), nil
}

func TestPrepareTurn_ProjectsPinnedSkillDiscoveryMetadata(t *testing.T) {
	base := newFakeSource([]domain.Event{{
		ID: "sevt_skill", Sequence: 1, Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "analyze the report"},
		}},
	}})
	source := &configuredTranscriptFakeSource{
		transcriptFakeSource: &transcriptFakeSource{fakeSource: base},
		session: domain.Session{
			ID: "sess_skill", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
				Skills: []domain.SkillReference{{
					Type: "custom", SkillID: "skill_reports", Version: "100",
				}},
			},
		},
		skills: []domain.SkillVersion{{
			SkillID: "skill_reports", Version: "100", Name: "report-tools",
			Description: "Analyze reports", UncompressedSizeBytes: 1024,
		}},
	}
	prepared, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).WithSkillRuntimeSupported(true).PrepareTurn(
		context.Background(),
		PrepareTurnInput{SessionID: "sess_skill", TriggerEventID: "sevt_skill"},
	)
	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.Contains(t, prepared.Request.System, "<available_skills>")
	require.Contains(t, prepared.Request.System, `"name":"report-tools"`)
	require.Contains(t, prepared.Request.System, "/workspace/skills/report-tools/SKILL.md")
	require.Contains(t, summarizeModelTools(prepared.Request.Tools), modelToolSummary{
		Name: agentruntime.RuntimeSkillToolName,
	})
	require.Contains(t, prepared.Tools, TurnTool{
		Name:       agentruntime.RuntimeSkillToolName,
		Kind:       TurnToolRuntimeSkill,
		Permission: domain.PermissionPolicy{Type: "always_allow"},
	})

	unsupported, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).PrepareTurn(
		context.Background(),
		PrepareTurnInput{SessionID: "sess_skill", TriggerEventID: "sevt_skill"},
	)
	require.NoError(t, err)
	require.Contains(t, unsupported.FatalError, "configured sandbox provider")
}

func TestPrepareTurn_SelectsThreadAgentRuntimeConfiguration(t *testing.T) {
	root := domain.SessionSkillsRoot + "/.agents/0123456789abcdef01234567"
	base := newFakeSource([]domain.Event{{
		ID: "sevt_child_skill", SessionID: "sess_child_skill",
		ThreadID: "sthr_child", Sequence: 1, Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "review the report"},
		}},
	}})
	childSystem := "You are the child reviewer."
	source := &threadConfiguredTranscriptFakeSource{
		configuredTranscriptFakeSource: &configuredTranscriptFakeSource{
			transcriptFakeSource: &transcriptFakeSource{fakeSource: base},
			session: domain.Session{
				ID: "sess_child_skill", Status: domain.StatusRunning,
				AgentSnapshot: domain.Agent{
					ID: "agent_primary", Version: 1, Name: "coordinator",
					Model: domain.Model{ID: "primary-model"},
				},
			},
		},
		thread: domain.SessionThread{
			ID: "sthr_child", SessionID: "sess_child_skill",
			Agent: domain.Agent{
				ID: "agent_child", Version: 2, Name: "reviewer",
				Model: domain.Model{ID: "child-model"}, System: &childSystem,
				Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
				Skills: []domain.SkillReference{{
					Type: "custom", SkillID: "skill_child", Version: "200",
				}},
			},
		},
		runtime: domain.SkillRuntime{
			Root: root,
			Versions: []domain.SkillVersion{{
				SkillID: "skill_child", Version: "200", Name: "child-review",
				Description: "Review reports", UncompressedSizeBytes: 1024,
			}},
		},
	}
	prepared, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).WithSkillRuntimeSupported(true).PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID: "sess_child_skill", TriggerEventID: "sevt_child_skill",
		},
	)
	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.Equal(t, "sthr_child", prepared.ThreadID)
	require.Equal(t, root, prepared.SkillRuntimeRoot)
	require.Equal(t, "child-model", prepared.Request.Model)
	require.Contains(t, prepared.Request.System, childSystem)
	require.Contains(
		t, prepared.Request.System, root+"/child-review/SKILL.md",
	)
}

func TestPrepareTurn_ReattachesInvokedSkillFromTranscriptAfterWorkerRestart(t *testing.T) {
	processedAt := time.Now().UTC()
	base := newFakeSource([]domain.Event{
		{
			ID: "sevt_prior_skill", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "first request",
			}}},
			ProcessedAt: &processedAt,
		},
		{
			ID: "sevt_current_skill", Sequence: 2, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "continue the report",
			}}},
		},
	})
	injection := agentruntime.RuntimeSkillInjection(
		"report-tools",
		[]byte("---\nname: report-tools\ndescription: Analyze reports\n---\ncanonical workflow\n"),
	)
	source := &configuredTranscriptFakeSource{
		transcriptFakeSource: &transcriptFakeSource{
			fakeSource: base,
			transcript: domain.ProviderTranscript{
				TriggerEventIDs: []string{"sevt_prior_skill"},
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{
						Type: "text", Text: "first request",
					}}},
					{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{
						Type: "tool_use", ToolUseID: "provider_skill",
						ToolName: agentruntime.RuntimeSkillToolName,
						Input:    map[string]any{"skill": "report-tools"},
					}}},
					{Role: domain.RoleUser, Content: []domain.ContentBlock{
						{Type: "tool_result", ToolResultFor: "provider_skill", Text: "Launching skill: report-tools"},
						injection,
					}},
					{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{
						Type: "text", Text: strings.Repeat("later analysis ", 6000),
					}}},
				},
			},
		},
		session: domain.Session{
			ID: "sess_restart_skill", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
				Skills: []domain.SkillReference{{
					Type: "custom", SkillID: "skill_reports", Version: "100",
				}},
			},
		},
		skills: []domain.SkillVersion{{
			SkillID: "skill_reports", Version: "100", Name: "report-tools",
			Description: "Analyze reports", UncompressedSizeBytes: 1024,
		}},
	}
	prepared, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).WithSkillRuntimeSupported(true).WithContextTokenBudget(9000).PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID: "sess_restart_skill", TriggerEventID: "sevt_current_skill",
		},
	)
	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.True(t, prepared.UsesProviderTranscript)
	require.True(t, prepared.ContextProjection.Compacted)
	require.Contains(t, agentruntime.LoadedRuntimeSkills(prepared.Request.Messages), "report-tools")
	lastMessage := prepared.Request.Messages[len(prepared.Request.Messages)-1]
	require.Equal(
		t,
		"continue the report",
		lastMessage.Content[len(lastMessage.Content)-1].Text,
	)
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
		ActionEventType:   domain.EvAgentCustomToolUse,
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
