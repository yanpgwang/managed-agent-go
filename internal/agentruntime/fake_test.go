package agentruntime

import (
	"context"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

type capSink struct{ events []domain.Event }

func (c *capSink) Emit(_ context.Context, drafts []domain.EventDraft) ([]domain.Event, error) {
	for _, d := range drafts {
		c.events = append(c.events, domain.Event{Type: d.Type, Payload: d.Payload})
	}
	return c.events, nil
}

func TestFake_PlainMessageEndsIdle(t *testing.T) {
	f := NewFake()
	sink := &capSink{}
	_, err := f.Run(context.Background(), RunRequest{
		SessionID: "sesn_1",
		Trigger:   domain.Event{Type: domain.EvUserMessage, Payload: map[string]any{"content": textContent("hello")}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	last := sink.events[len(sink.events)-1]
	if last.Type != domain.EvSessionStatusIdle {
		t.Fatalf("expected idle last, got %s", last.Type)
	}
}

func TestFake_ToolRequestStopsForResult(t *testing.T) {
	f := NewFake()
	sink := &capSink{}
	_, _ = f.Run(context.Background(), RunRequest{
		SessionID: "sesn_1",
		Trigger:   domain.Event{Type: domain.EvUserMessage, Payload: map[string]any{"content": textContent("tool: get_metrics")}},
	}, sink)
	last := sink.events[len(sink.events)-1]
	if last.Type != domain.EvAgentCustomToolUse {
		t.Fatalf("expected custom_tool_use last (awaiting result), got %s", last.Type)
	}
	if _, ok := last.Payload["custom_tool_use_id"]; ok {
		t.Fatal("agent.custom_tool_use must use its event id for correlation")
	}
}

func TestFake_InterruptEndsIdleNotTerminated(t *testing.T) {
	f := NewFake()
	sink := &capSink{}
	_, _ = f.Run(context.Background(), RunRequest{
		SessionID: "sesn_1",
		Trigger:   domain.Event{Type: domain.EvUserInterrupt},
	}, sink)
	if len(sink.events) != 1 {
		t.Fatalf("interrupt: want exactly one event, got %d: %v", len(sink.events), sink.events)
	}
	if sink.events[0].Type != domain.EvSessionStatusIdle {
		t.Fatalf("interrupt must end idle (not terminated), got %s", sink.events[0].Type)
	}
}

func TestFake_CustomToolResultEndsIdle(t *testing.T) {
	f := NewFake()
	sink := &capSink{}
	_, _ = f.Run(context.Background(), RunRequest{
		SessionID: "sesn_1",
		Trigger: domain.Event{Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{"content": "cpu 99%"}},
	}, sink)
	last := sink.events[len(sink.events)-1]
	if last.Type != domain.EvSessionStatusIdle {
		t.Fatalf("custom_tool_result: want idle last, got %s", last.Type)
	}
	// the agent.message before idle should carry the returned content
	if sink.events[0].Type != domain.EvAgentMessage {
		t.Fatalf("expected agent.message first, got %s", sink.events[0].Type)
	}
}
