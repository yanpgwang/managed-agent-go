package app

import (
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func validateMetadata(metadata map[string]any) error {
	if len(metadata) > 16 {
		return domain.Validation("metadata cannot contain more than 16 keys")
	}
	for key, raw := range metadata {
		if utf8.RuneCountInString(key) > 64 {
			return domain.Validation("metadata keys cannot exceed 64 characters")
		}
		value, ok := raw.(string)
		if !ok {
			return domain.Validation("metadata values must be strings")
		}
		if utf8.RuneCountInString(value) > 512 {
			return domain.Validation("metadata values cannot exceed 512 characters")
		}
	}
	return nil
}
