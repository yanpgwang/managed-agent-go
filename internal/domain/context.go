package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
	if maxEstimatedTokens <= 0 || original <= maxEstimatedTokens || len(messages) <= 1 {
		return cloneMessages(messages), projection
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
		return cloneMessages(messages), projection
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
	projection.ProjectedEstimatedTokens = EstimateMessagesTokens(suffix)
	return suffix, projection
}

func cloneMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	for i, message := range messages {
		out[i] = message
		out[i].Content = append([]ContentBlock(nil), message.Content...)
	}
	return out
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
