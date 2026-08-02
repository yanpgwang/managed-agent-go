package model

import (
	"context"
	"strings"
	"sync"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

var _ Client = (*Fake)(nil)

// Fake is a deterministic offline Client. It echoes the last user text block as
// an assistant reply and reports end_turn, so the agent core and app layer can
// be tested with no network. It records the last request for assertions.
type Fake struct {
	mu   sync.Mutex
	last Request
	err  error
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) CreateMessage(_ context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.last = req
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return Response{}, err
	}
	if strings.Contains(req.System, "independent outcome grader") {
		return Response{
			Content: []domain.ContentBlock{{
				Type: "text", Text: `{"result":"satisfied","explanation":"The requested outcome satisfies the rubric."}`,
			}},
			StopReason: "end_turn",
		}, nil
	}

	if tool, ok := firstClientTool(req.Tools); ok && !hasToolResult(req.Messages) {
		return Response{
			Content:    []domain.ContentBlock{{Type: "tool_use", ToolUseID: "fake_tool_1", ToolName: tool.Name, Input: map[string]any{}}},
			StopReason: "tool_use",
		}, nil
	}

	var lastUser string
	for _, m := range req.Messages {
		if m.Role != domain.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				lastUser = b.Text
			}
		}
	}
	return Response{
		Content:    []domain.ContentBlock{{Type: "text", Text: "echo: " + lastUser}},
		StopReason: "end_turn",
	}, nil
}

func firstClientTool(tools []ToolSchema) (ToolSchema, bool) {
	for _, tool := range tools {
		if tool.Type == "" {
			return tool, true
		}
	}
	return ToolSchema{}, false
}

// SetError makes subsequent calls return err after recording their request.
// It is intended for deterministic model-boundary and orchestration tests.
func (f *Fake) SetError(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// CreateMessageStream mirrors CreateMessage but streams the reply text in
// deterministic chunks before returning. It computes the same Response
// CreateMessage would, then, for a text reply, splits the text into >=2 ordered
// chunks and calls onDelta(0, chunk) for each. The concatenation of the chunks
// equals the final text, so streamed and non-streamed callers observe identical
// content. When the turn is a tool_use (the tool branch), no text is streamed:
// onDelta is not called and the tool_use Response is returned directly.
func (f *Fake) CreateMessageStream(ctx context.Context, req Request, onDelta func(index int, text string)) (Response, error) {
	resp, err := f.CreateMessage(ctx, req)
	if err != nil {
		return Response{}, err
	}
	// Only stream a single text block; tool_use (or anything else) returns whole.
	if resp.StopReason != "end_turn" || len(resp.Content) != 1 || resp.Content[0].Type != "text" {
		return resp, nil
	}
	if onDelta != nil {
		for _, chunk := range chunkText(resp.Content[0].Text) {
			if err := ctx.Err(); err != nil {
				return Response{}, err
			}
			onDelta(0, chunk)
		}
	}
	return resp, nil
}

// chunkText splits s into >=2 ordered pieces whose concatenation equals s. It
// splits on spaces (keeping the trailing space on each non-final word so the
// pieces rejoin exactly); if s has no interior split point it is halved by rune
// count so callers still observe at least two deltas.
func chunkText(s string) []string {
	if s == "" {
		return []string{"", ""}
	}
	var chunks []string
	rest := s
	for {
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			chunks = append(chunks, rest)
			break
		}
		chunks = append(chunks, rest[:i+1])
		rest = rest[i+1:]
	}
	if len(chunks) >= 2 {
		return chunks
	}
	// No usable space boundary: split roughly in half on a rune boundary.
	runes := []rune(s)
	if len(runes) < 2 {
		return []string{s, ""}
	}
	mid := len(runes) / 2
	return []string{string(runes[:mid]), string(runes[mid:])}
}

func (f *Fake) LastRequest() Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func hasToolResult(msgs []domain.Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}
