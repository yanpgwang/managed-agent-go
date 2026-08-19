package agentruntime

import (
	"context"
	"strings"

	"github.com/yanpgwang/mango/internal/domain"
)

var _ AgentRuntime = (*Fake)(nil)

type Fake struct{}

func NewFake() *Fake { return &Fake{} }

func idleEndTurn() domain.EventDraft {
	return domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
	}
}

// textContent builds a content[] block array holding a single text block, the
// public shape for user.message / agent.message content.
func textContent(text string) []any {
	return []any{map[string]any{"type": "text", "text": text}}
}

// contentText extracts the concatenated text from a content[] block array.
func contentText(payload map[string]any) string {
	blocks, ok := payload["content"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok {
			if t, ok := block["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

func (f *Fake) Run(ctx context.Context, req RunRequest, sink EventSink) (RunOutcome, error) {
	switch req.Trigger.Type {
	case domain.EvUserInterrupt:
		_, err := sink.Emit(ctx, []domain.EventDraft{idleEndTurn()})
		return RunOutcome{}, err

	case domain.EvUserCustomToolResult:
		content := req.Trigger.Payload["content"]
		_, err := sink.Emit(ctx, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": content}},
			idleEndTurn(),
		})
		return RunOutcome{}, err

	case domain.EvUserMessage:
		text := contentText(req.Trigger.Payload)
		if strings.Contains(text, "tool:") {
			out, err := sink.Emit(ctx, []domain.EventDraft{{
				Type: domain.EvAgentCustomToolUse,
				Payload: map[string]any{
					"name":  "get_metrics",
					"input": map[string]any{},
				},
			}})
			if err != nil {
				return RunOutcome{}, err
			}
			// Park like the real core: report the committed action event id so the
			// app layer persists a durable pending action and the session idles with
			// stop_reason.requires_action awaiting a user.custom_tool_result.
			return RunOutcome{RequiresAction: true, ActionEventIDs: []string{out[0].ID}}, nil
		}
		_, err := sink.Emit(ctx, []domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": textContent("echo: " + text)}},
			idleEndTurn(),
		})
		return RunOutcome{}, err

	default:
		return RunOutcome{}, nil
	}
}
