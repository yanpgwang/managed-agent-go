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
	return runWorkflowTurnInternal(
		actx,
		sessionID,
		triggerEventID,
		nil,
		nil,
	)
}

func runWorkflowTurnWithResolutions(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
) (RunTurnResult, error) {
	return runWorkflowTurnInternal(
		actx,
		sessionID,
		triggerEventID,
		resolutionEventIDs,
		nil,
	)
}

func runWorkflowTurnInternal(
	actx workflow.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
	interrupts *turnInterruptWatcher,
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
		actx:                   actx,
		sessionID:              sessionID,
		triggerEventID:         triggerEventID,
		resolutionEventIDs:     resolutionEventIDs,
		interrupts:             interrupts,
		usesProviderTranscript: prepared.UsesProviderTranscript,
		output:                 append([]domain.EventDraft(nil), prepared.PreludeEvents...),
		transcriptDelta: append(
			[]domain.Message(nil),
			prepared.TranscriptDelta...,
		),
	}
	if prepared.FatalError != "" {
		return turn.terminate(failTurn(prepared.FatalError))
	}

	toolsByName := indexTurnTools(prepared.Tools)
	messages, interrupted, failure, err := resumeWorkflowTurn(
		turn,
		prepared,
		toolsByName,
		append([]domain.Message(nil), prepared.Request.Messages...),
	)
	if err != nil {
		return RunTurnResult{}, err
	}
	if interrupted {
		return turn.complete(nil)
	}
	if failure != "" {
		return turn.terminate(failure)
	}

	for round := 0; round < maxWorkflowToolRounds; round++ {
		request := prepared.Request
		request.Messages = messages
		mappingCheckpoint := len(turn.toolUseMappings)

		called, activityOutcome, err := turn.callModel(CallModelInput{
			SessionID: sessionID,
			Request:   request,
		})
		if err != nil {
			return RunTurnResult{}, err
		}
		if activityOutcome.Interrupted && !activityOutcome.Completed {
			return turn.complete(nil)
		}
		if called.FatalError != "" {
			if activityOutcome.Interrupted {
				return turn.complete(nil)
			}
			return turn.terminate(failTurn(called.FatalError))
		}
		if prepared.UsesProviderTranscript {
			turn.transcriptDelta = agentruntime.AppendMerging(
				turn.transcriptDelta,
				[]domain.Message{{
					Role:    domain.RoleAssistant,
					Content: called.Response.Content,
				}},
			)
			for _, planned := range called.ToolSteps {
				providerID := planned.ProviderToolUseID
				if providerID == "" {
					providerID = planned.ToolUseEventID
				}
				turn.toolUseMappings = append(
					turn.toolUseMappings,
					domain.ProviderToolUseMapping{
						PublicEventID:     planned.ToolUseEventID,
						ProviderToolUseID: providerID,
						ToolName: toolNameForProviderID(
							called.Response.Content,
							providerID,
						),
					},
				)
			}
		}

		if content := agentruntime.TextBlocksToContent(called.Response.Content); len(content) > 0 {
			if called.MessageEventID == "" {
				if activityOutcome.Interrupted {
					return turn.complete(nil)
				}
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
			if activityOutcome.Interrupted {
				closeInterruptedProviderToolRound(
					turn,
					toolUses,
					nil,
					mappingCheckpoint,
				)
				return turn.complete(nil)
			}
			return turn.terminate(failTurn("tool-using turn has no durable attempt id"))
		}

		stepsByProviderID := make(
			map[string]PlannedToolStep,
			len(called.ToolSteps),
		)
		for _, planned := range called.ToolSteps {
			providerID := planned.ProviderToolUseID
			if providerID == "" {
				providerID = planned.ToolUseEventID
			}
			stepsByProviderID[providerID] = planned
		}
		plan, failure := planToolBatch(
			toolUses,
			toolsByName,
			stepsByProviderID,
		)
		if failure != "" {
			if activityOutcome.Interrupted {
				closeInterruptedProviderToolRound(
					turn,
					toolUses,
					nil,
					mappingCheckpoint,
				)
				return turn.complete(nil)
			}
			return turn.terminate(failure)
		}
		turn.output = append(turn.output, plan.actionDrafts...)
		if activityOutcome.Interrupted {
			closeInterruptedProviderToolRound(
				turn,
				toolUses,
				nil,
				mappingCheckpoint,
			)
			return turn.complete(nil)
		}

		executed, interrupted, failure, err := executeToolBatch(
			turn,
			prepared.AttemptID,
			plan,
		)
		if err != nil {
			return RunTurnResult{}, err
		}
		turn.output = append(turn.output, executed.resultDrafts...)
		if interrupted {
			closeInterruptedProviderToolRound(
				turn,
				toolUses,
				executed.resultBlocks,
				mappingCheckpoint,
			)
			return turn.complete(nil)
		}
		if failure != "" {
			return turn.terminate(failure)
		}

		// A model may request an executable tool and an approval/custom tool in
		// the same assistant block. Persist completed executable results before
		// parking so every tool_use already executed has a matching tool_result
		// when the remaining pending actions resume.
		if prepared.UsesProviderTranscript && len(executed.resultBlocks) > 0 {
			turn.transcriptDelta = agentruntime.AppendMerging(
				turn.transcriptDelta,
				[]domain.Message{{
					Role:    domain.RoleUser,
					Content: executed.resultBlocks,
				}},
			)
		}
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

func toolNameForProviderID(
	blocks []domain.ContentBlock,
	providerID string,
) string {
	for _, block := range blocks {
		if block.Type == "tool_use" && block.ToolUseID == providerID {
			return block.ToolName
		}
	}
	return ""
}
