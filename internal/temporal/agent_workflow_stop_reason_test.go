package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

func TestClassifyProviderResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		stopReason  string
		toolUses    int
		want        providerResponseDisposition
		wantFailure string
	}{
		{name: "end turn", stopReason: "end_turn", want: providerResponseComplete},
		{name: "stop sequence", stopReason: "stop_sequence", want: providerResponseComplete},
		{name: "refusal", stopReason: "refusal", want: providerResponseComplete},
		{name: "context limit", stopReason: "model_context_window_exceeded", want: providerResponseComplete},
		{name: "tool use", stopReason: "tool_use", toolUses: 1, want: providerResponseExecuteTools},
		{name: "pause turn", stopReason: "pause_turn", want: providerResponseContinuePause},
		{name: "output limit", stopReason: "max_tokens", want: providerResponseContinueOutput},
		{name: "tool reason without tool", stopReason: "tool_use", wantFailure: "without a client tool_use"},
		{name: "pause with client tool", stopReason: "pause_turn", toolUses: 1, wantFailure: "with a client tool_use"},
		{name: "truncated client tool", stopReason: "max_tokens", toolUses: 1, wantFailure: "potentially incomplete"},
		{name: "final reason with tool", stopReason: "end_turn", toolUses: 1, wantFailure: "with a client tool_use"},
		{name: "missing reason", wantFailure: "no stop_reason"},
		{name: "unknown reason", stopReason: "future_reason", wantFailure: `unsupported stop_reason "future_reason"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, failure := classifyProviderResponse(tt.stopReason, tt.toolUses)
			require.Equal(t, tt.want, got)
			if tt.wantFailure == "" {
				require.Empty(t, failure)
			} else {
				require.Contains(t, failure, tt.wantFailure)
			}
		})
	}
}

func TestWorkflowTurn_ContinuesPauseTurnWithExactProviderContent(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	initial := []domain.Message{{
		Role: domain.RoleUser,
		Content: []domain.ContentBlock{{
			Type: "text", Text: "research the topic",
		}},
	}}
	serverToolBlock := domain.ContentBlock{
		Type: "server_tool_use",
		Raw:  json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"topic"}}`),
	}
	pauseContent := []domain.ContentBlock{
		{Type: "text", Text: "I am still searching."},
		serverToolBlock,
	}
	secondPauseContent := []domain.ContentBlock{
		{
			Type: "web_search_tool_result",
			Raw:  json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"evidence"}]}`),
		},
		{Type: "text", Text: "I am checking the remaining sources."},
		{
			Type: "server_tool_use",
			Raw:  json.RawMessage(`{"type":"server_tool_use","id":"srvtoolu_2","name":"web_search","input":{"query":"topic evidence"}}`),
		},
	}

	var mu sync.Mutex
	var requests []model.Request
	var completed CompleteWorkflowTurnInput
	registerWorkflowTurnActivities(
		env,
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{
				ThreadID:               "sthr_pause",
				UsesProviderTranscript: true,
				TranscriptDelta:        initial,
				Request: model.Request{
					Model:    "test-model",
					Messages: initial,
				},
			}, nil
		},
		func(_ context.Context, in CallModelInput) (CallModelResult, error) {
			mu.Lock()
			requests = append(requests, in.Request)
			call := len(requests)
			mu.Unlock()
			if call == 1 {
				return CallModelResult{
					ModelRequestStartID: in.ModelRequestStartID,
					ModelRequestEndID:   in.ModelRequestEndID,
					MessageEventID:      "sevt_pause_message",
					Response: model.Response{
						StopReason: "pause_turn",
						Content:    pauseContent,
					},
				}, nil
			}
			if call == 2 {
				return CallModelResult{
					ModelRequestStartID: in.ModelRequestStartID,
					ModelRequestEndID:   in.ModelRequestEndID,
					MessageEventID:      "sevt_second_pause_message",
					Response: model.Response{
						StopReason: "pause_turn",
						Content:    secondPauseContent,
					},
				}, nil
			}
			return CallModelResult{
				ModelRequestStartID: in.ModelRequestStartID,
				ModelRequestEndID:   in.ModelRequestEndID,
				MessageEventID:      "sevt_pause_done",
				Response: model.Response{
					StopReason: "end_turn",
					Content:    []domain.ContentBlock{{Type: "text", Text: "Research complete."}},
				},
			}, nil
		},
		func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
			t.Fatal("pause_turn must not execute a client tool")
			return ExecuteToolResult{}, nil
		},
		func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
			completed = in
			return RunTurnResult{Disposition: TurnCompleted}, nil
		},
	)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sesn_pause", TriggerEventID: "sevt_user",
	})
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 3)
	require.Equal(t, initial, requests[0].Messages)
	require.Equal(t, []domain.Message{
		initial[0],
		{Role: domain.RoleAssistant, Content: pauseContent},
	}, requests[1].Messages)
	require.Equal(t, []domain.Message{
		initial[0],
		{Role: domain.RoleAssistant, Content: append(
			append([]domain.ContentBlock(nil), pauseContent...),
			secondPauseContent...,
		)},
	}, requests[2].Messages, "a repeated pause keeps the server-tool use/result chain paired")
	require.Equal(t, domain.StatusIdle, completed.Status)
	require.Equal(t, []string{
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvSessionStatusIdle,
	}, draftTypes(completed.Output))
	require.Len(t, completed.TranscriptDelta, 2)
	require.Equal(t, domain.RoleAssistant, completed.TranscriptDelta[1].Role)
	require.Equal(t, append(
		append(
			append([]domain.ContentBlock(nil), pauseContent...),
			secondPauseContent...,
		),
		domain.ContentBlock{Type: "text", Text: "Research complete."},
	), completed.TranscriptDelta[1].Content)
}

func TestWorkflowTurn_ContinuesMaxTokensWithInternalRecoveryMessage(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	initial := []domain.Message{{
		Role:    domain.RoleUser,
		Content: []domain.ContentBlock{{Type: "text", Text: "write a long answer"}},
	}}
	partial := []domain.ContentBlock{{Type: "text", Text: "First half"}}

	var mu sync.Mutex
	var requests []model.Request
	var completed CompleteWorkflowTurnInput
	registerWorkflowTurnActivities(
		env,
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{
				UsesProviderTranscript: true,
				TranscriptDelta:        initial,
				Request:                model.Request{Model: "test-model", Messages: initial},
			}, nil
		},
		func(_ context.Context, in CallModelInput) (CallModelResult, error) {
			mu.Lock()
			requests = append(requests, in.Request)
			call := len(requests)
			mu.Unlock()
			response := model.Response{
				StopReason: "max_tokens",
				Content:    partial,
			}
			messageID := "sevt_partial"
			if call == 2 {
				response = model.Response{
					StopReason: "end_turn",
					Content:    []domain.ContentBlock{{Type: "text", Text: "Second half"}},
				}
				messageID = "sevt_complete"
			}
			return CallModelResult{
				ModelRequestStartID: in.ModelRequestStartID,
				ModelRequestEndID:   in.ModelRequestEndID,
				MessageEventID:      messageID,
				Response:            response,
			}, nil
		},
		func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
			t.Fatal("max_tokens recovery must not execute a client tool")
			return ExecuteToolResult{}, nil
		},
		func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
			completed = in
			return RunTurnResult{Disposition: TurnCompleted}, nil
		},
	)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sesn_max_tokens", TriggerEventID: "sevt_user",
	})
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	require.Len(t, requests[1].Messages, 3)
	require.Equal(t, domain.RoleAssistant, requests[1].Messages[1].Role)
	require.Equal(t, partial, requests[1].Messages[1].Content)
	require.Equal(t, domain.RoleUser, requests[1].Messages[2].Role)
	require.Contains(t, requests[1].Messages[2].Content[0].Text, "Continue directly")
	require.Equal(t, domain.StatusIdle, completed.Status)
	require.Equal(t, []string{
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvSessionStatusIdle,
	}, draftTypes(completed.Output))
	require.Len(t, completed.TranscriptDelta, 4)
	require.Equal(t, requests[1].Messages[2], completed.TranscriptDelta[2])
}

func TestWorkflowTurn_RejectsContradictoryStopReason(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	var completed CompleteWorkflowTurnInput
	registerWorkflowTurnActivities(
		env,
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{Request: model.Request{Model: "test-model"}}, nil
		},
		func(_ context.Context, in CallModelInput) (CallModelResult, error) {
			return CallModelResult{
				ModelRequestStartID: in.ModelRequestStartID,
				ModelRequestEndID:   in.ModelRequestEndID,
				MessageEventID:      "sevt_invalid_stop",
				Response: model.Response{
					StopReason: "tool_use",
					Content:    []domain.ContentBlock{{Type: "text", Text: "invalid response"}},
				},
			}, nil
		},
		func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
			t.Fatal("invalid response must not execute a client tool")
			return ExecuteToolResult{}, nil
		},
		func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
			completed = in
			return RunTurnResult{Disposition: TurnTerminated}, nil
		},
	)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sesn_invalid_stop", TriggerEventID: "sevt_user",
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, domain.StatusTerminated, completed.Status)
	require.Equal(t, []string{
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvSessionError,
		domain.EvSessionStatusTerminated,
	}, draftTypes(completed.Output))
	errorPayload := completed.Output[2].Payload["error"].(map[string]any)
	require.Equal(t, "model_request_failed_error", errorPayload["type"])
	require.Contains(t, errorPayload["message"], "without a client tool_use")
}

func TestWorkflowTurn_BoundsMaxTokensContinuation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	var mu sync.Mutex
	calls := 0
	var completed CompleteWorkflowTurnInput
	registerWorkflowTurnActivities(
		env,
		func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
			return PrepareTurnResult{Request: model.Request{Model: "test-model"}}, nil
		},
		func(_ context.Context, in CallModelInput) (CallModelResult, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return CallModelResult{
				ModelRequestStartID: in.ModelRequestStartID,
				ModelRequestEndID:   in.ModelRequestEndID,
				MessageEventID:      fmt.Sprintf("sevt_truncated_%d", calls),
				Response: model.Response{
					StopReason: "max_tokens",
					Content: []domain.ContentBlock{{
						Type: "text", Text: fmt.Sprintf("part %d", calls),
					}},
				},
			}, nil
		},
		func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
			t.Fatal("max_tokens recovery must not execute a client tool")
			return ExecuteToolResult{}, nil
		},
		func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
			completed = in
			return RunTurnResult{Disposition: TurnTerminated}, nil
		},
	)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sesn_max_tokens_bound", TriggerEventID: "sevt_user",
	})
	require.NoError(t, env.GetWorkflowError())
	mu.Lock()
	require.Equal(t, maxOutputContinuations+1, calls)
	mu.Unlock()
	require.Equal(t, domain.StatusTerminated, completed.Status)
	require.Equal(t, domain.EvSessionError, completed.Output[len(completed.Output)-2].Type)
	errorPayload := completed.Output[len(completed.Output)-2].Payload["error"].(map[string]any)
	require.Contains(t, errorPayload["message"], "max_tokens continuation limit")
}
