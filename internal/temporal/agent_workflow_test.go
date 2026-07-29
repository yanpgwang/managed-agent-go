package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

func workflowTurnHarness(ctx workflow.Context, in PrepareTurnInput) (RunTurnResult, error) {
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Millisecond,
			BackoffCoefficient: 1,
			MaximumInterval:    time.Millisecond,
		},
	})
	return runWorkflowTurn(actx, in.SessionID, in.TriggerEventID)
}

func registerWorkflowTurnActivities(
	env *testsuite.TestWorkflowEnvironment,
	prepare func(context.Context, PrepareTurnInput) (PrepareTurnResult, error),
	callModel func(context.Context, CallModelInput) (CallModelResult, error),
	executeTool func(context.Context, ExecuteToolInput) (ExecuteToolResult, error),
	complete func(context.Context, CompleteWorkflowTurnInput) (RunTurnResult, error),
) {
	env.RegisterActivityWithOptions(prepare, activity.RegisterOptions{Name: ActivityPrepareTurn})
	env.RegisterActivityWithOptions(callModel, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(executeTool, activity.RegisterOptions{Name: ActivityExecuteTool})
	env.RegisterActivityWithOptions(complete, activity.RegisterOptions{Name: ActivityCompleteWorkflowTurn})
}

func TestWorkflowTurn_PreservesTextAndMultipleTools(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	initial := []domain.Message{{
		Role: domain.RoleUser,
		Content: []domain.ContentBlock{{
			Type: "text", Text: "inspect both",
		}},
	}}
	prepare := func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
		return PrepareTurnResult{
			AttemptID: "ratm_1",
			Request: model.Request{
				Model:    "test-model",
				Messages: initial,
				Tools: []model.ToolSchema{
					{Name: "read", InputSchema: map[string]any{"type": "object"}},
					{Name: "grep", InputSchema: map[string]any{"type": "object"}},
				},
			},
			Tools: []TurnTool{
				{Name: "read", Kind: TurnToolBuiltin, Permission: domain.PermissionPolicy{Type: "always_allow"}},
				{Name: "grep", Kind: TurnToolBuiltin, Permission: domain.PermissionPolicy{Type: "always_allow"}},
			},
		}, nil
	}

	var mu sync.Mutex
	var modelRequests []model.Request
	callModel := func(_ context.Context, in CallModelInput) (CallModelResult, error) {
		mu.Lock()
		modelRequests = append(modelRequests, in.Request)
		call := len(modelRequests)
		mu.Unlock()
		if call == 1 {
			return CallModelResult{
				MessageEventID: "sevt_text_1",
				ToolSteps: []PlannedToolStep{
					{ToolUseEventID: "sevt_tool_1", ToolStepID: "tstep_1"},
					{ToolUseEventID: "sevt_tool_2", ToolStepID: "tstep_2"},
				},
				Response: model.Response{
					StopReason: "tool_use",
					Content: []domain.ContentBlock{
						{Type: "text", Text: "I will inspect both files."},
						{Type: "tool_use", ToolUseID: "sevt_tool_1", ToolName: "read", Input: map[string]any{"path": "a.txt"}},
						{Type: "tool_use", ToolUseID: "sevt_tool_2", ToolName: "grep", Input: map[string]any{"pattern": "x"}},
					},
				},
			}, nil
		}
		return CallModelResult{
			MessageEventID: "sevt_text_2",
			Response: model.Response{
				StopReason: "end_turn",
				Content:    []domain.ContentBlock{{Type: "text", Text: "done"}},
			},
		}, nil
	}

	var toolCalls []ExecuteToolInput
	executeTool := func(_ context.Context, in ExecuteToolInput) (ExecuteToolResult, error) {
		mu.Lock()
		toolCalls = append(toolCalls, in)
		mu.Unlock()
		return ExecuteToolResult{
			Result: domain.ToolStepResult{
				Content: []any{map[string]any{"type": "text", "text": in.ToolName + " result"}},
			},
		}, nil
	}

	var completed CompleteWorkflowTurnInput
	complete := func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
		mu.Lock()
		completed = in
		mu.Unlock()
		return RunTurnResult{}, nil
	}
	registerWorkflowTurnActivities(env, prepare, callModel, executeTool, complete)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sess_1", TriggerEventID: "sevt_trigger",
	})
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, modelRequests, 2)
	require.Len(t, toolCalls, 2)

	postTool := modelRequests[1].Messages
	require.Len(t, postTool, 3)
	require.Equal(t, domain.RoleAssistant, postTool[1].Role)
	require.Equal(t, []domain.ContentBlock{
		{Type: "text", Text: "I will inspect both files."},
		{Type: "tool_use", ToolUseID: "sevt_tool_1", ToolName: "read", Input: map[string]any{"path": "a.txt"}},
		{Type: "tool_use", ToolUseID: "sevt_tool_2", ToolName: "grep", Input: map[string]any{"pattern": "x"}},
	}, postTool[1].Content, "assistant text and both tool uses must stay in one model round")
	require.Equal(t, domain.RoleUser, postTool[2].Role)
	require.Equal(t, []domain.ContentBlock{
		{Type: "tool_result", ToolResultFor: "sevt_tool_1", Text: "read result"},
		{Type: "tool_result", ToolResultFor: "sevt_tool_2", Text: "grep result"},
	}, postTool[2].Content)

	var eventTypes []string
	for _, draft := range completed.Output {
		eventTypes = append(eventTypes, draft.Type)
	}
	require.Equal(t, []string{
		domain.EvAgentMessage,
		domain.EvAgentToolUse,
		domain.EvAgentToolResult,
		domain.EvAgentToolUse,
		domain.EvAgentToolResult,
		domain.EvAgentMessage,
		domain.EvSessionStatusIdle,
	}, eventTypes)
	require.Equal(t, "ratm_1", completed.AttemptID)
	require.Equal(t, domain.RunAttemptCompleted, completed.AttemptState)
}

func TestWorkflowTurn_ToolActivityRetryDoesNotRepeatModelStep(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	prepare := func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
		return PrepareTurnResult{
			AttemptID: "ratm_retry",
			Request: model.Request{
				Model:    "test-model",
				Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "run"}}}},
				Tools:    []model.ToolSchema{{Name: "bash", InputSchema: map[string]any{"type": "object"}}},
			},
			Tools: []TurnTool{{
				Name: "bash", Kind: TurnToolBuiltin,
				Permission: domain.PermissionPolicy{Type: "always_allow"},
			}},
		}, nil
	}

	var mu sync.Mutex
	modelCalls := 0
	callModel := func(_ context.Context, _ CallModelInput) (CallModelResult, error) {
		mu.Lock()
		defer mu.Unlock()
		modelCalls++
		if modelCalls == 1 {
			return CallModelResult{
				ToolSteps: []PlannedToolStep{{
					ToolUseEventID: "sevt_tool_retry", ToolStepID: "tstep_retry",
				}},
				Response: model.Response{
					StopReason: "tool_use",
					Content: []domain.ContentBlock{{
						Type: "tool_use", ToolUseID: "sevt_tool_retry", ToolName: "bash",
						Input: map[string]any{"command": "echo ok"},
					}},
				},
			}, nil
		}
		return CallModelResult{Response: model.Response{StopReason: "end_turn"}}, nil
	}

	toolAttempts := 0
	executeTool := func(_ context.Context, _ ExecuteToolInput) (ExecuteToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		toolAttempts++
		if toolAttempts == 1 {
			return ExecuteToolResult{}, errors.New("activity result acknowledgement lost")
		}
		return ExecuteToolResult{
			Result: domain.ToolStepResult{
				Content: []any{map[string]any{"type": "text", "text": "ok"}},
			},
		}, nil
	}
	var completed CompleteWorkflowTurnInput
	complete := func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
		mu.Lock()
		completed = in
		mu.Unlock()
		return RunTurnResult{}, nil
	}
	registerWorkflowTurnActivities(env, prepare, callModel, executeTool, complete)

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sess_retry", TriggerEventID: "sevt_trigger",
	})
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, toolAttempts, "Temporal should retry only the failed tool Activity")
	require.Equal(t, 2, modelCalls, "the first model response must remain in Workflow history")
	resultEvents := 0
	for _, draft := range completed.Output {
		if draft.Type == domain.EvAgentToolResult {
			resultEvents++
		}
	}
	require.Equal(t, 1, resultEvents)
}
