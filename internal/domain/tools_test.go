package domain

import "testing"

func TestParseTools_BuiltinCustomMCP(t *testing.T) {
	raw := []any{
		map[string]any{
			"type":           "agent_toolset_20260401",
			"default_config": map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "always_allow"}},
			"configs": []any{
				map[string]any{"name": "bash", "permission_policy": map[string]any{"type": "always_ask"}},
				map[string]any{"name": "web_fetch", "enabled": false},
			},
		},
		map[string]any{"type": "custom", "name": "get_weather", "description": "d",
			"input_schema": map[string]any{"type": "object"}},
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "github"},
	}
	ts, err := ParseTools(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Builtin == nil || len(ts.Custom) != 1 || len(ts.MCP) != 1 {
		t.Fatalf("parsed = %+v", ts)
	}
	if ts.Custom[0].Name != "get_weather" {
		t.Fatalf("custom = %+v", ts.Custom)
	}
	// bash overridden to always_ask, still enabled by default
	if en, pol := ts.BuiltinEnabled("bash"); !en || pol.Type != "always_ask" {
		t.Fatalf("bash en=%v pol=%v", en, pol.Type)
	}
	// web_fetch disabled
	if en, _ := ts.BuiltinEnabled("web_fetch"); en {
		t.Fatal("web_fetch should be disabled")
	}
	// read: default enabled + default always_allow
	if en, pol := ts.BuiltinEnabled("read"); !en || pol.Type != "always_allow" {
		t.Fatalf("read en=%v pol=%v", en, pol.Type)
	}
}

func TestParseTools_RejectsUnknownType(t *testing.T) {
	_, err := ParseTools([]any{map[string]any{"type": "bogus"}})
	if err == nil {
		t.Fatal("expected error for unknown tool type")
	}
}

func TestParseTools_Empty(t *testing.T) {
	ts, err := ParseTools(nil)
	if err != nil || ts.Builtin != nil || len(ts.Custom) != 0 {
		t.Fatalf("empty parse = %+v err=%v", ts, err)
	}
}
