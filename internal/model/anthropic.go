package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

var _ Client = (*Anthropic)(nil)

const anthropicVersion = "2023-06-01"
const defaultMaxTokens = 4096

// AnthropicConfig configures the real Messages-API client. Everything comes
// from the caller (env in production); no value is hardcoded and no credential
// is ever compiled in or logged.
type AnthropicConfig struct {
	BaseURL    string // e.g. https://api.anthropic.com
	APIKey     string
	Model      string
	AuthHeader string // "x-api-key" (default) or "authorization-bearer"
	HTTPClient *http.Client
}

type Anthropic struct {
	cfg  AnthropicConfig
	http *http.Client
}

func NewAnthropic(cfg AnthropicConfig) (*Anthropic, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("model: anthropic base URL is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("model: anthropic API key is required")
	}
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "x-api-key"
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Anthropic{cfg: cfg, http: hc}, nil
}

// AnthropicFromEnv builds a client from environment variables. It returns
// (nil, false, nil) when the base URL or key is unset, so the caller falls back
// to the offline fake without error.
func AnthropicFromEnv() (*Anthropic, bool, error) {
	base := os.Getenv("MANAGED_AGENT_MODEL_BASE_URL")
	key := os.Getenv("MANAGED_AGENT_MODEL_API_KEY")
	if base == "" || key == "" {
		return nil, false, nil
	}
	auth := os.Getenv("MANAGED_AGENT_MODEL_AUTH")
	c, err := NewAnthropic(AnthropicConfig{
		BaseURL:    base,
		APIKey:     key,
		Model:      os.Getenv("MANAGED_AGENT_MODEL_ID"),
		AuthHeader: auth,
	})
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// wireBlock is one content block in the Anthropic Messages wire format. A block
// is a tagged union keyed on Type; only the fields relevant to that type are
// emitted (omitempty), so a text block carries text, a tool_use block carries
// id/name/input, and a tool_result block carries tool_use_id/content/is_error.
type wireBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	// tool_result
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   []wireBlock `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}
type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}
type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}
type wireRequest struct {
	Model     string        `json:"model"`
	System    string        `json:"system,omitempty"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream,omitempty"`
}
type wireResponse struct {
	Content    []wireBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
}

// buildWireRequest maps a domain Request to the Anthropic Messages wire request,
// applying config defaults for model and max_tokens. stream toggles the
// server-sent-events variant ("stream": true).
func (a *Anthropic) buildWireRequest(req Request, stream bool) wireRequest {
	model := req.Model
	if model == "" {
		model = a.cfg.Model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	body := wireRequest{Model: model, System: req.System, MaxTokens: maxTokens, Stream: stream}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, wireTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role)}
		for _, b := range m.Content {
			wm.Content = append(wm.Content, toWireBlock(b))
		}
		body.Messages = append(body.Messages, wm)
	}
	return body
}

// newHTTPRequest marshals the wire body and builds the POST /v1/messages request
// with version and auth headers set. No credential is ever logged.
func (a *Anthropic) newHTTPRequest(ctx context.Context, body wireRequest) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if a.cfg.AuthHeader == "authorization-bearer" {
		httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	} else {
		httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	}
	return httpReq, nil
}

func (a *Anthropic) CreateMessage(ctx context.Context, req Request) (Response, error) {
	httpReq, err := a.newHTTPRequest(ctx, a.buildWireRequest(req, false))
	if err != nil {
		return Response{}, err
	}

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return Response{}, classifyRequestError(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, classifyHTTPError(resp.StatusCode, raw, resp.Header)
	}
	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return Response{}, fmt.Errorf("model: decode response: %w", err)
	}
	out := Response{StopReason: wr.StopReason}
	for _, b := range wr.Content {
		cb := domain.ContentBlock{Type: b.Type, Text: b.Text}
		if b.Type == "tool_use" {
			cb.ToolUseID = b.ID
			cb.ToolName = b.Name
			cb.Input = b.Input
		}
		out.Content = append(out.Content, cb)
	}
	return out, nil
}

// CreateMessageStream opens the Messages-API server-sent-events stream
// ("stream": true) and decodes it incrementally. It scans the SSE body line by
// line, parses each `data:` payload as JSON, and dispatches on the top-level
// `type`:
//
//   - content_block_start   — opens a block at `index`; for tool_use it captures
//     id/name so the block can be assembled from later deltas.
//   - content_block_delta    — for a text_delta it accumulates text and calls
//     onDelta(index, text) per chunk; for an input_json_delta it accumulates the
//     tool_use partial_json (no onDelta — tool input is not previewed).
//   - content_block_stop     — finalizes a block; a tool_use block's accumulated
//     partial_json is parsed into its input map here.
//   - message_delta          — captures stop_reason.
//   - message_stop           — end of stream.
//
// It assembles and returns the final Response (content blocks in index order +
// stop_reason). A non-2xx status is handled exactly as the non-streaming path:
// the sanitized, length-bounded upstream body is folded into the returned error.
// The request context threads through the HTTP read, so a cancelled context
// aborts the stream.
//
// tool_use streaming coverage: text is fully incremental; tool_use blocks are
// assembled from content_block_start + accumulated input_json_delta and parsed
// at content_block_stop. This is enough for the tool loop (which needs the
// complete tool call), but partial_json is not surfaced incrementally.
func (a *Anthropic) CreateMessageStream(ctx context.Context, req Request, onDelta func(index int, text string)) (Response, error) {
	httpReq, err := a.newHTTPRequest(ctx, a.buildWireRequest(req, true))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := a.http.Do(httpReq)
	if err != nil {
		return Response{}, classifyRequestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return Response{}, classifyHTTPError(resp.StatusCode, raw, resp.Header)
	}
	return decodeMessageStream(resp.Body, onDelta)
}

// streamBlock accumulates one content block as its deltas arrive over the SSE
// stream. text blocks fill text; tool_use blocks fill id/name and buffer the
// tool input as partial_json fragments until the block stops.
type streamBlock struct {
	typ         string
	text        strings.Builder
	toolID      string
	toolName    string
	toolInput   map[string]any
	partialJSON strings.Builder
}

// sseEvent is the union of Messages-API streaming event fields we read. Only the
// fields relevant to a given `type` are populated; the rest stay zero.
type sseEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string         `json:"type"`
		Text  string         `json:"text"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

// decodeMessageStream reads an Anthropic Messages-API SSE body and assembles the
// final Response, invoking onDelta for each text_delta.
func decodeMessageStream(body io.Reader, onDelta func(index int, text string)) (Response, error) {
	sc := bufio.NewScanner(body)
	// Allow long data: lines (a single tool_use input_json_delta or a large text
	// chunk can exceed the default 64 KiB token size).
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	blocks := map[int]*streamBlock{}
	var order []int
	stopReason := ""

	finalize := func(idx int) {
		b := blocks[idx]
		if b == nil || b.typ != "tool_use" {
			return
		}
		if pj := b.partialJSON.String(); strings.TrimSpace(pj) != "" {
			var input map[string]any
			if err := json.Unmarshal([]byte(pj), &input); err == nil {
				b.toolInput = input
			}
		}
	}

	for sc.Scan() {
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			// event:, id:, retry:, comments, and blank separators are ignored;
			// dispatch is driven entirely by the JSON `type` on data: lines.
			continue
		}
		// Assumes one complete JSON payload per data: line, which is what the
		// Anthropic Messages API emits. The SSE spec permits an event's data to
		// span multiple data: lines (joined by "\n"); we do not reassemble those.
		// If the upstream ever splits a payload across lines, each fragment would
		// fail to parse — revisit with a per-event data-line accumulator then.
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return Response{}, fmt.Errorf("model: decode stream event: %w", err)
		}
		switch ev.Type {
		case "content_block_start":
			b := &streamBlock{typ: ev.ContentBlock.Type}
			if b.typ == "" {
				b.typ = "text"
			}
			if b.typ == "text" {
				b.text.WriteString(ev.ContentBlock.Text)
			}
			if b.typ == "tool_use" {
				b.toolID = ev.ContentBlock.ID
				b.toolName = ev.ContentBlock.Name
				b.toolInput = ev.ContentBlock.Input
			}
			if _, seen := blocks[ev.Index]; !seen {
				order = append(order, ev.Index)
			}
			blocks[ev.Index] = b
		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil {
				b = &streamBlock{typ: "text"}
				blocks[ev.Index] = b
				order = append(order, ev.Index)
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.text.WriteString(ev.Delta.Text)
				if onDelta != nil && ev.Delta.Text != "" {
					onDelta(ev.Index, ev.Delta.Text)
				}
			case "input_json_delta":
				b.partialJSON.WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			finalize(ev.Index)
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
		case "message_stop":
			// End of stream; loop exits when the body is drained.
		}
	}
	if err := sc.Err(); err != nil {
		return Response{}, fmt.Errorf("model: read stream: %w", err)
	}
	// Finalize any tool_use block that never received an explicit stop.
	for _, idx := range order {
		finalize(idx)
	}

	out := Response{StopReason: stopReason}
	for _, idx := range order {
		b := blocks[idx]
		cb := domain.ContentBlock{Type: b.typ}
		switch b.typ {
		case "tool_use":
			cb.ToolUseID = b.toolID
			cb.ToolName = b.toolName
			cb.Input = b.toolInput
		default:
			cb.Text = b.text.String()
		}
		out.Content = append(out.Content, cb)
	}
	return out, nil
}

// toWireBlock maps a domain ContentBlock to its Anthropic Messages wire shape.
// text blocks carry only text; tool_use blocks carry id/name/input; tool_result
// blocks carry tool_use_id, is_error, and a content array of text blocks (the
// wire shape the API requires, [{type:"text",text:...}]).
func toWireBlock(b domain.ContentBlock) wireBlock {
	switch b.Type {
	case "tool_use":
		return wireBlock{Type: "tool_use", ID: b.ToolUseID, Name: b.ToolName, Input: b.Input}
	case "tool_result":
		wb := wireBlock{Type: "tool_result", ToolUseID: b.ToolResultFor, IsError: b.IsError}
		if b.Text != "" {
			wb.Content = []wireBlock{{Type: "text", Text: b.Text}}
		}
		return wb
	default:
		return wireBlock{Type: b.Type, Text: b.Text}
	}
}

const maxUpstreamErrorLen = 512

// sanitizeErrorText collapses whitespace/control characters to single spaces and
// truncates to maxUpstreamErrorLen bytes so a hostile or verbose upstream body
// cannot produce a multi-line or unbounded error string.
func sanitizeErrorText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxUpstreamErrorLen {
		// Truncate on a rune boundary: back up from the byte limit until we are
		// at the start of a valid rune, so we never split a multibyte UTF-8
		// sequence and never emit invalid UTF-8.
		cut := maxUpstreamErrorLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…(truncated)"
	}
	return s
}
