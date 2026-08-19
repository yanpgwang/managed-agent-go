package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestFake_EchoesLastUserTextAndRecordsRequest(t *testing.T) {
	f := NewFake()
	req := Request{
		Model: "test-model",
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "first"}}},
			{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Type: "text", Text: "reply"}}},
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "second"}}},
		},
	}
	resp, err := f.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "echo: second" {
		t.Fatalf("Content = %#v, want single text 'echo: second'", resp.Content)
	}
	if got := f.LastRequest(); len(got.Messages) != 3 {
		t.Fatalf("LastRequest messages = %d, want 3", len(got.Messages))
	}
}

func TestFake_CallsOfferedToolThenEnds(t *testing.T) {
	f := NewFake()
	// Round 1: tools offered, no prior tool_result -> model calls the tool.
	r1, _ := f.CreateMessage(context.Background(), Request{
		Tools:    []ToolSchema{{Name: "bash"}},
		Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "run ls"}}}},
	})
	if r1.StopReason != "tool_use" || len(r1.Content) != 1 || r1.Content[0].Type != "tool_use" || r1.Content[0].ToolName != "bash" {
		t.Fatalf("round1 = %#v", r1)
	}
	// Round 2: history now has a tool_result -> model ends the turn with text.
	r2, _ := f.CreateMessage(context.Background(), Request{
		Tools: []ToolSchema{{Name: "bash"}},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "run ls"}}},
			{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Type: "tool_use", ToolUseID: "t1", ToolName: "bash"}}},
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "tool_result", ToolResultFor: "t1", Text: "a.go"}}},
		},
	})
	if r2.StopReason != "end_turn" || len(r2.Content) == 0 || r2.Content[0].Type != "text" {
		t.Fatalf("round2 = %#v", r2)
	}
}

func TestFake_CreateMessageStream_ChunksThenFinal(t *testing.T) {
	f := NewFake()
	var chunks []string
	resp, err := f.CreateMessageStream(context.Background(), Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "hello world"}}}},
	}, func(index int, text string) { chunks = append(chunks, text) })
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("want >=2 streamed chunks, got %v", chunks)
	}
	// chunks concatenate to the final text (prefix property; here equality)
	if strings.Join(chunks, "") != "echo: hello world" {
		t.Fatalf("chunks = %q, want to join to 'echo: hello world'", chunks)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "echo: hello world" || resp.StopReason != "end_turn" {
		t.Fatalf("final resp = %#v", resp)
	}
}

func TestFake_SetError(t *testing.T) {
	f := NewFake()
	want := errors.New("provider unavailable")
	f.SetError(want)

	_, err := f.CreateMessageStream(context.Background(), Request{}, nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if got := f.LastRequest(); len(got.Messages) != 0 {
		t.Fatalf("LastRequest = %#v, want recorded empty request", got)
	}
}
