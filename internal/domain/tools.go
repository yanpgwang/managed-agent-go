package domain

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

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

type MCPToolset struct {
	ServerName     string
	DefaultEnabled bool
	DefaultPolicy  PermissionPolicy
	Configs        []BuiltinConfig
}

type MCPServer struct {
	Name string
	URL  string
}

type ToolSet struct {
	Builtin *BuiltinToolset
	Custom  []CustomTool
	MCP     []MCPToolset
}

var blockedMCPAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // shared carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
}

// MCPAddressAllowed reports whether a resolved MCP endpoint is safe for the
// default public-internet connector. Private MCP services require a separate
// tunnel/egress capability; the worker must never reach them directly from a
// tenant-controlled URL.
func MCPAddressAllowed(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() ||
		!address.IsGlobalUnicast() ||
		address.IsPrivate() ||
		address.IsLoopback() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedMCPAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validPermissionPolicy(policy PermissionPolicy) bool {
	return policy.Type == "always_allow" || policy.Type == "always_ask"
}

// Documented field sets for the nested capability objects the public wire
// accepts. Mango stores tools, mcp_servers, and skills as opaque JSON and
// echoes them verbatim from GET /v1/agents/{id} and the version list, so an
// undocumented nested key would be persisted in an immutable agent version and
// replayed to every reader. Anything secret-shaped a client sends by mistake --
// an upstream Messages-style `authorization_token` on an MCP server, for
// example -- must therefore be refused at admission rather than absorbed.
// See docs/_upstream/claude-managed-agents/snapshot/api-reference/agents/
// {create,update}.md and guides/{tools,mcp-connector,permission-policies}.md.
var (
	builtinToolsetFields = []string{"configs", "default_config", "type"}
	mcpToolsetFields     = []string{"configs", "default_config", "mcp_server_name", "type"}
	customToolFields     = []string{"description", "input_schema", "name", "type"}
	toolConfigFields     = []string{"enabled", "name", "permission_policy"}
	defaultConfigFields  = []string{"enabled", "permission_policy"}
	mcpServerFields      = []string{"name", "type", "url"}
	policyFields         = []string{"type"}
)

// rejectUnknownFields fails on the first (lexicographically lowest, for a
// deterministic message) key that the documented object shape does not define.
func rejectUnknownFields(value map[string]any, context string, allowed []string) error {
	var unknown []string
	for key := range value {
		if !slices.Contains(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return fmt.Errorf("%s does not support field %q", context, unknown[0])
}

// optionalObject reads a documented optional object field. An absent field and
// an explicit null both mean "not configured"; any other non-object is a client
// error rather than something to silently ignore.
func optionalObject(
	value map[string]any,
	key, context string,
) (map[string]any, bool, error) {
	raw, present := value[key]
	if !present || raw == nil {
		return nil, false, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%s %s must be an object", context, key)
	}
	return object, true, nil
}

// optionalBool reads a documented optional boolean field with the same
// absent/null handling as optionalObject.
func optionalBool(value map[string]any, key, context string) (*bool, error) {
	raw, present := value[key]
	if !present || raw == nil {
		return nil, nil
	}
	flag, ok := raw.(bool)
	if !ok {
		return nil, fmt.Errorf("%s %s must be a boolean", context, key)
	}
	return &flag, nil
}

func parsePolicy(raw any, context string) (PermissionPolicy, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return PermissionPolicy{}, fmt.Errorf(
			"%s permission_policy must be an object",
			context,
		)
	}
	if err := rejectUnknownFields(
		m,
		context+" permission_policy",
		policyFields,
	); err != nil {
		return PermissionPolicy{}, err
	}
	policy := PermissionPolicy{}
	policy.Type, _ = m["type"].(string)
	if !validPermissionPolicy(policy) {
		return PermissionPolicy{}, fmt.Errorf(
			"%s permission_policy must be always_allow or always_ask",
			context,
		)
	}
	return policy, nil
}

// parseDefaultConfig applies a documented `default_config` object onto the
// caller's resolved defaults, leaving them untouched when the field is absent.
func parseDefaultConfig(
	tool map[string]any,
	context string,
	enabled *bool,
	policy *PermissionPolicy,
) error {
	config, present, err := optionalObject(tool, "default_config", context)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if err := rejectUnknownFields(
		config,
		context+" default_config",
		defaultConfigFields,
	); err != nil {
		return err
	}
	flag, err := optionalBool(config, "enabled", context+" default_config")
	if err != nil {
		return err
	}
	if flag != nil {
		*enabled = *flag
	}
	if raw, present := config["permission_policy"]; present && raw != nil {
		parsed, err := parsePolicy(raw, context+" default")
		if err != nil {
			return err
		}
		*policy = parsed
	}
	return nil
}

// parseToolConfigs applies the documented `configs` array shared by the
// built-in and MCP toolset variants. checkName enforces the variant's naming
// rule (built-in enum membership versus a free-form MCP tool name).
func parseToolConfigs(
	tool map[string]any,
	context string,
	checkName func(string) error,
) ([]BuiltinConfig, error) {
	raw, present := tool["configs"]
	if !present || raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s configs must be an array", context)
	}
	configured := make(map[string]struct{}, len(entries))
	parsed := make([]BuiltinConfig, 0, len(entries))
	for _, entry := range entries {
		value, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s config must be an object", context)
		}
		if err := rejectUnknownFields(
			value,
			context+" config",
			toolConfigFields,
		); err != nil {
			return nil, err
		}
		name, _ := value["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("%s config requires name", context)
		}
		if err := checkName(name); err != nil {
			return nil, err
		}
		if _, duplicate := configured[name]; duplicate {
			return nil, fmt.Errorf("duplicate %s config %q", context, name)
		}
		configured[name] = struct{}{}
		config := BuiltinConfig{Name: name}
		if raw, present := value["permission_policy"]; present && raw != nil {
			policy, err := parsePolicy(
				raw,
				fmt.Sprintf("%s %q", context, name),
			)
			if err != nil {
				return nil, err
			}
			config.Policy = &policy
		}
		enabled, err := optionalBool(
			value,
			"enabled",
			fmt.Sprintf("%s config %q", context, name),
		)
		if err != nil {
			return nil, err
		}
		config.Enabled = enabled
		parsed = append(parsed, config)
	}
	return parsed, nil
}

func ParseTools(raw []any) (ToolSet, error) {
	var ts ToolSet
	customNames := make(map[string]struct{})
	mcpServers := make(map[string]struct{})
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return ToolSet{}, fmt.Errorf("tool entry must be an object")
		}
		switch m["type"] {
		case BuiltinToolsetType:
			if ts.Builtin != nil {
				return ToolSet{}, fmt.Errorf("only one built-in toolset may be configured")
			}
			if err := rejectUnknownFields(
				m,
				BuiltinToolsetType,
				builtinToolsetFields,
			); err != nil {
				return ToolSet{}, err
			}
			bt := &BuiltinToolset{DefaultEnabled: true, DefaultPolicy: PermissionPolicy{Type: "always_allow"}}
			if err := parseDefaultConfig(
				m,
				"built-in toolset",
				&bt.DefaultEnabled,
				&bt.DefaultPolicy,
			); err != nil {
				return ToolSet{}, err
			}
			configs, err := parseToolConfigs(
				m,
				"built-in tool",
				func(name string) error {
					if !containsBuiltinTool(name) {
						return fmt.Errorf("unknown built-in tool %q", name)
					}
					return nil
				},
			)
			if err != nil {
				return ToolSet{}, err
			}
			bt.Configs = configs
			ts.Builtin = bt
		case "custom":
			if err := rejectUnknownFields(
				m,
				"custom tool",
				customToolFields,
			); err != nil {
				return ToolSet{}, err
			}
			name, _ := m["name"].(string)
			if name == "" {
				return ToolSet{}, fmt.Errorf("custom tool requires name")
			}
			if _, duplicate := customNames[name]; duplicate {
				return ToolSet{}, fmt.Errorf("duplicate custom tool %q", name)
			}
			if containsBuiltinTool(name) {
				return ToolSet{}, fmt.Errorf(
					"custom tool %q conflicts with a built-in tool",
					name,
				)
			}
			customNames[name] = struct{}{}
			ct := CustomTool{Name: name}
			if raw, present := m["description"]; present && raw != nil {
				description, ok := raw.(string)
				if !ok {
					return ToolSet{}, fmt.Errorf(
						"custom tool %q description must be a string",
						name,
					)
				}
				ct.Description = description
			}
			schema, present, err := optionalObject(
				m,
				"input_schema",
				fmt.Sprintf("custom tool %q", name),
			)
			if err != nil {
				return ToolSet{}, err
			}
			// input_schema is a caller-authored JSON Schema. Only the documented
			// wrapper is checked; its properties stay opaque by design.
			if present {
				if schemaType, _ := schema["type"].(string); schemaType != "object" {
					return ToolSet{}, fmt.Errorf(
						"custom tool %q input_schema type must be object",
						name,
					)
				}
				ct.InputSchema = schema
			}
			ts.Custom = append(ts.Custom, ct)
		case "mcp_toolset":
			if err := rejectUnknownFields(
				m,
				"mcp_toolset",
				mcpToolsetFields,
			); err != nil {
				return ToolSet{}, err
			}
			sn, _ := m["mcp_server_name"].(string)
			if sn == "" {
				return ToolSet{}, fmt.Errorf("mcp_toolset requires mcp_server_name")
			}
			if _, duplicate := mcpServers[sn]; duplicate {
				return ToolSet{}, fmt.Errorf("duplicate mcp_toolset for server %q", sn)
			}
			mcpServers[sn] = struct{}{}
			mt := MCPToolset{
				ServerName:     sn,
				DefaultEnabled: true,
				DefaultPolicy: PermissionPolicy{
					Type: "always_ask",
				},
			}
			if err := parseDefaultConfig(
				m,
				fmt.Sprintf("mcp server %q", sn),
				&mt.DefaultEnabled,
				&mt.DefaultPolicy,
			); err != nil {
				return ToolSet{}, err
			}
			configs, err := parseToolConfigs(
				m,
				fmt.Sprintf("mcp tool for server %q", sn),
				func(string) error { return nil },
			)
			if err != nil {
				return ToolSet{}, err
			}
			mt.Configs = configs
			ts.MCP = append(ts.MCP, mt)
		default:
			return ToolSet{}, fmt.Errorf("unknown tool type %v", m["type"])
		}
	}
	return ts, nil
}

func containsBuiltinTool(name string) bool {
	for _, candidate := range BuiltinToolNames {
		if candidate == name {
			return true
		}
	}
	return false
}

func (ts MCPToolset) ToolEnabled(name string) (bool, PermissionPolicy) {
	enabled := ts.DefaultEnabled
	policy := ts.DefaultPolicy
	for _, config := range ts.Configs {
		if config.Name != name {
			continue
		}
		if config.Enabled != nil {
			enabled = *config.Enabled
		}
		if config.Policy != nil {
			policy = *config.Policy
		}
	}
	return enabled, policy
}

func ParseMCPServers(raw []any) (map[string]MCPServer, error) {
	if len(raw) > 20 {
		return nil, fmt.Errorf("an agent may configure at most 20 MCP servers")
	}
	servers := make(map[string]MCPServer, len(raw))
	for _, item := range raw {
		value, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mcp server entry must be an object")
		}
		// The MCP connector guide is explicit that no authentication material is
		// supplied at configuration time, so an upstream-shaped credential field
		// here is refused instead of being stored and echoed back in plaintext.
		if err := rejectUnknownFields(value, "mcp server", mcpServerFields); err != nil {
			return nil, err
		}
		serverType, _ := value["type"].(string)
		name, _ := value["name"].(string)
		rawURL, _ := value["url"].(string)
		if serverType != "url" ||
			strings.TrimSpace(name) == "" ||
			strings.TrimSpace(rawURL) == "" {
			return nil, fmt.Errorf("mcp server requires type=url, name, and url")
		}
		if utf8.RuneCountInString(name) > 255 {
			return nil, fmt.Errorf("mcp server name exceeds 255 characters")
		}
		if utf8.RuneCountInString(rawURL) > 2048 {
			return nil, fmt.Errorf("mcp server %q url exceeds 2048 characters", name)
		}
		if _, duplicate := servers[name]; duplicate {
			return nil, fmt.Errorf("duplicate mcp server name %q", name)
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("mcp server %q has an invalid http(s) url", name)
		}
		if address, err := netip.ParseAddr(parsed.Hostname()); err == nil &&
			!MCPAddressAllowed(address) {
			return nil, fmt.Errorf(
				"mcp server %q url targets a non-public network",
				name,
			)
		}
		servers[name] = MCPServer{Name: name, URL: parsed.String()}
	}
	return servers, nil
}

// ValidateToolConfiguration performs control-plane validation that does not
// require contacting a provider or MCP server. It keeps malformed or
// internally inconsistent capabilities out of immutable Agent versions and
// Session snapshots.
func ValidateToolConfiguration(rawTools, rawServers []any) error {
	toolSet, err := ParseTools(rawTools)
	if err != nil {
		return err
	}
	servers, err := ParseMCPServers(rawServers)
	if err != nil {
		return err
	}
	referenced := make(map[string]struct{}, len(toolSet.MCP))
	for _, configured := range toolSet.MCP {
		if _, ok := servers[configured.ServerName]; !ok {
			return fmt.Errorf(
				"mcp_toolset references unknown server %q",
				configured.ServerName,
			)
		}
		referenced[configured.ServerName] = struct{}{}
	}
	for name := range servers {
		if _, ok := referenced[name]; !ok {
			return fmt.Errorf("MCP server %q has no matching mcp_toolset", name)
		}
	}
	for _, name := range []string{"web_search", "web_fetch"} {
		enabled, policy := toolSet.BuiltinEnabled(name)
		if enabled && policy.Type != "always_allow" {
			return fmt.Errorf(
				"%s requires always_allow while it is provider-native",
				name,
			)
		}
	}
	return nil
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
