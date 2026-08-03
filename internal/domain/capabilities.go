package domain

import "fmt"

// Mango keeps skills and multiagent as opaque JSON so an Agent version can
// round-trip the documented wire without the runtime executing them yet.
// "Opaque" must not mean "unchecked": every stored key is echoed by
// GET /v1/agents/{id} and the version list, so undocumented nested keys are
// refused at admission the same way the tool payloads are.
// Shapes come from
// docs/_upstream/claude-managed-agents/snapshot/api-reference/agents/
// {create,update}.md and guides/skills.md.
var (
	skillFields       = []string{"skill_id", "type", "version"}
	multiagentFields  = []string{"agents", "type"}
	rosterAgentFields = []string{"id", "type", "version"}
	rosterSelfFields  = []string{"type"}
)

// multiagentRosterLimit is the documented coordinator roster bound (1-20).
const multiagentRosterLimit = 20

// ValidateSkills checks the documented skill reference shape. Whether a skill
// exists or can execute is a separate concern; this only keeps unsupported
// fields out of stored agent state.
func ValidateSkills(raw []any) error {
	for _, item := range raw {
		value, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("skill entry must be an object")
		}
		if err := rejectUnknownFields(value, "skill", skillFields); err != nil {
			return err
		}
		skillType, _ := value["type"].(string)
		if skillType != "anthropic" && skillType != "custom" {
			return fmt.Errorf("skill type must be anthropic or custom")
		}
		skillID, _ := value["skill_id"].(string)
		if skillID == "" {
			return fmt.Errorf("skill requires skill_id")
		}
		if version, present := value["version"]; present && version != nil {
			if _, ok := version.(string); !ok {
				return fmt.Errorf("skill %q version must be a string", skillID)
			}
		}
	}
	return nil
}

// ValidateMultiagent checks the documented coordinator topology shape. Roster
// resolution (existence, archival, and the depth-1 rule) needs cross-resource
// reads and is not implemented; this is admission-time shape validation only.
func ValidateMultiagent(raw map[string]any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := rejectUnknownFields(raw, "multiagent", multiagentFields); err != nil {
		return err
	}
	if topology, _ := raw["type"].(string); topology != "coordinator" {
		return fmt.Errorf("multiagent type must be coordinator")
	}
	roster, ok := raw["agents"].([]any)
	if !ok {
		return fmt.Errorf("multiagent requires an agents array")
	}
	if len(roster) == 0 || len(roster) > multiagentRosterLimit {
		return fmt.Errorf(
			"multiagent agents must contain 1 to %d entries",
			multiagentRosterLimit,
		)
	}
	seen := make(map[string]struct{}, len(roster))
	selfEntries := 0
	for _, item := range roster {
		if id, ok := item.(string); ok {
			if id == "" {
				return fmt.Errorf("multiagent roster entry requires an agent id")
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("duplicate multiagent roster entry %q", id)
			}
			seen[id] = struct{}{}
			continue
		}
		entry, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf(
				"multiagent roster entry must be an agent id or reference object",
			)
		}
		switch entryType, _ := entry["type"].(string); entryType {
		case "self":
			if err := rejectUnknownFields(
				entry,
				"multiagent self entry",
				rosterSelfFields,
			); err != nil {
				return err
			}
			selfEntries++
			if selfEntries > 1 {
				return fmt.Errorf("multiagent roster allows at most one self entry")
			}
		case "agent":
			if err := rejectUnknownFields(
				entry,
				"multiagent agent entry",
				rosterAgentFields,
			); err != nil {
				return err
			}
			id, _ := entry["id"].(string)
			if id == "" {
				return fmt.Errorf("multiagent roster entry requires an agent id")
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("duplicate multiagent roster entry %q", id)
			}
			seen[id] = struct{}{}
			if version, present := entry["version"]; present && version != nil {
				pinned, ok := version.(float64)
				if !ok || pinned != float64(int(pinned)) || pinned < 1 {
					return fmt.Errorf(
						"multiagent roster entry %q version must be an integer of at least 1",
						id,
					)
				}
			}
		default:
			return fmt.Errorf("multiagent roster entry type must be agent or self")
		}
	}
	return nil
}

// ValidateCapabilityShapes checks each capability payload on its own, without
// the cross-field rules that need a fully resolved agent. Session agent
// overrides replace individual arrays -- `tools` may be overridden while
// `mcp_servers` is inherited -- so only shape can be judged at that boundary;
// the resolved session snapshot is validated in full afterwards.
func ValidateCapabilityShapes(tools, mcpServers, skills []any) error {
	if _, err := ParseTools(tools); err != nil {
		return err
	}
	if _, err := ParseMCPServers(mcpServers); err != nil {
		return err
	}
	return ValidateSkills(skills)
}

// ValidateAgentCapabilities validates every nested capability payload the
// public agent wire accepts as opaque JSON. It is the single admission-time
// gate shared by agent create, agent update, and session agent overrides, so
// nothing undocumented reaches an immutable agent version or session snapshot.
func ValidateAgentCapabilities(
	tools, mcpServers, skills []any,
	multiagent map[string]any,
) error {
	if err := ValidateToolConfiguration(tools, mcpServers); err != nil {
		return err
	}
	if err := ValidateSkills(skills); err != nil {
		return err
	}
	return ValidateMultiagent(multiagent)
}
