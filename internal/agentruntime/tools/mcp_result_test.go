package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/mcpclient"
)

func TestProjectMCPResult_SeparatesRawModelAndBinaryContent(t *testing.T) {
	sb := newSB(t)
	input := mcpclient.Result{
		Raw: json.RawMessage(`{
			"_meta":{"trace":"private-meta"},
			"content":[
				{"type":"text","text":"hello"},
				{"type":"image","mimeType":"image/png","data":"aW1hZ2UtYnl0ZXM="},
				{"type":"future_control","_meta":{"secret":"do-not-project"}}
			],
			"structuredContent":{"count":2}
		}`),
	}
	result, raw, rawPath, err := ProjectMCPResult(
		context.Background(),
		sb,
		"sevt_mcp",
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rawPath != "" || !strings.Contains(string(raw), "private-meta") {
		t.Fatalf("raw=%s rawPath=%q", raw, rawPath)
	}
	text := result.Content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "hello") ||
		!strings.Contains(text, "tool-results/sevt_mcp-1.png") ||
		!strings.Contains(text, `"count": 2`) {
		t.Fatalf("model projection = %q", text)
	}
	if strings.Contains(text, "private-meta") {
		t.Fatalf("MCP _meta leaked into model projection: %q", text)
	}
	if strings.Contains(text, "do-not-project") ||
		!strings.Contains(text, "unsupported future_control content") {
		t.Fatalf("unknown MCP control content was projected unsafely: %q", text)
	}
	binary, err := sb.ReadFile(
		context.Background(),
		"tool-results/sevt_mcp-1.png",
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "image-bytes" {
		t.Fatalf("binary = %q", binary)
	}
}

func TestProjectMCPResult_LargeRawIsSandboxReference(t *testing.T) {
	sb := newSB(t)
	raw := json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"_meta":{"large":"` +
		strings.Repeat("x", MaxInlineResultChars) + `"}}`)
	_, inline, rawPath, err := ProjectMCPResult(
		context.Background(),
		sb,
		"sevt_raw",
		mcpclient.Result{Raw: raw},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inline != nil || rawPath != "tool-results/sevt_raw.mcp.json" {
		t.Fatalf("inline=%s rawPath=%q", inline, rawPath)
	}
	stored, err := sb.ReadFile(context.Background(), rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(raw) {
		t.Fatal("raw MCP result was not preserved")
	}
}
