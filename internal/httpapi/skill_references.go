package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func parseOptionalNonNullSkillReferences(
	raw json.RawMessage,
	field string,
) (*[]domain.SkillReference, error) {
	return parseOptionalSkillReferences(raw, field, false)
}

// parseOptionalSkillReferenceReplacement preserves update/override tri-state:
// omission preserves the list, while null and [] both clear it.
func parseOptionalSkillReferenceReplacement(
	raw json.RawMessage,
	field string,
) (*[]domain.SkillReference, error) {
	return parseOptionalSkillReferences(raw, field, true)
}

func parseOptionalSkillReferences(
	raw json.RawMessage,
	field string,
	nullClears bool,
) (*[]domain.SkillReference, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		if !nullClears {
			return nil, domain.Validation(field + " cannot be null")
		}
		var cleared []domain.SkillReference
		return &cleared, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil || entries == nil {
		return nil, domain.Validation(field + " must be an array")
	}
	references := make([]domain.SkillReference, len(entries))
	for index, entry := range entries {
		var input struct {
			Type    string          `json:"type"`
			SkillID string          `json:"skill_id"`
			Version json.RawMessage `json:"version"`
		}
		decoder := json.NewDecoder(bytes.NewReader(entry))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return nil, domain.Validation(fmt.Sprintf(
				"%s[%d] must be a Skill reference object", field, index,
			))
		}
		var version string
		if len(input.Version) > 0 {
			if bytes.Equal(bytes.TrimSpace(input.Version), []byte("null")) ||
				json.Unmarshal(input.Version, &version) != nil || version == "" {
				return nil, domain.Validation(fmt.Sprintf(
					"%s[%d].version must be a string", field, index,
				))
			}
		}
		references[index] = domain.SkillReference{
			Type: input.Type, SkillID: input.SkillID, Version: version,
		}
	}
	return &references, nil
}
