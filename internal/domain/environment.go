package domain

import (
	"fmt"
	"reflect"
	"time"
)

type Environment struct {
	ID          string
	Name        string
	Description string
	ConfigType  string // "cloud" | "self_hosted"
	Config      map[string]any
	Metadata    map[string]any
	Scope       string // "" | "organization" | "account"
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

// EnvironmentPatch is the internal form of an Update Environment request. Every
// field is tri-state by absence: a nil pointer or nil map preserves the stored
// value, matching the documented "omitted fields preserve the existing value"
// rule for this resource.
type EnvironmentPatch struct {
	Name        *string
	Description *string
	Scope       *string
	// Metadata is a patch, not a replacement. A key mapped to nil or to the
	// empty string deletes that key; any other string upserts it.
	//
	// This deletion rule is deliberately NOT shared with Session metadata,
	// which deletes only on null. The two resources document different
	// semantics and must not converge on a common helper.
	Metadata map[string]any
	// Config is the raw request config object. Its `type` is required when the
	// field is present; unset fields inside it preserve the stored value.
	Config map[string]any
}

// ValidateEnvironmentScope enforces the documented scope enumeration.
func ValidateEnvironmentScope(scope string) error {
	switch scope {
	case "organization", "account":
		return nil
	}
	return Validation("scope must be organization or account")
}

// Apply folds a patch onto the environment and reports whether anything
// changed. It does not assign timestamps; the service owns the clock.
func (e Environment) Apply(patch EnvironmentPatch) (Environment, bool, error) {
	next := e
	changed := false

	if patch.Name != nil && *patch.Name != next.Name {
		next.Name = *patch.Name
		changed = true
	}
	if patch.Description != nil && *patch.Description != next.Description {
		next.Description = *patch.Description
		changed = true
	}
	if patch.Scope != nil {
		if err := ValidateEnvironmentScope(*patch.Scope); err != nil {
			return Environment{}, false, err
		}
		if *patch.Scope != next.Scope {
			next.Scope = *patch.Scope
			changed = true
		}
	}
	if patch.Metadata != nil {
		merged, metadataChanged, err := applyEnvironmentMetadataPatch(next.Metadata, patch.Metadata)
		if err != nil {
			return Environment{}, false, err
		}
		next.Metadata = merged
		changed = changed || metadataChanged
	}
	if patch.Config != nil {
		config, configType, err := mergeEnvironmentConfig(next.ConfigType, next.Config, patch.Config)
		if err != nil {
			return Environment{}, false, err
		}
		if !reflect.DeepEqual(config, next.Config) || configType != next.ConfigType {
			next.Config = config
			next.ConfigType = configType
			changed = true
		}
	}
	return next, changed, nil
}

// applyEnvironmentMetadataPatch implements the Environment rule: a key set to
// null OR to an empty string is deleted. Session metadata deletes only on null,
// so the two must stay separate implementations.
func applyEnvironmentMetadataPatch(
	current map[string]any,
	patch map[string]any,
) (map[string]any, bool, error) {
	merged := make(map[string]any, len(current)+len(patch))
	for key, value := range current {
		merged[key] = value
	}
	changed := false
	for key, raw := range patch {
		if raw == nil {
			if _, ok := merged[key]; ok {
				delete(merged, key)
				changed = true
			}
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return nil, false, Validation("metadata values must be strings or null")
		}
		if value == "" {
			if _, ok := merged[key]; ok {
				delete(merged, key)
				changed = true
			}
			continue
		}
		if existing, ok := merged[key]; !ok || existing != any(value) {
			merged[key] = value
			changed = true
		}
	}
	return merged, changed, nil
}

// mergeEnvironmentConfig applies the documented config union update rules:
// `type` is required inside the object, omitted `networking` preserves the
// stored policy, and omitted limited-network fields preserve their stored
// values. `packages` carries no preserve note upstream, so a supplied
// `packages` object replaces the stored one wholesale.
func mergeEnvironmentConfig(
	currentType string,
	current map[string]any,
	incoming map[string]any,
) (map[string]any, string, error) {
	rawType, present := incoming["type"]
	if !present {
		return nil, "", Validation("config type is required")
	}
	configType, ok := rawType.(string)
	if !ok || (configType != "cloud" && configType != "self_hosted") {
		return nil, "", Validation("config type must be cloud or self_hosted")
	}
	// Local choice: the stored config type selects the sandbox execution model
	// for every future Session, and upstream does not document switching it in
	// place. Reject the change explicitly rather than silently reinterpreting
	// existing behavior.
	if currentType != "" && configType != currentType {
		return nil, "", Validation("config type cannot be changed after creation")
	}

	if configType == "self_hosted" {
		if err := rejectUnknownConfigKeys(incoming, "type"); err != nil {
			return nil, "", err
		}
		return map[string]any{"type": "self_hosted"}, configType, nil
	}

	if err := rejectUnknownConfigKeys(incoming, "type", "networking", "packages"); err != nil {
		return nil, "", err
	}
	merged := map[string]any{"type": "cloud"}
	for key, value := range current {
		if key == "type" {
			continue
		}
		merged[key] = value
	}

	if raw, present := incoming["networking"]; present {
		currentNetworking, _ := merged["networking"].(map[string]any)
		networking, err := mergeCloudNetworking(currentNetworking, raw)
		if err != nil {
			return nil, "", err
		}
		merged["networking"] = networking
	}
	if raw, present := incoming["packages"]; present {
		packages, err := normalizeCloudPackages(raw)
		if err != nil {
			return nil, "", err
		}
		merged["packages"] = packages
	}
	return merged, configType, nil
}

func mergeCloudNetworking(current map[string]any, raw any) (map[string]any, error) {
	incoming, ok := raw.(map[string]any)
	if !ok {
		return nil, Validation("config networking must be an object")
	}
	policy, _ := incoming["type"].(string)
	switch policy {
	case "unrestricted":
		if err := rejectUnknownConfigKeys(incoming, "type"); err != nil {
			return nil, err
		}
		return map[string]any{"type": "unrestricted"}, nil
	case "limited":
		if err := rejectUnknownConfigKeys(
			incoming, "type", "allow_mcp_servers", "allow_package_managers", "allowed_hosts",
		); err != nil {
			return nil, err
		}
		// Preserve stored limited-network fields only when the stored policy was
		// already `limited`; switching from `unrestricted` starts from the
		// documented defaults instead.
		base := map[string]any{}
		if currentPolicy, _ := current["type"].(string); currentPolicy == "limited" {
			base = current
		}
		out := map[string]any{"type": "limited"}
		for _, field := range []string{"allow_mcp_servers", "allow_package_managers"} {
			value, err := resolveNetworkBool(incoming, base, field)
			if err != nil {
				return nil, err
			}
			out[field] = value
		}
		hosts, err := resolveAllowedHosts(incoming, base)
		if err != nil {
			return nil, err
		}
		out["allowed_hosts"] = hosts
		return out, nil
	}
	return nil, Validation("config networking type must be unrestricted or limited")
}

func resolveNetworkBool(incoming, base map[string]any, field string) (bool, error) {
	if raw, present := incoming[field]; present && raw != nil {
		value, ok := raw.(bool)
		if !ok {
			return false, Validation("config networking " + field + " must be a boolean")
		}
		return value, nil
	}
	value, _ := base[field].(bool)
	return value, nil
}

func resolveAllowedHosts(incoming, base map[string]any) ([]any, error) {
	raw, present := incoming["allowed_hosts"]
	if !present || raw == nil {
		hosts, _ := base["allowed_hosts"].([]any)
		if hosts == nil {
			return []any{}, nil
		}
		return hosts, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, Validation("config networking allowed_hosts must be an array of strings")
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return nil, Validation("config networking allowed_hosts must be an array of strings")
		}
	}
	return items, nil
}

var cloudPackageManagers = []string{"apt", "cargo", "gem", "go", "npm", "pip"}

func normalizeCloudPackages(raw any) (map[string]any, error) {
	incoming, ok := raw.(map[string]any)
	if !ok {
		return nil, Validation("config packages must be an object")
	}
	if err := rejectUnknownConfigKeys(incoming, append([]string{"type"}, cloudPackageManagers...)...); err != nil {
		return nil, err
	}
	if rawType, present := incoming["type"]; present && rawType != nil {
		value, ok := rawType.(string)
		if !ok || value != "packages" {
			return nil, Validation("config packages type must be packages")
		}
	}
	out := map[string]any{"type": "packages"}
	for _, manager := range cloudPackageManagers {
		raw, present := incoming[manager]
		if !present || raw == nil {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, Validation("config packages " + manager + " must be an array of strings")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return nil, Validation("config packages " + manager + " must be an array of strings")
			}
		}
		out[manager] = items
	}
	return out, nil
}

func rejectUnknownConfigKeys(object map[string]any, allowed ...string) error {
	permitted := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		permitted[key] = struct{}{}
	}
	for key := range object {
		if _, ok := permitted[key]; !ok {
			return Validation(fmt.Sprintf("unknown config field %q", key))
		}
	}
	return nil
}
