package agentruntime

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime/tools"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

var _ AgentRuntime = (*AgentCore)(nil)

// maxToolTurns bounds the model<->tool loop within a single Run so a model that
// keeps requesting tools can never spin forever. Reaching the cap ends the turn
// as if the model had stopped; the app layer appends the terminal idle status.
const maxToolTurns = 20

// AgentCore is the self-hosted agent runtime: a bounded orchestration loop that
// replays the projected conversation to the model, emits typed events, and runs
// enabled built-in tools inside the request sandbox. It owns no history (the app
// layer projects it) and touches no database or HTTP. The app layer appends the
// terminal session.status_idle after Run returns.
//
// Each turn calls the model once. Text blocks are emitted as agent.message. If
// the model requests always_allow built-in tools, the core executes each in the
// sandbox, emits the paired agent.tool_use/agent.tool_result events, threads the
// tool_use and tool_result blocks into a local running message list, and loops
// so the model sees the results within the same run. When a turn produces no
// tool_use (end_turn) the loop returns.
//
// With no toolset the loop runs exactly once and behaves like the S1 single
// round: a tool_use stop reason is impossible (no tools are offered), so the
// produced text is emitted and the turn ends.
type AgentCore struct {
	client model.Client
	ids    domain.IDGenerator
}

// NewAgentCore builds the agent core over a model client and an id generator.
// The generator names the committed event ids the core pre-assigns to drafts,
// most importantly the assistant agent.message: when the sink is a
// PreviewEmitter the core generates this id up front so the preview stream
// (PreviewStart / PreviewDelta) and the persisted agent.message share one id.
// It is the same generator kind the store/admission layer uses, so ids the core
// mints are drawn from the same space the sink would otherwise assign.
func NewAgentCore(c model.Client, ids domain.IDGenerator) *AgentCore {
	return &AgentCore{client: c, ids: ids}
}

// drivesModelTurn reports whether a trigger should start a model turn. A plain
// user.message does, and so does a user.custom_tool_result that resolves a
// parked custom tool: the projected history now pairs that result with its
// agent.custom_tool_use, so the model sees the outcome and the loop continues.
//
// user.tool_confirmation (the always_ask resume) is intentionally not here yet:
// resolving it requires projecting the confirmation into a tool_result AND, on
// allow, executing the built-in the model asked for — neither of which exists.
// Driving a turn on it would replay a dangling agent.tool_use with no paired
// result, which is a malformed Messages request. Parking on always_ask works;
// its resume is a follow-up. Interrupts and outcome definitions never by
// themselves drive a turn.
func drivesModelTurn(triggerType string) bool {
	switch triggerType {
	case domain.EvUserMessage, domain.EvUserCustomToolResult:
		return true
	}
	return false
}

func (a *AgentCore) Run(ctx context.Context, req RunRequest, sink EventSink) (RunOutcome, error) {
	if !drivesModelTurn(req.Trigger.Type) {
		return RunOutcome{}, nil
	}
	system := ""
	if req.AgentSnapshot.System != nil {
		system = *req.AgentSnapshot.System
	}
	toolSchemas := enabledToolSchemas(req.ToolSet)

	// messages is the local running conversation, seeded from the projected
	// history and grown with assistant tool_use / user tool_result blocks so the
	// model sees tool outcomes within this run.
	messages := req.Messages

	for turn := 0; turn < maxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return RunOutcome{}, err
		}

		// Assistant text is streamed as a preview when the sink supports it: the
		// core mints the committed agent.message id up front, announces it with
		// PreviewStart, streams each text delta through PreviewDelta, then emits
		// the full agent.message draft carrying that same id. Preview and
		// persisted event are one event correlated by the shared id. When the sink
		// is not a PreviewEmitter the turn falls back to the non-streaming
		// CreateMessage + plain Emit (S1 behavior). Only assistant text previews;
		// the tool loop below is unchanged.
		var resp model.Response
		var err error
		previewer, canPreview := sink.(PreviewEmitter)
		if canPreview {
			messageID := a.ids.NewID(domain.PrefixEvent)
			started := false
			resp, err = a.client.CreateMessageStream(ctx, model.Request{
				Model:    req.AgentSnapshot.Model.ID,
				System:   system,
				Messages: messages,
				Tools:    toolSchemas,
			}, func(index int, text string) {
				if !started {
					previewer.PreviewStart(messageID, domain.EvAgentMessage)
					started = true
				}
				previewer.PreviewDelta(messageID, index, text)
			})
			if err != nil {
				return RunOutcome{}, err
			}
			if content := textBlocksToContent(resp.Content); len(content) > 0 {
				// A text turn that produced no deltas (e.g. a client that never
				// streams) still needs a PreviewStart so the preview id is announced
				// before the persisted agent.message carrying it.
				if !started {
					previewer.PreviewStart(messageID, domain.EvAgentMessage)
				}
				if _, err := sink.Emit(ctx, []domain.EventDraft{{
					ID:      messageID,
					Type:    domain.EvAgentMessage,
					Payload: map[string]any{"content": content},
				}}); err != nil {
					return RunOutcome{}, err
				}
			}
		} else {
			resp, err = a.client.CreateMessage(ctx, model.Request{
				Model:    req.AgentSnapshot.Model.ID,
				System:   system,
				Messages: messages,
				Tools:    toolSchemas,
			})
			if err != nil {
				return RunOutcome{}, err
			}
			// Emit any assistant text as an agent.message (S1 behavior).
			if content := textBlocksToContent(resp.Content); len(content) > 0 {
				if _, err := sink.Emit(ctx, []domain.EventDraft{{
					Type:    domain.EvAgentMessage,
					Payload: map[string]any{"content": content},
				}}); err != nil {
					return RunOutcome{}, err
				}
			}
		}

		// Collect the tool_use blocks this turn requested.
		var toolUses []domain.ContentBlock
		for _, b := range resp.Content {
			if b.Type == "tool_use" {
				toolUses = append(toolUses, b)
			}
		}
		if len(toolUses) == 0 {
			return RunOutcome{}, nil // end_turn: no tools requested.
		}

		// Execute each tool_use. always_allow built-ins run inline and thread their
		// result back into this run. custom tools and always_ask built-ins cannot
		// be resolved by the core: they emit the use event and park the run with
		// requires_action so the app layer stops at idle and a later
		// user.custom_tool_result / user.tool_confirmation resumes a fresh run.
		var assistantBlocks, resultBlocks []domain.ContentBlock
		var actionEventIDs []string
		for _, use := range toolUses {
			enabled, policy := req.ToolSet.BuiltinEnabled(use.ToolName)
			exec, isBuiltin := tools.Registry()[use.ToolName]

			switch {
			case isBuiltin && enabled && policy.Type == "always_allow":
				// Emit the tool_use alone first so we can read back its committed id
				// and use it to correlate the tool_result. The public event id is the
				// committed event's own id (out[0].ID); the model's tool_use id is not
				// part of the wire payload, so it is intentionally not persisted here.
				out, err := sink.Emit(ctx, []domain.EventDraft{{
					Type: domain.EvAgentToolUse,
					Payload: map[string]any{
						"name":  use.ToolName,
						"input": use.Input,
					},
				}})
				if err != nil {
					return RunOutcome{}, err
				}
				id := out[0].ID

				result := exec(ctx, req.Sandbox, use.Input)

				if _, err := sink.Emit(ctx, []domain.EventDraft{{
					Type: domain.EvAgentToolResult,
					Payload: map[string]any{
						"tool_use_id": id,
						"content":     result.Content,
						"is_error":    result.IsError,
					},
				}}); err != nil {
					return RunOutcome{}, err
				}

				assistantBlocks = append(assistantBlocks, domain.ContentBlock{
					Type:      "tool_use",
					ToolUseID: id,
					ToolName:  use.ToolName,
					Input:     use.Input,
				})
				resultBlocks = append(resultBlocks, domain.ContentBlock{
					Type:          "tool_result",
					ToolResultFor: id,
					Text:          flattenResultText(result.Content),
					IsError:       result.IsError,
				})

			case isBuiltin && enabled:
				// Enabled built-in whose policy is not always_allow (always_ask):
				// emit agent.tool_use carrying the evaluated permission and park for
				// a user.tool_confirmation referencing the committed event id.
				out, err := sink.Emit(ctx, []domain.EventDraft{{
					Type: domain.EvAgentToolUse,
					Payload: map[string]any{
						"name":                 use.ToolName,
						"input":                use.Input,
						"evaluated_permission": "ask",
					},
				}})
				if err != nil {
					return RunOutcome{}, err
				}
				actionEventIDs = append(actionEventIDs, out[0].ID)

			default:
				// Custom tool (not a built-in the core can execute): emit
				// agent.custom_tool_use and park for a user.custom_tool_result
				// referencing the committed event id.
				out, err := sink.Emit(ctx, []domain.EventDraft{{
					Type: domain.EvAgentCustomToolUse,
					Payload: map[string]any{
						"name":  use.ToolName,
						"input": use.Input,
					},
				}})
				if err != nil {
					return RunOutcome{}, err
				}
				actionEventIDs = append(actionEventIDs, out[0].ID)
			}
		}

		// Any parked tool ends the run: the core cannot make progress until the
		// app admits the awaited result as a new trigger. The app appends the
		// terminal session.status_idle{stop_reason: requires_action} referencing
		// these ids.
		if len(actionEventIDs) > 0 {
			return RunOutcome{RequiresAction: true, ActionEventIDs: actionEventIDs}, nil
		}

		// If nothing executed, there is no result to feed back; end the turn to
		// avoid an unbounded no-progress loop.
		if len(assistantBlocks) == 0 {
			return RunOutcome{}, nil
		}

		messages = append(messages,
			domain.Message{Role: domain.RoleAssistant, Content: assistantBlocks},
			domain.Message{Role: domain.RoleUser, Content: resultBlocks},
		)
	}
	return RunOutcome{}, nil
}

// enabledToolSchemas returns the model-facing tool schemas the session offers:
// every enabled built-in in canonical order, followed by the session's custom
// tools. Offering custom tools lets the model request them; the core parks the
// run when it does, since only the app/client can resolve a custom tool.
func enabledToolSchemas(ts domain.ToolSet) []model.ToolSchema {
	schemas := enabledBuiltinSchemas(ts)
	for _, ct := range ts.Custom {
		schemas = append(schemas, model.ToolSchema{
			Name:        ct.Name,
			Description: ct.Description,
			InputSchema: ct.InputSchema,
		})
	}
	return schemas
}

// enabledBuiltinSchemas returns the model-facing tool schemas for every enabled
// built-in in the toolset, in the canonical BuiltinToolNames order. A built-in
// whose schema is nil is skipped rather than offered: declaring a tool with a
// null input_schema is an illegal Messages request (400). Every built-in name
// currently returns a non-nil object schema, so this is a defensive safeguard.
func enabledBuiltinSchemas(ts domain.ToolSet) []model.ToolSchema {
	var schemas []model.ToolSchema
	for _, name := range domain.BuiltinToolNames {
		if enabled, _ := ts.BuiltinEnabled(name); !enabled {
			continue
		}
		schema := tools.Schema(name)
		if schema == nil {
			continue
		}
		schemas = append(schemas, model.ToolSchema{Name: name, InputSchema: schema})
	}
	return schemas
}

// textContent projects the non-empty text blocks of a model response into the
// agent.message wire content array.
func textBlocksToContent(blocks []domain.ContentBlock) []any {
	content := make([]any, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		content = append(content, map[string]any{"type": "text", "text": b.Text})
	}
	return content
}

// flattenResultText extracts the concatenated text of a tool result's content
// block array for threading into the local tool_result message block.
func flattenResultText(content []any) string {
	var s string
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != "text" {
			continue
		}
		if text, _ := m["text"].(string); text != "" {
			s += text
		}
	}
	return s
}
