package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactMessagesLeavesTranscriptUntouchedWithinBudget(t *testing.T) {
	raw := json.RawMessage(`{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}`)
	result := json.RawMessage(`{"type":"text","text":"done"}`)
	messages := []Message{{
		Role: RoleUser,
		Content: []ContentBlock{
			{
				Type: "tool_use", ToolUseID: "tool_1", ToolName: "inspect",
				Input: map[string]any{
					"nested": map[string]any{"items": []any{"original"}},
				},
				Raw: raw,
			},
			{Type: "tool_result", ToolResultFor: "tool_1", ResultContent: []json.RawMessage{result}},
		},
	}}
	got, projection := CompactMessages(messages, 100)
	if projection.Compacted {
		t.Fatal("small transcript was unexpectedly compacted")
	}
	if len(got) != 1 || len(got[0].Content) != 2 {
		t.Fatalf("projection = %#v", got)
	}
	got[0].Content[0].Input["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	got[0].Content[0].Raw[0] = '['
	got[0].Content[1].ResultContent[0][0] = '['
	if nested := messages[0].Content[0].Input["nested"].(map[string]any)["items"].([]any)[0]; nested != "original" {
		t.Fatalf("projection mutated nested tool input: %#v", nested)
	}
	if messages[0].Content[0].Raw[0] != '{' || messages[0].Content[1].ResultContent[0][0] != '{' {
		t.Fatal("projection mutated raw transcript content")
	}
}

func TestCompactMessagesKeepsNewestContextAndSummarizesRichHistory(t *testing.T) {
	image, _ := json.Marshal(map[string]any{
		"type": "image", "source": map[string]any{
			"type": "base64", "media_type": "image/png", "data": strings.Repeat("A", 6000),
		},
	})
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "image", Raw: image}}},
		{Role: RoleAssistant, Content: []ContentBlock{{Type: "text", Text: strings.Repeat("old analysis ", 600)}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "current request"}}},
	}

	got, projection := CompactMessages(messages, 500)
	if !projection.Compacted || projection.DroppedMessages == 0 {
		t.Fatalf("projection = %#v", projection)
	}
	if got[len(got)-1].Role != RoleUser {
		t.Fatalf("last role = %s, want user", got[len(got)-1].Role)
	}
	lastText := got[len(got)-1].Content[len(got[len(got)-1].Content)-1].Text
	if lastText != "current request" {
		t.Fatalf("newest input = %q", lastText)
	}
	if !strings.Contains(got[0].Content[0].Text, "image omitted") {
		t.Fatalf("summary = %q", got[0].Content[0].Text)
	}
	if len(messages[0].Content[0].Raw) == 0 {
		t.Fatal("lossless rich content was mutated")
	}
}

func TestCompactMessagesNeverOrphansToolResultAtBoundary(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: strings.Repeat("old ", 1000)}}},
		{Role: RoleAssistant, Content: []ContentBlock{{
			Type: "tool_use", ToolUseID: "tool_1", ToolName: "read",
			Input: map[string]any{"path": strings.Repeat("x", 5000)},
		}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: "tool_result", ToolResultFor: "tool_1", Text: "ok"}}},
		{Role: RoleAssistant, Content: []ContentBlock{{Type: "text", Text: "done"}}},
	}

	got, projection := CompactMessages(messages, 500)
	if !projection.Compacted {
		t.Fatal("expected compaction")
	}
	toolUse := -1
	toolResult := -1
	for i, message := range got {
		for _, block := range message.Content {
			switch block.Type {
			case "tool_use":
				toolUse = i
			case "tool_result":
				toolResult = i
			}
		}
	}
	if toolUse < 0 || toolResult != toolUse+1 {
		t.Fatalf("tool pair was split: use=%d result=%d projection=%#v", toolUse, toolResult, got)
	}
}

func TestEstimateMessagesTokensCountsRichSerializedContent(t *testing.T) {
	small := []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "x"}}}}
	large := []Message{{Role: RoleUser, Content: []ContentBlock{{
		Type: "document", Raw: json.RawMessage(`{"type":"document","data":"` + strings.Repeat("A", 3000) + `"}`),
	}}}}
	if EstimateMessagesTokens(large) <= EstimateMessagesTokens(small)+900 {
		t.Fatalf("rich document was not included in token estimate")
	}
}
