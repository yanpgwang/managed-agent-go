// Package model defines the inference client the agent core calls. The
// interface is deliberately small and vendor-neutral so the transport (real
// Messages API, offline fake, or another provider) is swappable without
// touching the agent core or the domain.
package model

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type ToolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Request struct {
	Model     string
	System    string
	Messages  []domain.Message
	MaxTokens int
	Tools     []ToolSchema
}

type Response struct {
	Content    []domain.ContentBlock
	StopReason string
}

type Client interface {
	CreateMessage(ctx context.Context, req Request) (Response, error)

	// CreateMessageStream is the streaming variant of CreateMessage. It invokes
	// onDelta once per text delta (index is the content-block index; currently
	// always 0), in order, as the reply is produced, then returns the same
	// Response CreateMessage would return for the same request. Callers that do
	// not care about incremental deltas can call CreateMessage instead.
	//
	// When the response is not streamable text (e.g. a tool_use turn), onDelta
	// is not called and the full Response is returned directly.
	CreateMessageStream(ctx context.Context, req Request, onDelta func(index int, text string)) (Response, error)
}
