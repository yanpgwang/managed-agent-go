package app

import (
	"github.com/yanpgwang/mango/internal/domain"
)

// The public metadata contract lives in the domain so resource state machines
// that run inside a storage transaction can validate the bag they produce.
func validateMetadata(metadata map[string]any) error {
	return domain.ValidateMetadata(metadata)
}

// ValidateMetadata exposes the shared public-resource metadata contract to
// alternate control-plane service wiring.
func ValidateMetadata(metadata map[string]any) error {
	return domain.ValidateMetadata(metadata)
}
