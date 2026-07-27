package domain

import "fmt"

const BuiltinToolsetType = "agent_toolset_20260401"

var BuiltinToolNames = []string{"bash", "read", "write", "edit", "glob", "grep", "web_fetch", "web_search"}

type PermissionPolicy struct{ Type string }

type BuiltinConfig struct {
	Name    string
	Enabled *bool
	Policy  *PermissionPolicy
}

type BuiltinToolset struct {
	DefaultEnabled bool
	DefaultPolicy  PermissionPolicy
	Configs        []BuiltinConfig
}

type CustomTool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type MCPToolset struct{ ServerName string }

type ToolSet struct {
	Builtin *BuiltinToolset
	Custom  []CustomTool
	MCP     []MCPToolset
}

func parsePolicy(raw any) *PermissionPolicy {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	t, _ := m["type"].(string)
	if t == "" {
		return nil
	}
	return &PermissionPolicy{Type: t}
}

func ParseTools(raw []any) (ToolSet, error) {
	var ts ToolSet
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return ToolSet{}, fmt.Errorf("tool entry must be an object")
		}
		switch m["type"] {
		case BuiltinToolsetType:
			bt := &BuiltinToolset{DefaultEnabled: true, DefaultPolicy: PermissionPolicy{Type: "always_allow"}}
			if dc, ok := m["default_config"].(map[string]any); ok {
				if en, ok := dc["enabled"].(bool); ok {
					bt.DefaultEnabled = en
				}
				if p := parsePolicy(dc["permission_policy"]); p != nil {
					bt.DefaultPolicy = *p
				}
			}
			if cfgs, ok := m["configs"].([]any); ok {
				for _, c := range cfgs {
					cm, ok := c.(map[string]any)
					if !ok {
						return ToolSet{}, fmt.Errorf("toolset config must be an object")
					}
					name, _ := cm["name"].(string)
					if name == "" {
						return ToolSet{}, fmt.Errorf("toolset config requires name")
					}
					bc := BuiltinConfig{Name: name, Policy: parsePolicy(cm["permission_policy"])}
					if en, ok := cm["enabled"].(bool); ok {
						bc.Enabled = &en
					}
					bt.Configs = append(bt.Configs, bc)
				}
			}
			ts.Builtin = bt
		case "custom":
			name, _ := m["name"].(string)
			if name == "" {
				return ToolSet{}, fmt.Errorf("custom tool requires name")
			}
			ct := CustomTool{Name: name}
			ct.Description, _ = m["description"].(string)
			ct.InputSchema, _ = m["input_schema"].(map[string]any)
			ts.Custom = append(ts.Custom, ct)
		case "mcp_toolset":
			sn, _ := m["mcp_server_name"].(string)
			if sn == "" {
				return ToolSet{}, fmt.Errorf("mcp_toolset requires mcp_server_name")
			}
			ts.MCP = append(ts.MCP, MCPToolset{ServerName: sn})
		default:
			return ToolSet{}, fmt.Errorf("unknown tool type %v", m["type"])
		}
	}
	return ts, nil
}

func (ts ToolSet) BuiltinEnabled(name string) (bool, PermissionPolicy) {
	if ts.Builtin == nil {
		return false, PermissionPolicy{}
	}
	enabled := ts.Builtin.DefaultEnabled
	policy := ts.Builtin.DefaultPolicy
	for _, c := range ts.Builtin.Configs {
		if c.Name != name {
			continue
		}
		if c.Enabled != nil {
			enabled = *c.Enabled
		}
		if c.Policy != nil {
			policy = *c.Policy
		}
	}
	return enabled, policy
}
