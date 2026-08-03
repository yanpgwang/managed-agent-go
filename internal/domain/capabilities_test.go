package domain

import "testing"

// The agent capability payloads are stored as opaque JSON and echoed verbatim
// by GET /v1/agents/{id} and the version list. These tests pin the admission
// boundary that keeps undocumented nested keys -- including anything
// credential-shaped -- from ever reaching that stored state.
func TestParseTools_RejectsUndocumentedNestedFields(t *testing.T) {
	cases := map[string][]any{
		"built-in toolset": {map[string]any{
			"type":        BuiltinToolsetType,
			"api_key":     "secret",
			"description": "d",
		}},
		"built-in default_config": {map[string]any{
			"type": BuiltinToolsetType,
			"default_config": map[string]any{
				"enabled":     true,
				"sudo":        true,
				"api_key":     "secret",
				"description": "d",
			},
		}},
		"built-in config": {map[string]any{
			"type":    BuiltinToolsetType,
			"configs": []any{map[string]any{"name": "bash", "sudo": true}},
		}},
		"built-in permission policy": {map[string]any{
			"type": BuiltinToolsetType,
			"configs": []any{map[string]any{
				"name": "bash",
				"permission_policy": map[string]any{
					"type":  "always_ask",
					"scope": "everything",
				},
			}},
		}},
		"custom tool": {map[string]any{
			"type":         "custom",
			"name":         "get_weather",
			"description":  "d",
			"input_schema": map[string]any{"type": "object"},
			"handler_url":  "https://attacker.example",
		}},
		"mcp toolset": {map[string]any{
			"type":                "mcp_toolset",
			"mcp_server_name":     "github",
			"authorization_token": "secret",
		}},
		"mcp toolset default_config": {map[string]any{
			"type":            "mcp_toolset",
			"mcp_server_name": "github",
			"default_config": map[string]any{
				"enabled": true,
				"headers": map[string]any{"Authorization": "Bearer secret"},
			},
		}},
		"mcp toolset config": {map[string]any{
			"type":            "mcp_toolset",
			"mcp_server_name": "github",
			"configs": []any{map[string]any{
				"name":    "list_issues",
				"api_key": "secret",
			}},
		}},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTools(raw); err == nil {
				t.Fatalf("undocumented nested field was accepted: %#v", raw)
			}
		})
	}
}

func TestParseTools_RejectsMalformedNestedValues(t *testing.T) {
	cases := map[string][]any{
		"configs not an array": {map[string]any{
			"type":    BuiltinToolsetType,
			"configs": map[string]any{"name": "bash"},
		}},
		"default_config not an object": {map[string]any{
			"type":           BuiltinToolsetType,
			"default_config": "enabled",
		}},
		"enabled not a boolean": {map[string]any{
			"type":           BuiltinToolsetType,
			"default_config": map[string]any{"enabled": "yes"},
		}},
		"config enabled not a boolean": {map[string]any{
			"type":    BuiltinToolsetType,
			"configs": []any{map[string]any{"name": "bash", "enabled": "yes"}},
		}},
		"permission policy not an object": {map[string]any{
			"type": BuiltinToolsetType,
			"default_config": map[string]any{
				"permission_policy": "always_allow",
			},
		}},
		"custom description not a string": {map[string]any{
			"type": "custom", "name": "get_weather", "description": 7,
		}},
		"custom input_schema not an object": {map[string]any{
			"type": "custom", "name": "get_weather", "input_schema": "object",
		}},
		"custom input_schema wrong type": {map[string]any{
			"type": "custom", "name": "get_weather",
			"input_schema": map[string]any{"type": "string"},
		}},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTools(raw); err == nil {
				t.Fatalf("malformed nested value was accepted: %#v", raw)
			}
		})
	}
}

// A custom tool's input_schema is a caller-authored JSON Schema. Only the
// documented wrapper is checked; its own keywords stay untouched.
func TestParseTools_KeepsCustomInputSchemaOpaque(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string", "description": "d"},
		},
		"required":             []any{"city"},
		"additionalProperties": false,
	}
	parsed, err := ParseTools([]any{map[string]any{
		"type": "custom", "name": "get_weather", "description": "d",
		"input_schema": schema,
	}})
	if err != nil {
		t.Fatalf("rich JSON Schema was rejected: %v", err)
	}
	if len(parsed.Custom) != 1 || len(parsed.Custom[0].InputSchema) != 4 {
		t.Fatalf("input_schema was not preserved: %#v", parsed.Custom)
	}
}

func TestParseMCPServers_RejectsUndocumentedFields(t *testing.T) {
	// The MCP connector guide states that no authentication tokens are supplied
	// at configuration time, so this upstream-shaped field must fail loudly
	// instead of being persisted and echoed back in plaintext.
	for name, raw := range map[string][]any{
		"authorization_token": {map[string]any{
			"type": "url", "name": "github", "url": "https://mcp.example.com",
			"authorization_token": "secret",
		}},
		"headers": {map[string]any{
			"type": "url", "name": "github", "url": "https://mcp.example.com",
			"headers": map[string]any{"Authorization": "Bearer secret"},
		}},
		"tool_configuration": {map[string]any{
			"type": "url", "name": "github", "url": "https://mcp.example.com",
			"tool_configuration": map[string]any{"enabled": true},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMCPServers(raw); err == nil {
				t.Fatalf("undocumented MCP server field was accepted: %#v", raw)
			}
		})
	}
}

func TestValidateSkills_AcceptsDocumentedShape(t *testing.T) {
	if err := ValidateSkills([]any{
		map[string]any{"type": "anthropic", "skill_id": "xlsx"},
		map[string]any{
			"type": "custom", "skill_id": "skill_01ABC", "version": "latest",
		},
	}); err != nil {
		t.Fatalf("documented skills were rejected: %v", err)
	}
}

func TestValidateSkills_RejectsUndocumentedAndMalformed(t *testing.T) {
	for name, raw := range map[string][]any{
		"unknown field": {map[string]any{
			"type": "anthropic", "skill_id": "xlsx", "api_key": "secret",
		}},
		"unknown type":     {map[string]any{"type": "bundled", "skill_id": "xlsx"}},
		"missing skill_id": {map[string]any{"type": "anthropic"}},
		"non-string version": {map[string]any{
			"type": "anthropic", "skill_id": "xlsx", "version": 3,
		}},
		"not an object": {"xlsx"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSkills(raw); err == nil {
				t.Fatalf("invalid skill was accepted: %#v", raw)
			}
		})
	}
}

func TestValidateMultiagent_AcceptsDocumentedRosterForms(t *testing.T) {
	if err := ValidateMultiagent(map[string]any{
		"type": "coordinator",
		"agents": []any{
			"agent_01ABC",
			map[string]any{"type": "agent", "id": "agent_01DEF", "version": float64(2)},
			map[string]any{"type": "self"},
		},
	}); err != nil {
		t.Fatalf("documented roster was rejected: %v", err)
	}
	if err := ValidateMultiagent(nil); err != nil {
		t.Fatalf("absent multiagent was rejected: %v", err)
	}
}

func TestValidateMultiagent_RejectsUndocumentedAndMalformed(t *testing.T) {
	roster := func(entries ...any) map[string]any {
		return map[string]any{"type": "coordinator", "agents": entries}
	}
	for name, raw := range map[string]map[string]any{
		"unknown top-level field": {
			"type": "coordinator", "agents": []any{"agent_01ABC"},
			"webhook_url": "https://attacker.example",
		},
		"unknown topology":       {"type": "swarm", "agents": []any{"agent_01ABC"}},
		"missing roster":         {"type": "coordinator"},
		"empty roster":           roster(),
		"unknown entry field":    roster(map[string]any{"type": "agent", "id": "a", "token": "secret"}),
		"unknown self field":     roster(map[string]any{"type": "self", "depth": 2}),
		"unknown entry type":     roster(map[string]any{"type": "team", "id": "a"}),
		"entry missing id":       roster(map[string]any{"type": "agent"}),
		"empty string entry":     roster(""),
		"duplicate entry":        roster("agent_01ABC", map[string]any{"type": "agent", "id": "agent_01ABC"}),
		"two self entries":       roster(map[string]any{"type": "self"}, map[string]any{"type": "self"}),
		"non-positive version":   roster(map[string]any{"type": "agent", "id": "a", "version": float64(0)}),
		"non-integer version":    roster(map[string]any{"type": "agent", "id": "a", "version": "2"}),
		"entry is not an object": roster(float64(7)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMultiagent(raw); err == nil {
				t.Fatalf("invalid multiagent was accepted: %#v", raw)
			}
		})
	}
	oversized := make([]any, 0, 21)
	for index := range 21 {
		oversized = append(oversized, string(rune('a'+index))+"_agent")
	}
	if err := ValidateMultiagent(roster(oversized...)); err == nil {
		t.Fatal("roster above the documented 20-entry limit was accepted")
	}
}

func TestValidateAgentCapabilities_RejectsEachUndocumentedPayload(t *testing.T) {
	servers := []any{map[string]any{
		"type": "url", "name": "github", "url": "https://mcp.example.com",
	}}
	tools := []any{map[string]any{
		"type": "mcp_toolset", "mcp_server_name": "github",
	}}
	if err := ValidateAgentCapabilities(tools, servers, nil, nil); err != nil {
		t.Fatalf("documented configuration was rejected: %v", err)
	}
	leaky := []any{map[string]any{
		"type": "url", "name": "github", "url": "https://mcp.example.com",
		"authorization_token": "secret",
	}}
	if err := ValidateAgentCapabilities(tools, leaky, nil, nil); err == nil {
		t.Fatal("mcp_servers credential field was accepted")
	}
	if err := ValidateAgentCapabilities(tools, servers, []any{
		map[string]any{"type": "anthropic", "skill_id": "xlsx", "api_key": "secret"},
	}, nil); err == nil {
		t.Fatal("skills credential field was accepted")
	}
	if err := ValidateAgentCapabilities(tools, servers, nil, map[string]any{
		"type": "coordinator", "agents": []any{"agent_01ABC"}, "api_key": "secret",
	}); err == nil {
		t.Fatal("multiagent credential field was accepted")
	}
}

// Session overrides replace one array at a time, so shape validation must not
// apply the pairing rules that need a fully resolved agent.
func TestValidateCapabilityShapes_SkipsCrossFieldPairing(t *testing.T) {
	toolsOnly := []any{map[string]any{
		"type": "mcp_toolset", "mcp_server_name": "github",
	}}
	if err := ValidateCapabilityShapes(toolsOnly, nil, nil); err != nil {
		t.Fatalf("tools-only override was rejected: %v", err)
	}
	serversOnly := []any{map[string]any{
		"type": "url", "name": "github", "url": "https://mcp.example.com",
	}}
	if err := ValidateCapabilityShapes(nil, serversOnly, nil); err != nil {
		t.Fatalf("mcp_servers-only override was rejected: %v", err)
	}
	if err := ValidateToolConfiguration(toolsOnly, nil); err == nil {
		t.Fatal("resolved snapshot validation lost the MCP pairing rule")
	}
	leaky := []any{map[string]any{
		"type": "url", "name": "github", "url": "https://mcp.example.com",
		"authorization_token": "secret",
	}}
	if err := ValidateCapabilityShapes(nil, leaky, nil); err == nil {
		t.Fatal("override mcp_servers credential field was accepted")
	}
}
