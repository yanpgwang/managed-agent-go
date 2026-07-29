package temporal

import (
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// maxWorkflowToolRounds matches AgentCore's existing bounded loop. Keeping a
// hard deterministic bound prevents a model that continually requests tools
// from growing Workflow history forever within one public turn.
const maxWorkflowToolRounds = 20

// runWorkflowTurn owns the plan-act-observe loop in deterministic Workflow
// code. Every model call and every tool call is an Activity, so each completed
// response/result is independently recorded in Temporal history and replay
// resumes at the next unfinished step.
func runWorkflowTurn(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
) (RunTurnResult, error) {
	var prepared PrepareTurnResult
	if err := workflow.ExecuteActivity(actx, ActivityPrepareTurn, PrepareTurnInput{
		SessionID: sessionID, TriggerEventID: triggerEventID,
	}).Get(actx, &prepared); err != nil {
		return RunTurnResult{}, err
	}
	if prepared.AlreadyCompleted {
		return RunTurnResult{Terminated: prepared.Terminated}, nil
	}

	var (
		output    []domain.EventDraft
		attemptID string
		ordinal   int
	)
	if prepared.FatalError != "" {
		return terminateWorkflowTurn(
			actx, sessionID, triggerEventID, output, attemptID, prepared.FatalError,
		)
	}

	messages := append([]domain.Message(nil), prepared.Request.Messages...)
	toolsByName := make(map[string]TurnTool, len(prepared.Tools))
	for _, tool := range prepared.Tools {
		// Built-ins are listed before custom tools. Preserve the first owner when
		// names collide, matching AgentCore's built-in-first dispatch.
		if _, exists := toolsByName[tool.Name]; !exists {
			toolsByName[tool.Name] = tool
		}
	}

	for round := 0; round < maxWorkflowToolRounds; round++ {
		request := prepared.Request
		request.Messages = messages

		var called CallModelResult
		if err := workflow.ExecuteActivity(actx, ActivityCallModel, CallModelInput{
			SessionID: sessionID,
			Request:   request,
		}).Get(actx, &called); err != nil {
			return RunTurnResult{}, err
		}
		if called.FatalError != "" {
			return terminateWorkflowTurn(
				actx, sessionID, triggerEventID, output, attemptID, called.FatalError,
			)
		}

		if content := agentruntime.TextBlocksToContent(called.Response.Content); len(content) > 0 {
			if called.MessageEventID == "" {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					"model response text has no durable public event id",
				)
			}
			output = append(output, domain.EventDraft{
				ID:      called.MessageEventID,
				Type:    domain.EvAgentMessage,
				Payload: map[string]any{"content": content},
			})
		}

		var toolUses []domain.ContentBlock
		for _, block := range called.Response.Content {
			if block.Type == "tool_use" {
				toolUses = append(toolUses, block)
			}
		}
		if len(toolUses) == 0 {
			return completeWorkflowTurn(
				actx, sessionID, triggerEventID, output, attemptID,
			)
		}
		if prepared.AttemptID == "" {
			return terminateWorkflowTurn(
				actx,
				sessionID,
				triggerEventID,
				output,
				attemptID,
				"tool-using turn has no durable attempt id",
			)
		}
		stepIDsByEvent := make(map[string]string, len(called.ToolSteps))
		for _, planned := range called.ToolSteps {
			stepIDsByEvent[planned.ToolUseEventID] = planned.ToolStepID
		}

		// Validate the whole model batch before executing its first side effect.
		// A custom or approval-gated tool is not supported on this first slice;
		// refusing the batch up front avoids executing an earlier built-in and
		// only then discovering that the same model response cannot complete.
		for _, use := range toolUses {
			if stepIDsByEvent[use.ToolUseID] == "" {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					"model tool request has no durable operation id",
				)
			}
			definition, ok := toolsByName[use.ToolName]
			if !ok {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					"model requested a tool that is not enabled: "+use.ToolName,
				)
			}
			if definition.Kind != TurnToolBuiltin || definition.Permission.Type != "always_allow" {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					"client-action tools are not supported on the Temporal path yet",
				)
			}
		}

		resultBlocks := make([]domain.ContentBlock, 0, len(toolUses))
		for _, use := range toolUses {
			output = append(output, domain.EventDraft{
				ID:   use.ToolUseID,
				Type: domain.EvAgentToolUse,
				Payload: map[string]any{
					"name":  use.ToolName,
					"input": use.Input,
				},
			})

			attemptID = prepared.AttemptID
			var executed ExecuteToolResult
			if err := workflow.ExecuteActivity(actx, ActivityExecuteTool, ExecuteToolInput{
				SessionID:      sessionID,
				TriggerEventID: triggerEventID,
				AttemptID:      attemptID,
				Ordinal:        ordinal,
				ToolUseEventID: use.ToolUseID,
				ToolStepID:     stepIDsByEvent[use.ToolUseID],
				ToolName:       use.ToolName,
				Input:          use.Input,
			}).Get(actx, &executed); err != nil {
				return RunTurnResult{}, err
			}
			ordinal++
			if executed.FatalError != "" {
				return terminateWorkflowTurn(
					actx, sessionID, triggerEventID, output, attemptID, executed.FatalError,
				)
			}
			if executed.Ambiguous {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					"a tool began executing but no trustworthy result was recorded; "+
						"the side effect will not be retried",
				)
			}

			output = append(output, domain.EventDraft{
				Type: domain.EvAgentToolResult,
				Payload: map[string]any{
					"tool_use_id": use.ToolUseID,
					"content":     executed.Result.Content,
					"is_error":    executed.Result.IsError,
				},
			})
			resultBlocks = append(resultBlocks, domain.ContentBlock{
				Type:          "tool_result",
				ToolResultFor: use.ToolUseID,
				Text:          agentruntime.FlattenResultText(executed.Result.Content),
				IsError:       executed.Result.IsError,
			})
		}

		// Preserve the model's exact assistant round, including text emitted
		// alongside tool_use blocks, then append the paired tool results.
		messages = agentruntime.AppendMerging(messages, []domain.Message{
			{Role: domain.RoleAssistant, Content: called.Response.Content},
			{Role: domain.RoleUser, Content: resultBlocks},
		})
	}

	// Preserve the legacy bounded-loop behavior: reaching the cap closes the
	// public turn normally rather than allowing unbounded Workflow history.
	return completeWorkflowTurn(actx, sessionID, triggerEventID, output, attemptID)
}

func completeWorkflowTurn(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	attemptID string,
) (RunTurnResult, error) {
	output = append(output, domain.EventDraft{
		Type: domain.EvSessionStatusIdle,
		Payload: map[string]any{
			"stop_reason": map[string]any{"type": "end_turn"},
		},
	})
	input := CompleteWorkflowTurnInput{
		SessionID:      sessionID,
		TriggerEventID: triggerEventID,
		Output:         output,
		Status:         domain.StatusIdle,
		AttemptID:      attemptID,
	}
	if attemptID != "" {
		input.AttemptState = domain.RunAttemptCompleted
	}
	var result RunTurnResult
	err := workflow.ExecuteActivity(actx, ActivityCompleteWorkflowTurn, input).Get(actx, &result)
	return result, err
}

func terminateWorkflowTurn(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	attemptID string,
	message string,
) (RunTurnResult, error) {
	output = append(output,
		domain.EventDraft{Type: domain.EvSessionError, Payload: map[string]any{
			"error": map[string]any{"type": "api_error", "message": message},
		}},
		domain.EventDraft{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
	)
	input := CompleteWorkflowTurnInput{
		SessionID:      sessionID,
		TriggerEventID: triggerEventID,
		Output:         output,
		Status:         domain.StatusTerminated,
		AttemptID:      attemptID,
	}
	if attemptID != "" {
		input.AttemptState = domain.RunAttemptFailed
		input.AttemptError = &message
	}
	var result RunTurnResult
	err := workflow.ExecuteActivity(actx, ActivityCompleteWorkflowTurn, input).Get(actx, &result)
	return result, err
}
