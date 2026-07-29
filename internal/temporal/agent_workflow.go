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
	return runWorkflowTurnVersioned(actx, sessionID, triggerEventID, nil, true)
}

// runWorkflowTurnV1 freezes the forward behavior of Workflow histories that
// recorded workflowAgentLoopV1. Those histories have no pending-action selector,
// so they must continue rejecting client-action tool batches instead of
// installing a barrier they could never resume.
func runWorkflowTurnV1(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
) (RunTurnResult, error) {
	return runWorkflowTurnVersioned(actx, sessionID, triggerEventID, nil, false)
}

func runWorkflowTurnWithResolutions(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
) (RunTurnResult, error) {
	return runWorkflowTurnVersioned(
		actx,
		sessionID,
		triggerEventID,
		resolutionEventIDs,
		true,
	)
}

func runWorkflowTurnVersioned(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
	clientActions bool,
) (RunTurnResult, error) {
	var prepared PrepareTurnResult
	if err := workflow.ExecuteActivity(actx, ActivityPrepareTurn, PrepareTurnInput{
		SessionID:          sessionID,
		TriggerEventID:     triggerEventID,
		ResolutionEventIDs: resolutionEventIDs,
	}).Get(actx, &prepared); err != nil {
		return RunTurnResult{}, err
	}
	if prepared.AlreadyCompleted {
		if prepared.Terminated {
			return RunTurnResult{Terminated: true, Disposition: TurnTerminated}, nil
		}
		return RunTurnResult{Disposition: TurnCompleted}, nil
	}

	var (
		output    []domain.EventDraft
		attemptID string
		ordinal   int
	)
	if prepared.FatalError != "" {
		return terminateWorkflowTurn(
			actx, sessionID, triggerEventID, output, attemptID,
			resolutionEventIDs, prepared.FatalError,
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

	// A fully claimed barrier resumes as one logical tool-result round. The
	// preparation Activity removed the parked action/result events from the base
	// causal projection and returned their server-owned normalized payloads in
	// original action order. Reconstruct every tool_use first, then every result,
	// so a mixed custom/confirmation barrier remains a legal Messages API pair.
	if len(prepared.ResumeActions) > 0 {
		actionBlocks := make([]domain.ContentBlock, 0, len(prepared.ResumeActions))
		resultBlocks := make([]domain.ContentBlock, 0, len(prepared.ResumeActions))
		for _, action := range prepared.ResumeActions {
			definition, ok := toolsByName[action.ToolName]
			if !ok {
				return terminateWorkflowTurn(
					actx, sessionID, triggerEventID, output, attemptID,
					resolutionEventIDs,
					"pending action names a tool that is not enabled: "+action.ToolName,
				)
			}
			actionBlocks = append(actionBlocks, domain.ContentBlock{
				Type:      "tool_use",
				ToolUseID: action.ActionEventID,
				ToolName:  action.ToolName,
				Input:     action.Input,
			})

			content := action.Content
			isError := action.IsError
			switch action.Kind {
			case domain.PendingCustomToolResult:
				if definition.Kind != TurnToolCustom {
					return terminateWorkflowTurn(
						actx, sessionID, triggerEventID, output, attemptID,
						resolutionEventIDs,
						"custom tool result does not reference a custom tool",
					)
				}
			case domain.PendingToolConfirmation:
				if definition.Kind != TurnToolBuiltin ||
					definition.Permission.Type != "always_ask" {
					return terminateWorkflowTurn(
						actx, sessionID, triggerEventID, output, attemptID,
						resolutionEventIDs,
						"tool confirmation does not reference an always_ask built-in",
					)
				}
				if action.Confirmation == "deny" {
					text := "Tool call denied by user."
					if action.DenyMessage != "" {
						text += " " + action.DenyMessage
					}
					content = []any{map[string]any{"type": "text", "text": text}}
					isError = true
				} else {
					if action.ToolStepID == "" || prepared.AttemptID == "" {
						return terminateWorkflowTurn(
							actx, sessionID, triggerEventID, output, attemptID,
							resolutionEventIDs,
							"allowed confirmation has no durable operation id",
						)
					}
					attemptID = prepared.AttemptID
					var executed ExecuteToolResult
					if err := workflow.ExecuteActivity(actx, ActivityExecuteTool, ExecuteToolInput{
						SessionID:      sessionID,
						TriggerEventID: triggerEventID,
						AttemptID:      attemptID,
						Ordinal:        ordinal,
						ToolUseEventID: action.ActionEventID,
						ToolStepID:     action.ToolStepID,
						ToolName:       action.ToolName,
						Input:          action.Input,
					}).Get(actx, &executed); err != nil {
						return RunTurnResult{}, err
					}
					ordinal++
					if executed.FatalError != "" {
						return terminateWorkflowTurn(
							actx, sessionID, triggerEventID, output, attemptID,
							resolutionEventIDs, executed.FatalError,
						)
					}
					if executed.Ambiguous {
						return terminateWorkflowTurn(
							actx, sessionID, triggerEventID, output, attemptID,
							resolutionEventIDs,
							"a confirmed tool began executing but no trustworthy result was recorded; "+
								"the side effect will not be retried",
						)
					}
					content = executed.Result.Content
					isError = executed.Result.IsError
				}
				output = append(output, domain.EventDraft{
					Type: domain.EvAgentToolResult,
					Payload: map[string]any{
						"tool_use_id": action.ActionEventID,
						"content":     content,
						"is_error":    isError,
					},
				})
			default:
				return terminateWorkflowTurn(
					actx, sessionID, triggerEventID, output, attemptID,
					resolutionEventIDs, "unknown pending action kind",
				)
			}
			resultBlocks = append(resultBlocks, domain.ContentBlock{
				Type:          "tool_result",
				ToolResultFor: action.ActionEventID,
				Text:          agentruntime.FlattenResultText(content),
				IsError:       isError,
			})
		}
		messages = agentruntime.AppendMerging(messages, []domain.Message{
			{Role: domain.RoleAssistant, Content: actionBlocks},
			{Role: domain.RoleUser, Content: resultBlocks},
		})
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
				actx, sessionID, triggerEventID, output, attemptID,
				resolutionEventIDs, called.FatalError,
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
					resolutionEventIDs,
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
				nil, resolutionEventIDs,
			)
		}
		if prepared.AttemptID == "" {
			return terminateWorkflowTurn(
				actx,
				sessionID,
				triggerEventID,
				output,
				attemptID,
				resolutionEventIDs,
				"tool-using turn has no durable attempt id",
			)
		}
		stepIDsByEvent := make(map[string]string, len(called.ToolSteps))
		for _, planned := range called.ToolSteps {
			stepIDsByEvent[planned.ToolUseEventID] = planned.ToolStepID
		}

		// Validate the whole model batch before executing its first side effect.
		// The classification itself is durable Activity output (prepared.Tools);
		// Workflow code never consults a mutable registry during replay.
		for _, use := range toolUses {
			if stepIDsByEvent[use.ToolUseID] == "" {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					resolutionEventIDs,
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
					resolutionEventIDs,
					"model requested a tool that is not enabled: "+use.ToolName,
				)
			}
			if !clientActions &&
				(definition.Kind != TurnToolBuiltin ||
					definition.Permission.Type != "always_allow") {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					resolutionEventIDs,
					"client-action tools are not supported on the Temporal path yet",
				)
			}
			if definition.Kind == TurnToolBuiltin &&
				definition.Permission.Type != "always_allow" &&
				definition.Permission.Type != "always_ask" {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					resolutionEventIDs,
					"built-in tool has unsupported permission policy: "+
						definition.Permission.Type,
				)
			}
		}

		if !clientActions {
			// Freeze v1 output construction as well as its rejection behavior.
			// Existing v1 histories emitted each always-allow use/result pair
			// interleaved, and their CompleteWorkflowTurn Activity input must stay
			// byte-for-byte compatible when replayed.
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
						actx, sessionID, triggerEventID, output, attemptID,
						resolutionEventIDs, executed.FatalError,
					)
				}
				if executed.Ambiguous {
					return terminateWorkflowTurn(
						actx,
						sessionID,
						triggerEventID,
						output,
						attemptID,
						resolutionEventIDs,
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
			messages = agentruntime.AppendMerging(messages, []domain.Message{
				{Role: domain.RoleAssistant, Content: called.Response.Content},
				{Role: domain.RoleUser, Content: resultBlocks},
			})
			continue
		}

		// Commit every use before any result, matching the logical assistant
		// tool-use round. Always-allow built-ins execute now; custom tools and
		// always_ask built-ins become one durable client-action barrier.
		type executableUse struct {
			use domain.ContentBlock
		}
		var (
			executable   []executableUse
			pendingIDs   []string
			resultDrafts []domain.EventDraft
		)
		for _, use := range toolUses {
			definition := toolsByName[use.ToolName]
			draft := domain.EventDraft{
				ID: use.ToolUseID,
				Payload: map[string]any{
					"name":  use.ToolName,
					"input": use.Input,
				},
			}
			switch {
			case definition.Kind == TurnToolCustom:
				draft.Type = domain.EvAgentCustomToolUse
				pendingIDs = append(pendingIDs, use.ToolUseID)
			case definition.Permission.Type == "always_ask":
				draft.Type = domain.EvAgentToolUse
				draft.Payload["evaluated_permission"] = "ask"
				pendingIDs = append(pendingIDs, use.ToolUseID)
			default:
				draft.Type = domain.EvAgentToolUse
				executable = append(executable, executableUse{use: use})
			}
			output = append(output, draft)
		}

		resultBlocks := make([]domain.ContentBlock, 0, len(toolUses))
		for _, executable := range executable {
			use := executable.use
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
					actx, sessionID, triggerEventID, output, attemptID,
					resolutionEventIDs, executed.FatalError,
				)
			}
			if executed.Ambiguous {
				return terminateWorkflowTurn(
					actx,
					sessionID,
					triggerEventID,
					output,
					attemptID,
					resolutionEventIDs,
					"a tool began executing but no trustworthy result was recorded; "+
						"the side effect will not be retried",
				)
			}

			resultDrafts = append(resultDrafts, domain.EventDraft{
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
		output = append(output, resultDrafts...)

		if len(pendingIDs) > 0 {
			return completeWorkflowTurn(
				actx,
				sessionID,
				triggerEventID,
				output,
				attemptID,
				pendingIDs,
				resolutionEventIDs,
			)
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
	return completeWorkflowTurn(
		actx, sessionID, triggerEventID, output, attemptID, nil, resolutionEventIDs,
	)
}

func completeWorkflowTurn(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	attemptID string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
) (RunTurnResult, error) {
	stopReason := map[string]any{"type": "end_turn"}
	if len(pendingActionEventIDs) > 0 {
		stopReason = map[string]any{
			"type":      "requires_action",
			"event_ids": pendingActionEventIDs,
		}
	}
	output = append(output, domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": stopReason},
	})
	input := CompleteWorkflowTurnInput{
		SessionID:             sessionID,
		TriggerEventID:        triggerEventID,
		Output:                output,
		Status:                domain.StatusIdle,
		AttemptID:             attemptID,
		PendingActionEventIDs: pendingActionEventIDs,
		ResolutionEventIDs:    resolutionEventIDs,
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
	resolutionEventIDs []string,
	message string,
) (RunTurnResult, error) {
	output = append(output,
		domain.EventDraft{Type: domain.EvSessionError, Payload: map[string]any{
			"error": map[string]any{"type": "api_error", "message": message},
		}},
		domain.EventDraft{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
	)
	input := CompleteWorkflowTurnInput{
		SessionID:          sessionID,
		TriggerEventID:     triggerEventID,
		Output:             output,
		Status:             domain.StatusTerminated,
		AttemptID:          attemptID,
		ResolutionEventIDs: resolutionEventIDs,
	}
	if attemptID != "" {
		input.AttemptState = domain.RunAttemptFailed
		input.AttemptError = &message
	}
	var result RunTurnResult
	err := workflow.ExecuteActivity(actx, ActivityCompleteWorkflowTurn, input).Get(actx, &result)
	return result, err
}
