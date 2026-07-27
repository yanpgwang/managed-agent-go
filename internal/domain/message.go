package domain

import "strings"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type ContentBlock struct {
	Type          string         // "text" | "tool_use" | "tool_result"
	Text          string         // text blocks; also flattened text of a tool_result
	ToolUseID     string         // tool_use: the event id
	ToolName      string         // tool_use: tool name
	Input         map[string]any // tool_use: arguments
	ToolResultFor string         // tool_result: the tool_use id it answers
	IsError       bool           // tool_result: error flag
}

type Message struct {
	Role    Role
	Content []ContentBlock
}

// ProjectMessages folds an ordered session event log into a Messages-API
// conversation. S1 handles only user.message and agent.message text blocks;
// status, error, and server-only events are skipped, as are turns that carry no
// non-empty text. This is where "the server owns history" is realized: the
// durable event log is the single truth, projected to the model every turn.
//
// The real Messages API requires strictly alternating roles. Two real flows
// produce consecutive same-role messages: a user sending several user.message
// events before drainRuns claims them, and a model turn that emits no text
// (so no agent.message) leaving two user turns adjacent. To keep the request
// legal we merge adjacent same-role messages into one, concatenating their
// content blocks in order. This is a pure transformation of the event log.
func ProjectMessages(events []Event) []Message {
	// First pass: collect two sets in one scan.
	//
	//   answered — tool_use ids that some later tool_result references
	//   (agent.tool_result.tool_use_id / user.custom_tool_result.custom_tool_use_id,
	//   both pointing at a tool_use's committed Event.ID). A tool_use whose id is
	//   absent is dangling (e.g. an always_ask built-in that parked and never
	//   resumed); emitting the unpaired tool_use would make the projected request
	//   illegal (400), so those blocks are dropped in the second pass.
	//
	//   seen — ids that an actual agent.tool_use / agent.custom_tool_use event
	//   committed. Symmetrically, a tool_result whose referenced id is not in this
	//   set is an orphan (e.g. a client forging a user.custom_tool_result with a
	//   bogus custom_tool_use_id); emitting the unpaired tool_result would likewise
	//   make the request illegal (400), so those blocks are dropped too.
	answered := make(map[string]struct{})
	seen := make(map[string]struct{})
	for _, e := range events {
		switch e.Type {
		case EvAgentToolUse, EvAgentCustomToolUse:
			id := e.ID
			if id == "" {
				id, _ = e.Payload["id"].(string)
			}
			if id != "" {
				seen[id] = struct{}{}
			}
		case EvAgentToolResult:
			if id, _ := e.Payload["tool_use_id"].(string); id != "" {
				answered[id] = struct{}{}
			}
		case EvUserCustomToolResult:
			if id, _ := e.Payload["custom_tool_use_id"].(string); id != "" {
				answered[id] = struct{}{}
			}
		}
	}

	var out []Message
	add := func(role Role, blocks []ContentBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, Message{Role: role, Content: blocks})
	}
	for _, e := range events {
		switch e.Type {
		case EvUserMessage:
			add(RoleUser, textBlocks(e.Payload))
		case EvAgentMessage:
			add(RoleAssistant, textBlocks(e.Payload))
		case EvAgentToolUse, EvAgentCustomToolUse:
			// The correlation id is the committed event id (Event.ID), the same
			// value the public wire exposes and the value a tool_result event's
			// tool_use_id points at. payload["id"] holds the model's transient
			// id and is only a fallback for constructions without a committed ID.
			id := e.ID
			if id == "" {
				id, _ = e.Payload["id"].(string)
			}
			if id == "" {
				continue
			}
			// Drop dangling tool_use blocks: an unpaired tool_use makes the
			// projected request illegal. A paired tool_use (its id appears in a
			// later tool_result) is kept unchanged.
			if _, ok := answered[id]; !ok {
				continue
			}
			name, _ := e.Payload["name"].(string)
			input, _ := e.Payload["input"].(map[string]any)
			add(RoleAssistant, []ContentBlock{{Type: "tool_use", ToolUseID: id, ToolName: name, Input: input}})
		case EvAgentToolResult:
			id, _ := e.Payload["tool_use_id"].(string)
			if id == "" {
				continue
			}
			// Drop orphan tool_result blocks: a result referencing a tool_use id
			// that no committed tool_use event produced is unpaired and would make
			// the projected request illegal. Symmetric to the dangling-tool_use drop.
			if _, ok := seen[id]; !ok {
				continue
			}
			add(RoleUser, []ContentBlock{resultBlock(id, e.Payload)})
		case EvUserCustomToolResult:
			id, _ := e.Payload["custom_tool_use_id"].(string)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; !ok {
				continue
			}
			add(RoleUser, []ContentBlock{resultBlock(id, e.Payload)})
		default:
			continue
		}
	}
	return out
}

func resultBlock(toolUseID string, payload map[string]any) ContentBlock {
	b := ContentBlock{Type: "tool_result", ToolResultFor: toolUseID}
	b.IsError, _ = payload["is_error"].(bool)
	b.Text = flattenText(payload["content"])
	return b
}

func flattenText(raw any) string {
	blocks, ok := raw.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, item := range blocks {
		if m, ok := item.(map[string]any); ok {
			if t, _ := m["type"].(string); t == "text" {
				if s, _ := m["text"].(string); s != "" {
					sb.WriteString(s)
				}
			}
		}
	}
	return sb.String()
}

func textBlocks(payload map[string]any) []ContentBlock {
	raw, ok := payload["content"].([]any)
	if !ok {
		return nil
	}
	var blocks []ContentBlock
	for _, item := range raw {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := block["type"].(string); t != "text" {
			continue
		}
		text, _ := block["text"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		blocks = append(blocks, ContentBlock{Type: "text", Text: text})
	}
	return blocks
}
