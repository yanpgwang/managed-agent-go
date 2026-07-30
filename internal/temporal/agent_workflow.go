package temporal

import (
	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// maxWorkflowToolRounds is the deterministic safety bound for one public turn.
// It prevents a model that continually requests tools from growing Workflow
// history forever.
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
	return runWorkflowTurnInternal(actx, sessionID, triggerEventID, nil)
}

func runWorkflowTurnWithResolutions(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
) (RunTurnResult, error) {
	return runWorkflowTurnInternal(actx, sessionID, triggerEventID, resolutionEventIDs)
}

func runWorkflowTurnInternal(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
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
			return RunTurnResult{Disposition: TurnTerminated}, nil
		}
		return RunTurnResult{Disposition: TurnCompleted}, nil
	}

	turn := &workflowTurnState{
		actx:               actx,
		sessionID:          sessionID,
		triggerEventID:     triggerEventID,
		resolutionEventIDs: resolutionEventIDs,
	}
	if prepared.FatalError != "" {
		return turn.terminate(failTurn(prepared.FatalError))
	}

	toolsByName := indexTurnTools(prepared.Tools)
	messages, failure, err := resumeWorkflowTurn(
		turn,
		prepared,
		toolsByName,
		append([]domain.Message(nil), prepared.Request.Messages...),
	)
	if err != nil {
		return RunTurnResult{}, err
	}
	if failure != "" {
		return turn.terminate(failure)
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
			return turn.terminate(failTurn(called.FatalError))
		}

		if content := agentruntime.TextBlocksToContent(called.Response.Content); len(content) > 0 {
			if called.MessageEventID == "" {
				return turn.terminate(failTurn(
					"model response text has no durable public event id",
				))
			}
			turn.output = append(turn.output, domain.EventDraft{
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
			return turn.complete(nil)
		}
		if prepared.AttemptID == "" {
			return turn.terminate(failTurn("tool-using turn has no durable attempt id"))
		}

		stepIDsByEvent := make(map[string]string, len(called.ToolSteps))
		for _, planned := range called.ToolSteps {
			stepIDsByEvent[planned.ToolUseEventID] = planned.ToolStepID
		}
		plan, failure := planToolBatch(toolUses, toolsByName, stepIDsByEvent)
		if failure != "" {
			return turn.terminate(failure)
		}
		turn.output = append(turn.output, plan.actionDrafts...)

		executed, failure, err := executeToolBatch(turn, prepared.AttemptID, plan)
		if err != nil {
			return RunTurnResult{}, err
		}
		if failure != "" {
			return turn.terminate(failure)
		}
		turn.output = append(turn.output, executed.resultDrafts...)

		if len(plan.pendingActionEventIDs) > 0 {
			return turn.complete(plan.pendingActionEventIDs)
		}

		// Preserve the model's exact assistant round, including text emitted
		// alongside tool_use blocks, then append the paired tool results.
		messages = agentruntime.AppendMerging(messages, []domain.Message{
			{Role: domain.RoleAssistant, Content: called.Response.Content},
			{Role: domain.RoleUser, Content: executed.resultBlocks},
		})
	}

	// Reaching the safety bound closes the public turn normally rather than
	// allowing unbounded Workflow history.
	return turn.complete(nil)
}
