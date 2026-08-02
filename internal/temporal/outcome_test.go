package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

type outcomePrepareSource struct {
	*fakeSource
	session domain.Session
}

func (s *outcomePrepareSource) GetSession(context.Context, string) (domain.Session, error) {
	return s.session, nil
}

func TestPrepareTurnRunsReceiptProcessedActiveOutcome(t *testing.T) {
	processedAt := time.Now().UTC()
	trigger := domain.Event{
		ID: "sevt_outcome", Sequence: 1, Type: domain.EvUserDefineOutcome,
		Payload: map[string]any{
			"outcome_id": "outc_1", "description": "produce report",
			"rubric":         map[string]any{"type": "text", "content": "has evidence"},
			"max_iterations": 3,
		},
		ProcessedAt: &processedAt,
	}
	source := &outcomePrepareSource{
		fakeSource: newFakeSource([]domain.Event{trigger}),
		session: domain.Session{
			ID: "sess_outcome", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{Model: domain.Model{ID: "model"}},
			Outcomes: []domain.OutcomeEvaluation{{
				OutcomeID: "outc_1", Description: "produce report", Result: "running",
			}},
		},
	}
	prepared, err := NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID: "sess_outcome", TriggerEventID: trigger.ID,
	})
	require.NoError(t, err)
	require.False(t, prepared.AlreadyCompleted)
	require.NotNil(t, prepared.Outcome)
	require.Equal(t, "outc_1", prepared.Outcome.OutcomeID)

	source.session.Outcomes[0].Result = "satisfied"
	prepared, err = NewActivities(
		nil, source, nil, nil, &testIDGen{},
	).PrepareTurn(context.Background(), PrepareTurnInput{
		SessionID: "sess_outcome", TriggerEventID: trigger.ID,
	})
	require.NoError(t, err)
	require.True(t, prepared.AlreadyCompleted)
}

type outcomeModel struct {
	request  model.Request
	response model.Response
}

func (m *outcomeModel) CreateMessage(_ context.Context, request model.Request) (model.Response, error) {
	m.request = request
	return m.response, nil
}

func (m *outcomeModel) CreateMessageStream(
	ctx context.Context,
	request model.Request,
	onDelta func(int, string),
) (model.Response, error) {
	response, err := m.CreateMessage(ctx, request)
	if err == nil && onDelta != nil {
		for _, block := range response.Content {
			if block.Type == "text" {
				onDelta(0, block.Text)
			}
		}
	}
	return response, err
}

func TestEvaluateOutcomeUsesIsolatedGraderContext(t *testing.T) {
	client := &outcomeModel{response: model.Response{
		Content: []domain.ContentBlock{{
			Type: "text",
			Text: `{"result":"satisfied","explanation":"all acceptance criteria are met"}`,
		}},
		Usage: domain.TokenUsage{InputTokens: 17, OutputTokens: 8},
	}}
	activities := NewActivities(
		client, nil, nil, nil, domain.NewSeqIDGen(),
	)

	got, err := activities.EvaluateOutcome(context.Background(), EvaluateOutcomeInput{
		SessionID: "sess_1", Model: "claude-sonnet", Effort: "high", Speed: "standard",
		Outcome: domain.OutcomeSpec{
			OutcomeID: "outc_1", Description: "ship the report",
			Rubric:        map[string]any{"type": "text", "content": "contains evidence"},
			MaxIterations: 3,
		},
		Candidate: []domain.Message{{
			Role:    domain.RoleAssistant,
			Content: []domain.ContentBlock{{Type: "text", Text: "report with evidence"}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "satisfied", got.Result)
	require.Equal(t, int64(17), got.Usage.InputTokens)
	require.NotEmpty(t, got.StartEventID)
	require.NotEmpty(t, got.EndEventID)

	request := client.request
	require.Equal(t, outcomeGraderSystem+" Return exactly one JSON object with "+
		`{"result":"satisfied|needs_revision|failed","explanation":"..."}.`, request.System)
	require.Len(t, request.Messages, 1)
	require.Equal(t, domain.RoleUser, request.Messages[0].Role)
	require.Contains(t, request.Messages[0].Content[0].Text, "ship the report")
}

func TestWorkflowTurnEvaluatesOutcomeAndAccountsUsage(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflowTurnHarness)

	prepare := func(context.Context, PrepareTurnInput) (PrepareTurnResult, error) {
		return PrepareTurnResult{
			Request: model.Request{
				Model: "claude-sonnet", Effort: "high", Speed: "standard",
				Messages: []domain.Message{{
					Role:    domain.RoleUser,
					Content: []domain.ContentBlock{{Type: "text", Text: "produce report"}},
				}},
			},
			Outcome: &domain.OutcomeSpec{
				OutcomeID: "outc_1", Description: "produce report",
				Rubric:        map[string]any{"type": "text", "content": "has evidence"},
				MaxIterations: 3,
			},
		}, nil
	}
	callModel := func(_ context.Context, in CallModelInput) (CallModelResult, error) {
		return CallModelResult{
			MessageEventID:      "sevt_answer",
			ModelRequestStartID: in.ModelRequestStartID,
			ModelRequestEndID:   in.ModelRequestEndID,
			Response: model.Response{
				Content: []domain.ContentBlock{{Type: "text", Text: "finished report"}},
				Usage: domain.TokenUsage{
					InputTokens: 10, OutputTokens: 4, Speed: "standard",
				},
			},
		}, nil
	}
	evaluate := func(context.Context, EvaluateOutcomeInput) (EvaluateOutcomeResult, error) {
		return EvaluateOutcomeResult{
			StartEventID: "sevt_eval_start", EndEventID: "sevt_eval_end",
			Result: "satisfied", Explanation: "rubric met",
			Usage: domain.TokenUsage{InputTokens: 3, OutputTokens: 2, Speed: "fast"},
		}, nil
	}
	executeTool := func(context.Context, ExecuteToolInput) (ExecuteToolResult, error) {
		t.Fatal("outcome without tool_use must not execute a tool")
		return ExecuteToolResult{}, nil
	}
	var completed CompleteWorkflowTurnInput
	complete := func(_ context.Context, in CompleteWorkflowTurnInput) (RunTurnResult, error) {
		completed = in
		return RunTurnResult{Disposition: TurnCompleted}, nil
	}
	registerWorkflowTurnActivities(env, prepare, callModel, executeTool, complete)
	env.RegisterActivityWithOptions(evaluate, activity.RegisterOptions{Name: ActivityEvaluateOutcome})

	env.ExecuteWorkflow(workflowTurnHarness, PrepareTurnInput{
		SessionID: "sess_1", TriggerEventID: "sevt_outcome",
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{
		domain.EvSpanModelRequestStart,
		domain.EvAgentMessage,
		domain.EvSpanModelRequestEnd,
		domain.EvSpanOutcomeEvaluationStart,
		domain.EvSpanOutcomeEvaluationEnd,
		domain.EvSessionStatusIdle,
	}, draftTypes(completed.Output))
	require.Equal(t, int64(13), completed.Usage.InputTokens)
	require.Equal(t, int64(6), completed.Usage.OutputTokens)
	end := completed.Output[4].Payload
	require.Equal(t, "sevt_eval_start", end["outcome_evaluation_start_id"])
	require.Equal(t, "satisfied", end["result"])
	require.Equal(t, "fast", end["usage"].(map[string]any)["speed"])
	modelEnd := completed.Output[2].Payload["model_usage"].(map[string]any)
	require.Equal(t, "standard", modelEnd["speed"])
}
