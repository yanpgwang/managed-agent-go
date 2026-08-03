package domain

import (
	"net/netip"
	"strings"
	"testing"
)

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
		map[string]any{
			"type":            "mcp_toolset",
			"mcp_server_name": "github",
			"configs": []any{map[string]any{
				"name": "delete_issue",
				"permission_policy": map[string]any{
					"type": "always_allow",
				},
			}},
		},
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
	if en, pol := ts.MCP[0].ToolEnabled("list_issues"); !en || pol.Type != "always_ask" {
		t.Fatalf("MCP default en=%v pol=%v", en, pol.Type)
	}
	if en, pol := ts.MCP[0].ToolEnabled("delete_issue"); !en || pol.Type != "always_allow" {
		t.Fatalf("MCP override en=%v pol=%v", en, pol.Type)
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

func TestParseMCPServers_ValidatesIdentityAndURL(t *testing.T) {
	servers, err := ParseMCPServers([]any{map[string]any{
		"type": "url", "name": "github", "url": "https://example.com/mcp",
	}})
	if err != nil || servers["github"].URL != "https://example.com/mcp" {
		t.Fatalf("servers=%#v err=%v", servers, err)
	}
	for _, raw := range [][]any{
		{map[string]any{"type": "url", "name": "x", "url": "file:///tmp/mcp"}},
		{map[string]any{"type": "url", "name": "x", "url": "https://u:p@example.com/mcp"}},
		{map[string]any{"type": "url", "name": "x", "url": "http://127.0.0.1/mcp"}},
		{map[string]any{"type": "url", "name": "x", "url": "http://169.254.169.254/mcp"}},
		{map[string]any{"type": "url", "name": "x", "url": "http://[::1]/mcp"}},
		{
			map[string]any{"type": "url", "name": "x", "url": "https://one.example"},
			map[string]any{"type": "url", "name": "x", "url": "https://two.example"},
		},
	} {
		if _, err := ParseMCPServers(raw); err == nil {
			t.Fatalf("expected invalid MCP servers to fail: %#v", raw)
		}
	}
}

func TestParseMCPServers_RejectsUnsupportedFields(t *testing.T) {
	for _, field := range []string{"authorization_token", "headers", "tool_configuration"} {
		t.Run(field, func(t *testing.T) {
			_, err := ParseMCPServers([]any{map[string]any{
				"type": "url", "name": "github", "url": "https://example.com/mcp",
				field: "secret",
			}})
			if err == nil {
				t.Fatalf("unsupported field %q was accepted", field)
			}
			if !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error should name only the field: %v", err)
			}
		})
	}
}

func TestMCPAddressAllowed(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"169.254.169.254",
		"192.168.1.1",
		"::1",
		"fc00::1",
		"fe80::1",
		"2001:db8::1",
	} {
		address := netip.MustParseAddr(raw)
		if MCPAddressAllowed(address) {
			t.Errorf("non-public address %s was allowed", raw)
		}
	}
	for _, raw := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"2606:4700:4700::1111",
	} {
		address := netip.MustParseAddr(raw)
		if !MCPAddressAllowed(address) {
			t.Errorf("public address %s was rejected", raw)
		}
	}
}

func TestValidateToolConfiguration_RequiresMatchingMCPPairs(t *testing.T) {
	server := []any{map[string]any{
		"type": "url", "name": "github", "url": "https://mcp.example.com",
	}}
	toolset := []any{map[string]any{
		"type": "mcp_toolset", "mcp_server_name": "github",
	}}
	if err := ValidateToolConfiguration(toolset, server); err != nil {
		t.Fatalf("valid MCP pair: %v", err)
	}
	if err := ValidateToolConfiguration(nil, server); err == nil {
		t.Fatal("expected unreferenced MCP server to fail")
	}
	if err := ValidateToolConfiguration(toolset, nil); err == nil {
		t.Fatal("expected dangling MCP toolset to fail")
	}
}

func TestValidateToolConfiguration_RejectsApprovalForNativeWeb(t *testing.T) {
	err := ValidateToolConfiguration([]any{map[string]any{
		"type": BuiltinToolsetType,
		"configs": []any{map[string]any{
			"name": "web_search",
			"permission_policy": map[string]any{
				"type": "always_ask",
			},
		}},
	}}, nil)
	if err == nil {
		t.Fatal("expected provider-native web approval policy to fail")
	}
}

func TestParseTools_RejectsDuplicateAndUnknownConfiguration(t *testing.T) {
	cases := [][]any{
		{
			map[string]any{"type": BuiltinToolsetType},
			map[string]any{"type": BuiltinToolsetType},
		},
		{map[string]any{
			"type": BuiltinToolsetType,
			"configs": []any{
				map[string]any{"name": "not_a_builtin"},
			},
		}},
		{map[string]any{
			"type": "mcp_toolset", "mcp_server_name": "github",
			"default_config": map[string]any{
				"permission_policy": map[string]any{"type": "sometimes"},
			},
		}},
		{map[string]any{
			"type": "custom", "name": "bash",
		}},
	}
	for _, raw := range cases {
		if _, err := ParseTools(raw); err == nil {
			t.Fatalf("expected invalid tool configuration to fail: %#v", raw)
		}
	}
}
