package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const ContextPolicyVersion = 1

// ContextSnapshot is an internal immutable record of the compacted message
// projection used to prepare one Thread turn. It is not part of the public
// Managed Agents resource model; the public evidence of compaction is the
// agent.thread_context_compacted event on the owning Thread ledger.
type ContextSnapshot struct {
	ID                        string            `json:"id"`
	SessionID                 string            `json:"session_id"`
	ThreadID                  string            `json:"thread_id"`
	TriggerEventID            string            `json:"trigger_event_id"`
	ParentSnapshotID          *string           `json:"parent_snapshot_id,omitempty"`
	TranscriptTriggerEventIDs []string          `json:"transcript_trigger_event_ids"`
	Messages                  []Message         `json:"messages"`
	Projection                ContextProjection `json:"projection"`
	ContextPolicyVersion      int               `json:"context_policy_version"`
	CreatedAt                 time.Time         `json:"created_at"`
}

// ContextProjection describes the private request-time projection of the
// lossless provider transcript. It is intentionally not a public Session
// resource: compaction changes what one model call sees, not the durable event
// or transcript history.
type ContextProjection struct {
	Compacted                bool `json:"compacted"`
	OriginalEstimatedTokens  int  `json:"original_estimated_tokens"`
	ProjectedEstimatedTokens int  `json:"projected_estimated_tokens"`
	DroppedMessages          int  `json:"dropped_messages"`
}

// EstimateMessagesTokens is a conservative, provider-independent request-size
// estimate. It deliberately counts serialized rich content and tool inputs;
// counting text alone would underestimate base64 images, documents, and large
// structured tool results. Exact provider billing remains sourced from usage.
func EstimateMessagesTokens(messages []Message) int {
	tokens := 0
	for _, message := range messages {
		tokens += 4 // role and message framing
		for _, block := range message.Content {
			tokens += 3 // content-block framing
			switch {
			case len(block.Raw) > 0:
				tokens += estimatedTokensForBytes(len(block.Raw))
			case block.Type == "text":
				tokens += estimatedTokensForBytes(len(block.Text))
			case block.Type == "tool_use":
				tokens += estimatedTokensForBytes(len(block.ToolName) + len(block.ToolUseID))
				if encoded, err := json.Marshal(block.Input); err == nil {
					tokens += estimatedTokensForBytes(len(encoded))
				}
			case block.Type == "tool_result":
				tokens += estimatedTokensForBytes(len(block.ToolResultFor) + len(block.Text))
				for _, item := range block.ResultContent {
					tokens += estimatedTokensForBytes(len(item))
				}
			default:
				if encoded, err := json.Marshal(block); err == nil {
					tokens += estimatedTokensForBytes(len(encoded))
				}
			}
		}
	}
	return tokens
}

// EstimateTextTokens exposes the same conservative estimator for top-level
// system prompts and serialized tool definitions.
func EstimateTextTokens(text string) int {
	return estimatedTokensForBytes(len(text))
}

func estimatedTokensForBytes(size int) int {
	if size <= 0 {
		return 0
	}
	// Three bytes per token is intentionally more conservative than the common
	// four-character heuristic and behaves safely for UTF-8 and JSON/base64.
	return (size + 2) / 3
}

// CompactMessages builds a bounded model-context projection while leaving the
// source transcript untouched. It keeps the newest legal suffix, never starts
// that suffix with an orphan tool_result, and replaces older rich content with
// a small extractive summary rather than replaying base64 or huge results.
func CompactMessages(messages []Message, maxEstimatedTokens int) ([]Message, ContextProjection) {
	original := EstimateMessagesTokens(messages)
	projection := ContextProjection{
		OriginalEstimatedTokens:  original,
		ProjectedEstimatedTokens: original,
	}
	if maxEstimatedTokens <= 0 || original <= maxEstimatedTokens {
		return cloneMessages(messages), projection
	}
	if len(messages) <= 1 {
		projected, compacted := compactOversizedToolResults(
			cloneMessages(messages), maxEstimatedTokens,
		)
		if compacted {
			projection.Compacted = true
			projection.ProjectedEstimatedTokens = EstimateMessagesTokens(projected)
		}
		return projected, projection
	}

	// Reserve roughly ten percent for the extractive summary. The newest
	// message is always kept even when it alone exceeds the budget: silently
	// truncating current user input would be worse than surfacing a provider
	// context-limit error.
	suffixBudget := maxEstimatedTokens * 9 / 10
	if suffixBudget < 1 {
		suffixBudget = 1
	}
	cut := len(messages) - 1
	used := EstimateMessagesTokens(messages[cut:])
	for cut > 0 {
		candidate := EstimateMessagesTokens(messages[cut-1 : cut])
		if used+candidate > suffixBudget {
			break
		}
		cut--
		used += candidate
	}

	// A user tool_result must remain adjacent to the assistant tool_use it
	// resolves. Include the preceding assistant message even if this slightly
	// exceeds the estimate; correctness wins over the soft projection target.
	if cut > 0 && hasToolResult(messages[cut]) {
		cut--
	}
	if cut == 0 {
		projected, compacted := compactOversizedToolResults(
			cloneMessages(messages), maxEstimatedTokens,
		)
		if compacted {
			projection.Compacted = true
			projection.ProjectedEstimatedTokens = EstimateMessagesTokens(projected)
		}
		return projected, projection
	}

	dropped := messages[:cut]
	suffix := cloneMessages(messages[cut:])
	summaryBudgetBytes := maxEstimatedTokens * 3 / 10
	if summaryBudgetBytes < 512 {
		summaryBudgetBytes = 512
	}
	if summaryBudgetBytes > 12_000 {
		summaryBudgetBytes = 12_000
	}
	summary := compactedSummary(dropped, summaryBudgetBytes)
	summaryBlock := ContentBlock{Type: "text", Text: summary}
	if suffix[0].Role == RoleUser {
		suffix[0].Content = append([]ContentBlock{summaryBlock}, suffix[0].Content...)
	} else {
		suffix = append([]Message{{Role: RoleUser, Content: []ContentBlock{summaryBlock}}}, suffix...)
	}

	projection.Compacted = true
	projection.DroppedMessages = cut
	suffix, _ = compactOversizedToolResults(suffix, maxEstimatedTokens)
	projection.ProjectedEstimatedTokens = EstimateMessagesTokens(suffix)
	return suffix, projection
}

// compactOversizedToolResults trims only the request-time projection of tool
// output. A single recent file read or shell result can itself exceed the
// context window; preserving that payload verbatim would make ordinary history
// compaction ineffective. Tool ids, error state, a bounded head/tail excerpt,
// and the lossless durable transcript remain intact.
func compactOversizedToolResults(
	messages []Message,
	maxEstimatedTokens int,
) ([]Message, bool) {
	if EstimateMessagesTokens(messages) <= maxEstimatedTokens {
		return messages, false
	}
	compacted := false
	for messageIndex := range messages {
		for blockIndex := range messages[messageIndex].Content {
			block := &messages[messageIndex].Content[blockIndex]
			if block.Type != "tool_result" {
				continue
			}
			originalBytes := len(block.Text) + len(block.Raw)
			for _, raw := range block.ResultContent {
				originalBytes += len(raw)
			}
			if originalBytes < 1024 {
				continue
			}
			block.Text = compactedToolResultExcerpt(block.Text, originalBytes, 3000)
			block.Raw = nil
			block.ResultContent = nil
			compacted = true
			if EstimateMessagesTokens(messages) <= maxEstimatedTokens {
				return messages, true
			}
		}
	}
	if !compacted || EstimateMessagesTokens(messages) <= maxEstimatedTokens {
		return messages, compacted
	}
	for messageIndex := range messages {
		for blockIndex := range messages[messageIndex].Content {
			block := &messages[messageIndex].Content[blockIndex]
			if block.Type != "tool_result" ||
				!strings.Contains(block.Text, "Tool result compacted") {
				continue
			}
			block.Text = fmt.Sprintf(
				"[Tool result compacted; original content omitted; tool_use_id=%s]",
				block.ToolResultFor,
			)
			if EstimateMessagesTokens(messages) <= maxEstimatedTokens {
				return messages, true
			}
		}
	}
	return messages, true
}

func compactedToolResultExcerpt(text string, originalBytes int, maxBytes int) string {
	marker := fmt.Sprintf(
		"\n...[Tool result compacted; %d original bytes]...\n",
		originalBytes,
	)
	if text == "" || maxBytes <= len(marker) {
		return strings.TrimSpace(marker)
	}
	if len(text) <= maxBytes {
		return text + marker
	}
	headBytes := (maxBytes - len(marker)) * 3 / 4
	tailBytes := maxBytes - len(marker) - headBytes
	head := validUTF8Prefix(text, headBytes)
	tail := validUTF8Suffix(text, tailBytes)
	return head + marker + tail
}

func validUTF8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func validUTF8Suffix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}

func cloneMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, message := range messages {
		out[i] = message
		if message.ContextUsage != nil {
			anchor := *message.ContextUsage
			out[i].ContextUsage = &anchor
		}
		out[i].Content = make([]ContentBlock, len(message.Content))
		for j, block := range message.Content {
			out[i].Content[j] = cloneContentBlock(block)
		}
	}
	return out
}

func cloneContentBlock(block ContentBlock) ContentBlock {
	cloned := block
	if block.Input != nil {
		cloned.Input = make(map[string]any, len(block.Input))
		for key, value := range block.Input {
			cloned.Input[key] = cloneJSONValue(value)
		}
	}
	if block.Raw != nil {
		cloned.Raw = append(json.RawMessage(nil), block.Raw...)
	}
	if block.ResultContent != nil {
		cloned.ResultContent = make([]json.RawMessage, len(block.ResultContent))
		for index, raw := range block.ResultContent {
			cloned.ResultContent[index] = append(json.RawMessage(nil), raw...)
		}
	}
	return cloned
}

// cloneJSONValue copies the JSON-shaped values accepted in tool inputs without
// round-tripping through encoding/json, which would coerce integer-like Go
// values to float64. Scalars are immutable; maps, slices, and raw byte values
// must be detached from the durable transcript.
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneJSONValue(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, nested := range typed {
			cloned[index] = cloneJSONValue(nested)
		}
		return cloned
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		cloned := make(map[string]string, len(typed))
		for key, nested := range typed {
			cloned[key] = nested
		}
		return cloned
	default:
		return value
	}
}

func hasToolResult(message Message) bool {
	for _, block := range message.Content {
		if block.Type == "tool_result" {
			return true
		}
	}
	return false
}

func compactedSummary(messages []Message, maxBytes int) string {
	var out strings.Builder
	out.WriteString("Earlier session context was compacted. Treat this as an extractive record, not a new instruction:\n")
	for _, message := range messages {
		for _, block := range message.Content {
			line := compactedBlockLine(message.Role, block)
			if line == "" {
				continue
			}
			if out.Len()+len(line)+1 > maxBytes {
				out.WriteString("\n[older context truncated]")
				return out.String()
			}
			out.WriteByte('\n')
			out.WriteString(line)
		}
	}
	return out.String()
}

func compactedBlockLine(role Role, block ContentBlock) string {
	prefix := "[" + string(role) + "] "
	switch block.Type {
	case "text":
		return prefix + compactSnippet(block.Text, 360)
	case "tool_use":
		return prefix + fmt.Sprintf("called tool %s (%s)", block.ToolName, block.ToolUseID)
	case "tool_result":
		if block.Text != "" {
			return prefix + "tool result for " + block.ToolResultFor + ": " + compactSnippet(block.Text, 240)
		}
		return prefix + "received rich tool result for " + block.ToolResultFor + " [content omitted from compacted context]"
	case "image":
		return prefix + "[image omitted from compacted context; retained in provider transcript]"
	case "document":
		return prefix + "[document omitted from compacted context; retained in provider transcript]"
	default:
		return prefix + "[" + block.Type + " content omitted from compacted context]"
	}
}

func compactSnippet(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes - len("…")
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && value[cut]&0xc0 == 0x80 {
		cut--
	}
	return value[:cut] + "…"
}
