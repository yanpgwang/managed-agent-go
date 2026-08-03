package domain

import "unicode/utf8"

// Public metadata limits shared by every resource that exposes a metadata bag.
const (
	MaxMetadataKeys       = 16
	MaxMetadataKeyRunes   = 64
	MaxMetadataValueRunes = 512
)

// ValidateMetadata enforces the public metadata contract on a resolved bag.
// Callers that accept a per-key patch must merge first and validate the result;
// a patch may legitimately carry nil values that mean "delete this key" and
// never reach the stored bag.
func ValidateMetadata(metadata map[string]any) error {
	if len(metadata) > MaxMetadataKeys {
		return Validation("metadata cannot contain more than 16 keys")
	}
	for key, raw := range metadata {
		if utf8.RuneCountInString(key) > MaxMetadataKeyRunes {
			return Validation("metadata keys cannot exceed 64 characters")
		}
		value, ok := raw.(string)
		if !ok {
			return Validation("metadata values must be strings")
		}
		if utf8.RuneCountInString(value) > MaxMetadataValueRunes {
			return Validation("metadata values cannot exceed 512 characters")
		}
	}
	return nil
}
