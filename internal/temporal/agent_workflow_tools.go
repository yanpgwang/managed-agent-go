package temporal

import (
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// turnFailure is a typed terminal reason produced by deterministic turn logic.
// It is deliberately distinct from error: errors retry an Activity, while a
// turnFailure is committed as an honest terminal Session outcome.
type turnFailure string

func failTurn(message string) turnFailure {
	return turnFailure(message)
}

// workflowTurnState owns the mutable, deterministic state threaded through one
// Workflow-owned turn. Capturing it here prevents every terminal branch from
// manually passing the same session, trigger, output, attempt, and resolution
// arguments in positional order.
type workflowTurnState struct {
	actx               workflow.Context
	sessionID          string
	triggerEventID     string
	resolutionEventIDs []string
	output             []domain.EventDraft
	attemptID          string
	ordinal            int
	interrupts         *turnInterruptWatcher
}

func (t *workflowTurnState) callModel(
	input CallModelInput,
) (CallModelResult, interruptibleActivityOutcome, error) {
	var called CallModelResult
	if t.interrupts == nil {
		err := workflow.ExecuteActivity(
			t.actx,
			ActivityCallModel,
			input,
		).Get(t.actx, &called)
		return called, interruptibleActivityOutcome{Completed: err == nil}, err
	}
	outcome, err := t.interrupts.executeActivity(
		ActivityCallModel,
		input,
		&called,
	)
	return called, outcome, err
}

func (t *workflowTurnState) executeTool(
	attemptID string,
	use domain.ContentBlock,
	stepID string,
) (ExecuteToolResult, interruptibleActivityOutcome, error) {
	t.attemptID = attemptID
	input := ExecuteToolInput{
		SessionID:      t.sessionID,
		TriggerEventID: t.triggerEventID,
		AttemptID:      attemptID,
		Ordinal:        t.ordinal,
		ToolUseEventID: use.ToolUseID,
		ToolStepID:     stepID,
		ToolName:       use.ToolName,
		Input:          use.Input,
	}
	var executed ExecuteToolResult
	if t.interrupts == nil {
		err := workflow.ExecuteActivity(
			t.actx,
			ActivityExecuteTool,
			input,
		).Get(t.actx, &executed)
		if err != nil {
			return ExecuteToolResult{}, interruptibleActivityOutcome{}, err
		}
		t.ordinal++
		return executed, interruptibleActivityOutcome{Completed: true}, nil
	}
	outcome, err := t.interrupts.executeActivity(
		ActivityExecuteTool,
		input,
		&executed,
	)
	if err != nil {
		return ExecuteToolResult{}, interruptibleActivityOutcome{}, err
	}
	if outcome.Completed {
		t.ordinal++
	}
	return executed, outcome, nil
}

func (t *workflowTurnState) complete(
	pendingActionEventIDs []string,
) (RunTurnResult, error) {
	stopReason := map[string]any{"type": "end_turn"}
	if len(pendingActionEventIDs) > 0 {
		stopReason = map[string]any{
			"type":      "requires_action",
			"event_ids": pendingActionEventIDs,
		}
	}
	output := append(t.output, domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": stopReason},
	})
	input := CompleteWorkflowTurnInput{
		SessionID:             t.sessionID,
		TriggerEventID:        t.triggerEventID,
		Output:                output,
		Status:                domain.StatusIdle,
		AttemptID:             t.attemptID,
		PendingActionEventIDs: pendingActionEventIDs,
		ResolutionEventIDs:    t.resolutionEventIDs,
	}
	if t.attemptID != "" {
		input.AttemptState = domain.RunAttemptCompleted
	}
	var result RunTurnResult
	err := workflow.ExecuteActivity(
		t.actx,
		ActivityCompleteWorkflowTurn,
		input,
	).Get(t.actx, &result)
	return result, err
}

func (t *workflowTurnState) terminate(
	failure turnFailure,
) (RunTurnResult, error) {
	message := string(failure)
	output := append(t.output,
		domain.EventDraft{Type: domain.EvSessionError, Payload: map[string]any{
			"error": map[string]any{"type": "api_error", "message": message},
		}},
		domain.EventDraft{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
	)
	input := CompleteWorkflowTurnInput{
		SessionID:          t.sessionID,
		TriggerEventID:     t.triggerEventID,
		Output:             output,
		Status:             domain.StatusTerminated,
		AttemptID:          t.attemptID,
		ResolutionEventIDs: t.resolutionEventIDs,
	}
	if t.attemptID != "" {
		input.AttemptState = domain.RunAttemptFailed
		input.AttemptError = &message
	}
	var result RunTurnResult
	err := workflow.ExecuteActivity(
		t.actx,
		ActivityCompleteWorkflowTurn,
		input,
	).Get(t.actx, &result)
	return result, err
}

func indexTurnTools(tools []TurnTool) map[string]TurnTool {
	toolsByName := make(map[string]TurnTool, len(tools))
	for _, tool := range tools {
		// Built-ins are listed before custom tools. Preserve the first owner when
		// names collide, matching the public built-in-first dispatch contract.
		if _, exists := toolsByName[tool.Name]; !exists {
			toolsByName[tool.Name] = tool
		}
	}
	return toolsByName
}

// resumeWorkflowTurn reconstructs a fully claimed pending-action barrier as one
// logical tool-result round. It remains Workflow-scoped because an allowed
// confirmation executes the original built-in through an Activity.
func resumeWorkflowTurn(
	turn *workflowTurnState,
	prepared PrepareTurnResult,
	toolsByName map[string]TurnTool,
	messages []domain.Message,
) ([]domain.Message, bool, turnFailure, error) {
	if len(prepared.ResumeActions) == 0 {
		return messages, false, "", nil
	}

	actionBlocks := make([]domain.ContentBlock, 0, len(prepared.ResumeActions))
	resultBlocks := make([]domain.ContentBlock, 0, len(prepared.ResumeActions))
	for _, action := range prepared.ResumeActions {
		definition, ok := toolsByName[action.ToolName]
		if !ok {
			return nil, false, failTurn(
				"pending action names a tool that is not enabled: " + action.ToolName,
			), nil
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
				return nil, false, failTurn(
					"custom tool result does not reference a custom tool",
				), nil
			}
		case domain.PendingToolConfirmation:
			if definition.Kind != TurnToolBuiltin ||
				definition.Permission.Type != "always_ask" {
				return nil, false, failTurn(
					"tool confirmation does not reference an always_ask built-in",
				), nil
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
					return nil, false, failTurn(
						"allowed confirmation has no durable operation id",
					), nil
				}
				executed, activityOutcome, err := turn.executeTool(
					prepared.AttemptID,
					domain.ContentBlock{
						Type:      "tool_use",
						ToolUseID: action.ActionEventID,
						ToolName:  action.ToolName,
						Input:     action.Input,
					},
					action.ToolStepID,
				)
				if err != nil {
					return nil, false, "", err
				}
				if activityOutcome.Interrupted && !activityOutcome.Completed {
					return nil, true, "", nil
				}
				if executed.FatalError != "" {
					if activityOutcome.Interrupted {
						return nil, true, "", nil
					}
					return nil, false, failTurn(executed.FatalError), nil
				}
				if executed.Ambiguous {
					if activityOutcome.Interrupted {
						return nil, true, "", nil
					}
					return nil, false, failTurn(
						"a confirmed tool began executing but no trustworthy result was recorded; " +
							"the side effect will not be retried",
					), nil
				}
				content = executed.Result.Content
				isError = executed.Result.IsError
				if activityOutcome.Interrupted {
					turn.output = append(turn.output, domain.EventDraft{
						Type: domain.EvAgentToolResult,
						Payload: map[string]any{
							"tool_use_id": action.ActionEventID,
							"content":     content,
							"is_error":    isError,
						},
					})
					return nil, true, "", nil
				}
			}
			turn.output = append(turn.output, domain.EventDraft{
				Type: domain.EvAgentToolResult,
				Payload: map[string]any{
					"tool_use_id": action.ActionEventID,
					"content":     content,
					"is_error":    isError,
				},
			})
		default:
			return nil, false, failTurn("unknown pending action kind"), nil
		}
		resultBlocks = append(resultBlocks, domain.ContentBlock{
			Type:          "tool_result",
			ToolResultFor: action.ActionEventID,
			Text:          agentruntime.FlattenResultText(content),
			IsError:       isError,
		})
	}

	return agentruntime.AppendMerging(messages, []domain.Message{
		{Role: domain.RoleAssistant, Content: actionBlocks},
		{Role: domain.RoleUser, Content: resultBlocks},
	}), false, "", nil
}

type plannedToolUse struct {
	use    domain.ContentBlock
	stepID string
}

type toolBatchPlan struct {
	actionDrafts          []domain.EventDraft
	executable            []plannedToolUse
	pendingActionEventIDs []string
}

// planToolBatch is the pure classification boundary for one model tool-use
// round. It validates the complete batch before any side effect and then
// separates server-executed built-ins from client-action barriers.
func planToolBatch(
	toolUses []domain.ContentBlock,
	toolsByName map[string]TurnTool,
	stepIDsByEvent map[string]string,
) (toolBatchPlan, turnFailure) {
	for _, use := range toolUses {
		if stepIDsByEvent[use.ToolUseID] == "" {
			return toolBatchPlan{}, failTurn(
				"model tool request has no durable operation id",
			)
		}
		definition, ok := toolsByName[use.ToolName]
		if !ok {
			return toolBatchPlan{}, failTurn(
				"model requested a tool that is not enabled: " + use.ToolName,
			)
		}
		if definition.Kind == TurnToolBuiltin &&
			definition.Permission.Type != "always_allow" &&
			definition.Permission.Type != "always_ask" {
			return toolBatchPlan{}, failTurn(
				"built-in tool has unsupported permission policy: " +
					definition.Permission.Type,
			)
		}
	}

	plan := toolBatchPlan{
		actionDrafts: make([]domain.EventDraft, 0, len(toolUses)),
	}
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
			plan.pendingActionEventIDs = append(
				plan.pendingActionEventIDs,
				use.ToolUseID,
			)
		case definition.Permission.Type == "always_ask":
			draft.Type = domain.EvAgentToolUse
			draft.Payload["evaluated_permission"] = "ask"
			plan.pendingActionEventIDs = append(
				plan.pendingActionEventIDs,
				use.ToolUseID,
			)
		default:
			draft.Type = domain.EvAgentToolUse
			plan.executable = append(plan.executable, plannedToolUse{
				use:    use,
				stepID: stepIDsByEvent[use.ToolUseID],
			})
		}
		plan.actionDrafts = append(plan.actionDrafts, draft)
	}
	return plan, ""
}

type toolBatchExecution struct {
	resultDrafts []domain.EventDraft
	resultBlocks []domain.ContentBlock
}

// executeToolBatch remains Workflow-scoped: each tool execution is a durable
// Activity and its command order and ordinal are part of replay history.
func executeToolBatch(
	turn *workflowTurnState,
	attemptID string,
	plan toolBatchPlan,
) (toolBatchExecution, bool, turnFailure, error) {
	execution := toolBatchExecution{
		resultDrafts: make([]domain.EventDraft, 0, len(plan.executable)),
		resultBlocks: make([]domain.ContentBlock, 0, len(plan.executable)),
	}
	for _, planned := range plan.executable {
		executed, activityOutcome, err := turn.executeTool(
			attemptID,
			planned.use,
			planned.stepID,
		)
		if err != nil {
			return toolBatchExecution{}, false, "", err
		}
		if activityOutcome.Interrupted && !activityOutcome.Completed {
			return execution, true, "", nil
		}
		if executed.FatalError != "" {
			if activityOutcome.Interrupted {
				return execution, true, "", nil
			}
			return toolBatchExecution{}, false, failTurn(executed.FatalError), nil
		}
		if executed.Ambiguous {
			if activityOutcome.Interrupted {
				return execution, true, "", nil
			}
			return toolBatchExecution{}, false, failTurn(
				"a tool began executing but no trustworthy result was recorded; " +
					"the side effect will not be retried",
			), nil
		}
		execution.resultDrafts = append(execution.resultDrafts, domain.EventDraft{
			Type: domain.EvAgentToolResult,
			Payload: map[string]any{
				"tool_use_id": planned.use.ToolUseID,
				"content":     executed.Result.Content,
				"is_error":    executed.Result.IsError,
			},
		})
		execution.resultBlocks = append(execution.resultBlocks, domain.ContentBlock{
			Type:          "tool_result",
			ToolResultFor: planned.use.ToolUseID,
			Text:          agentruntime.FlattenResultText(executed.Result.Content),
			IsError:       executed.Result.IsError,
		})
		if activityOutcome.Interrupted {
			return execution, true, "", nil
		}
	}
	return execution, false, "", nil
}
