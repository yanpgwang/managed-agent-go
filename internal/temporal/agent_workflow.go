package temporal

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"go.temporal.io/sdk/workflow"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// maxWorkflowToolRounds is the deterministic safety bound for one public turn.
// It prevents a model that continually requests tools from growing Workflow
// history forever.
const maxWorkflowToolRounds = 20

const (
	liveModelSpanStartChangeID = "live-model-request-span-start"
	liveModelSpanStartVersion  = 1

	// mcpToolEventsChangeID gates the wire-breaking move of MCP tool calls from
	// agent.tool_use/agent.tool_result onto the documented
	// agent.mcp_tool_use/agent.mcp_tool_result pair. A Workflow that recorded no
	// marker for this change keeps emitting the legacy pair for its whole run, so
	// an in-flight turn replays deterministically and its already-published
	// public ledger stays internally consistent.
	mcpToolEventsChangeID = "mcp-tool-event-types"
	mcpToolEventsVersion  = 1
)

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
	liveModelSpanStarts := workflow.GetVersion(
		actx,
		liveModelSpanStartChangeID,
		workflow.DefaultVersion,
		liveModelSpanStartVersion,
	) == liveModelSpanStartVersion
	// Evaluated before any tool draft is built so both the resume round and every
	// model round of this turn agree on one naming scheme. Ordering relative to
	// the gate above is part of replay history and must not change.
	mcpToolEvents := workflow.GetVersion(
		actx,
		mcpToolEventsChangeID,
		workflow.DefaultVersion,
		mcpToolEventsVersion,
	) == mcpToolEventsVersion

	turn := &workflowTurnState{
		actx:                   actx,
		sessionID:              sessionID,
		triggerEventID:         triggerEventID,
		resolutionEventIDs:     resolutionEventIDs,
		interrupts:             interrupts,
		usesProviderTranscript: prepared.UsesProviderTranscript,
		mcpToolEvents:          mcpToolEvents,
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

	maxRounds := maxWorkflowToolRounds
	if prepared.Outcome != nil {
		// Each evaluation cycle may consume a full tool loop, and
		// max_iterations_reached is followed by one final acknowledgment turn.
		maxRounds *= prepared.Outcome.MaxIterations + 1
	}
	outcomeIteration := 0
	outcomeFinished := false
	for round := 0; round < maxRounds; round++ {
		// A later model request must never overtake completed public progress
		// from the preceding tool/outcome round in PostgreSQL receipt order.
		if liveModelSpanStarts {
			if err := turn.flushOutput(); err != nil {
				return RunTurnResult{}, err
			}
		}
		request := prepared.Request
		request.Messages = messages
		mappingCheckpoint := len(turn.toolUseMappings)
		modelRequestStartID, modelRequestEndID := modelRequestSpanIDs(
			sessionID,
			triggerEventID,
			round,
		)
		if liveModelSpanStarts {
			if err := turn.startModelRequest(modelRequestStartID); err != nil {
				return RunTurnResult{}, err
			}
		}

		called, activityOutcome, err := turn.callModel(CallModelInput{
			SessionID:           sessionID,
			ModelRequestStartID: modelRequestStartID,
			ModelRequestEndID:   modelRequestEndID,
			Request:             request,
		})
		if err != nil {
			return RunTurnResult{}, err
		}
		if activityOutcome.Interrupted && !activityOutcome.Completed {
			cancelled := CallModelResult{
				ModelRequestStartID: modelRequestStartID,
				ModelRequestEndID:   modelRequestEndID,
			}
			if !liveModelSpanStarts {
				if start := modelRequestStartDraft(cancelled); start != nil {
					turn.output = append(turn.output, *start)
				}
			}
			if end := modelRequestEndDraft(cancelled, true); end != nil {
				turn.output = append(turn.output, *end)
			}
			return turn.complete(nil)
		}
		if !liveModelSpanStarts {
			if start := modelRequestStartDraft(called); start != nil {
				turn.output = append(turn.output, *start)
			}
		}
		if called.FatalError != "" {
			if end := modelRequestEndDraft(called, true); end != nil {
				turn.output = append(turn.output, *end)
			}
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
			if end := modelRequestEndDraft(called, false); end != nil {
				turn.output = append(turn.output, *end)
			}
			if prepared.Outcome == nil || outcomeFinished {
				return turn.complete(nil)
			}
			candidate := agentruntime.AppendMerging(messages, []domain.Message{{
				Role: domain.RoleAssistant, Content: called.Response.Content,
			}})
			finalCycle := outcomeIteration+1 >= prepared.Outcome.MaxIterations
			evaluated, evaluationOutcome, err := turn.evaluateOutcome(
				EvaluateOutcomeInput{
					SessionID:  sessionID,
					Model:      prepared.Request.Model,
					Effort:     prepared.Request.Effort,
					Speed:      prepared.Request.Speed,
					Outcome:    *prepared.Outcome,
					Candidate:  candidate,
					Iteration:  outcomeIteration,
					FinalCycle: finalCycle,
				},
			)
			if err != nil {
				return RunTurnResult{}, err
			}
			if evaluationOutcome.Interrupted && !evaluationOutcome.Completed {
				return turn.complete(nil)
			}
			if evaluated.FatalError != "" {
				if evaluationOutcome.Interrupted {
					return turn.complete(nil)
				}
				return turn.terminate(failTurn(evaluated.FatalError))
			}
			turn.output = append(
				turn.output,
				outcomeEvaluationDrafts(
					*prepared.Outcome,
					outcomeIteration,
					evaluated,
				)...,
			)
			if evaluationOutcome.Interrupted {
				return turn.complete(nil)
			}
			switch evaluated.Result {
			case "satisfied", "failed":
				return turn.complete(nil)
			case "needs_revision", "max_iterations_reached":
				feedback := "Independent outcome evaluation: " + evaluated.Explanation
				if evaluated.Result == "max_iterations_reached" {
					feedback += "\nThe evaluation budget is exhausted. Provide one final acknowledgment of the best available result."
					outcomeFinished = true
				} else {
					feedback += "\nRevise the deliverable to address this feedback, then present the updated result."
					outcomeIteration++
				}
				feedbackMessage := domain.Message{
					Role:    domain.RoleUser,
					Content: []domain.ContentBlock{{Type: "text", Text: feedback}},
				}
				messages = agentruntime.AppendMerging(candidate, []domain.Message{feedbackMessage})
				if prepared.UsesProviderTranscript {
					turn.transcriptDelta = agentruntime.AppendMerging(
						turn.transcriptDelta,
						[]domain.Message{feedbackMessage},
					)
				}
				continue
			default:
				return turn.terminate(failTurn("grader returned an unsupported outcome result"))
			}
		}
		if end := modelRequestEndDraft(called, false); end != nil {
			turn.output = append(turn.output, *end)
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
			mcpToolEvents,
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

// modelRequestSpanIDs are deterministic Workflow-owned operation ids. Owning
// them before the interruptible model Activity starts lets an interrupt commit
// the terminal span.model_request_end that closes any best-effort preview, even
// when cancellation prevents the Activity result from being recorded.
func modelRequestSpanIDs(sessionID, triggerEventID string, round int) (string, string) {
	makeID := func(kind string) string {
		sum := sha256.Sum256([]byte(
			sessionID + "\x00" + triggerEventID + "\x00" + strconv.Itoa(round) + "\x00" + kind,
		))
		return domain.PrefixEvent + hex.EncodeToString(sum[:12])
	}
	return makeID("model_start"), makeID("model_end")
}

func workflowProgressEventID(
	sessionID string,
	triggerEventID string,
	ordinal int,
) string {
	sum := sha256.Sum256([]byte(
		sessionID + "\x00" + triggerEventID + "\x00progress\x00" +
			strconv.Itoa(ordinal),
	))
	return domain.PrefixEvent + hex.EncodeToString(sum[:12])
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
