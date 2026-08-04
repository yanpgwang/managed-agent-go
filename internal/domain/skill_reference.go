package domain

import "encoding/json"

// SkillReference is the resolved Skill configuration stored on an Agent or
// Session snapshot. Requests may omit Version or send "latest"; application
// services replace either form with the concrete immutable Version before the
// resource crosses a persistence boundary.
type SkillReference struct {
	Type    string `json:"type"`
	SkillID string `json:"skill_id"`
	Version string `json:"version,omitempty"`

	// legacyJSON preserves Skill values written before references became a
	// strict tagged union. New requests can never populate it; it exists so an
	// upgrade does not make old Agent or Session projections unreadable or drop
	// unknown fields when an unrelated Agent field is updated.
	legacyJSON json.RawMessage
}

// IsLegacy reports whether this value came from the former opaque Skills
// representation. Legacy values remain readable but cannot be attached to a
// newly created Session until the Agent's Skills list is replaced.
func (r SkillReference) IsLegacy() bool { return len(r.legacyJSON) > 0 }

func (r SkillReference) MarshalJSON() ([]byte, error) {
	if r.IsLegacy() {
		return append([]byte(nil), r.legacyJSON...), nil
	}
	type wireReference struct {
		Type    string `json:"type"`
		SkillID string `json:"skill_id"`
		Version string `json:"version,omitempty"`
	}
	return json.Marshal(wireReference{
		Type: r.Type, SkillID: r.SkillID, Version: r.Version,
	})
}

func (r *SkillReference) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		r.setLegacy(data)
		return nil
	}
	for key := range fields {
		switch key {
		case "type", "skill_id", "version":
		default:
			r.setLegacy(data)
			return nil
		}
	}
	var decoded struct {
		Type    string `json:"type"`
		SkillID string `json:"skill_id"`
		Version string `json:"version,omitempty"`
	}
	if _, ok := fields["type"]; !ok {
		r.setLegacy(data)
		return nil
	}
	if _, ok := fields["skill_id"]; !ok {
		r.setLegacy(data)
		return nil
	}
	if raw, ok := fields["version"]; ok {
		var version string
		if err := json.Unmarshal(raw, &version); err != nil {
			r.setLegacy(data)
			return nil
		}
	}
	if err := json.Unmarshal(data, &decoded); err != nil ||
		(decoded.Type != "custom" && decoded.Type != "anthropic") {
		r.setLegacy(data)
		return nil
	}
	*r = SkillReference{
		Type: decoded.Type, SkillID: decoded.SkillID, Version: decoded.Version,
	}
	return nil
}

func (r *SkillReference) setLegacy(data []byte) {
	*r = SkillReference{legacyJSON: append(json.RawMessage(nil), data...)}
}
