package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type EnvironmentService struct {
	env   EnvironmentRepository
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewEnvironmentService(env EnvironmentRepository, ids domain.IDGenerator, clock domain.Clock) *EnvironmentService {
	return &EnvironmentService{env: env, ids: ids, clock: clock}
}

func (s *EnvironmentService) Create(ctx context.Context, e domain.Environment) (domain.Environment, error) {
	if e.ConfigType == "" {
		if configType, ok := e.Config["type"].(string); ok {
			e.ConfigType = configType
		} else {
			e.ConfigType = "cloud"
		}
	}
	if err := validateEnvironment(e); err != nil {
		return domain.Environment{}, err
	}
	// Only persist the capability the runtime can currently honor. Explicit
	// networking and package policies are rejected above rather than stored as
	// inert configuration.
	e.Config = map[string]any{"type": e.ConfigType}
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	now := s.clock.Now().UTC()
	e.ID = s.ids.NewID(domain.PrefixEnv)
	e.CreatedAt = now
	e.UpdatedAt = now
	return e, s.env.Put(ctx, e)
}

func (s *EnvironmentService) Update(
	ctx context.Context,
	id string,
	patch domain.EnvironmentPatch,
) (domain.Environment, error) {
	current, err := s.env.Get(ctx, id)
	if err != nil {
		return domain.Environment{}, err
	}
	if current.ArchivedAt != nil {
		return domain.Environment{}, domain.Validation("archived environment is read-only")
	}
	if patch.Scope != nil && *patch.Scope == "" {
		return domain.Environment{}, domain.Validation("scope must be organization or account")
	}

	if patch.Config != nil {
		rawConfig := *patch.Config
		configType, ok := rawConfig["type"].(string)
		if !ok {
			return domain.Environment{}, domain.Validation("config type must be cloud or self_hosted")
		}
		if value, present := rawConfig["networking"]; present && value == nil {
			return domain.Environment{}, domain.Validation("environment networking configuration cannot be null")
		}
		if value, present := rawConfig["packages"]; present && value == nil {
			return domain.Environment{}, domain.Validation("environment package configuration cannot be null")
		}
		if err := validateEnvironmentConfig(rawConfig, configType); err != nil {
			return domain.Environment{}, err
		}
		normalized := map[string]any{"type": configType}
		patch.Config = &normalized
		if configType == "cloud" && patch.Scope == nil {
			clearedScope := ""
			patch.Scope = &clearedScope
		}
	}

	next, changed := current.Apply(patch)
	if patch.Config != nil {
		next.ConfigType = (*patch.Config)["type"].(string)
	}
	if err := validateEnvironment(next); err != nil {
		return domain.Environment{}, err
	}
	if !changed {
		return current, nil
	}
	next.UpdatedAt = s.clock.Now().UTC()
	return s.env.Update(ctx, next)
}

func validateEnvironment(environment domain.Environment) error {
	if environment.Name == "" {
		return domain.Validation("name is required")
	}
	if environment.ConfigType != "cloud" && environment.ConfigType != "self_hosted" {
		return domain.Validation("config type must be cloud or self_hosted")
	}
	if environment.Scope != "" && environment.Scope != "organization" && environment.Scope != "account" {
		return domain.Validation("scope must be organization or account")
	}
	if environment.ConfigType == "cloud" && environment.Scope != "" {
		return domain.Validation("scope is only supported for self_hosted environments")
	}
	if err := validateMetadata(environment.Metadata); err != nil {
		return err
	}
	return validateEnvironmentConfig(environment.Config, environment.ConfigType)
}

func validateEnvironmentConfig(config map[string]any, configType string) error {
	if value, present := config["type"]; present {
		valueType, ok := value.(string)
		if !ok || valueType != configType {
			return domain.Validation("config type must be cloud or self_hosted")
		}
	}
	allowed := map[string]struct{}{"type": {}}
	if configType == "cloud" {
		allowed["networking"] = struct{}{}
		allowed["packages"] = struct{}{}
	}
	if err := rejectUnknownEnvironmentFields(config, allowed, "config"); err != nil {
		return err
	}
	if value, present := config["networking"]; present {
		if err := validateEnvironmentNetworking(value); err != nil {
			return err
		}
	}
	if value, present := config["packages"]; present {
		if err := validateEnvironmentPackages(value); err != nil {
			return err
		}
	}
	return nil
}

func validateEnvironmentNetworking(value any) error {
	networking, ok := value.(map[string]any)
	if !ok || networking == nil {
		return domain.Validation("environment networking configuration must be an object")
	}
	typeValue, ok := networking["type"].(string)
	if !ok {
		return domain.Validation("environment networking type must be unrestricted or limited")
	}
	if typeValue == "limited" {
		allowed := map[string]struct{}{
			"type": {}, "allow_mcp_servers": {}, "allow_package_managers": {}, "allowed_hosts": {},
		}
		if err := rejectUnknownEnvironmentFields(networking, allowed, "networking"); err != nil {
			return err
		}
		for _, field := range []string{"allow_mcp_servers", "allow_package_managers"} {
			if fieldValue, present := networking[field]; present {
				if _, ok := fieldValue.(bool); !ok {
					return domain.Validation(
						fmt.Sprintf("environment networking.%s must be a boolean", field),
					)
				}
			}
		}
		if hosts, present := networking["allowed_hosts"]; present {
			if err := validateEnvironmentStringList(hosts, "networking.allowed_hosts"); err != nil {
				return err
			}
		}
		return domain.Unsupported("limited environment networking is not implemented")
	}
	if typeValue != "unrestricted" {
		return domain.Validation("environment networking type must be unrestricted or limited")
	}
	return rejectUnknownEnvironmentFields(
		networking,
		map[string]struct{}{"type": {}},
		"networking",
	)
}

func validateEnvironmentPackages(value any) error {
	packages, ok := value.(map[string]any)
	if !ok || packages == nil {
		return domain.Validation("environment package configuration must be an object")
	}
	allowed := map[string]struct{}{
		"type": {}, "apt": {}, "cargo": {}, "gem": {}, "go": {}, "npm": {}, "pip": {},
	}
	if err := rejectUnknownEnvironmentFields(packages, allowed, "packages"); err != nil {
		return err
	}
	if value, present := packages["type"]; present {
		packageType, ok := value.(string)
		if !ok || packageType != "packages" {
			return domain.Validation("environment package configuration type must be packages")
		}
	}
	hasPackages := false
	for _, manager := range []string{"apt", "cargo", "gem", "go", "npm", "pip"} {
		value, present := packages[manager]
		if !present {
			continue
		}
		if err := validateEnvironmentStringList(value, "packages."+manager); err != nil {
			return err
		}
		switch values := value.(type) {
		case []string:
			hasPackages = hasPackages || len(values) > 0
		case []any:
			hasPackages = hasPackages || len(values) > 0
		}
	}
	if hasPackages {
		return domain.Unsupported("environment package installation is not implemented")
	}
	return nil
}

func validateEnvironmentStringList(value any, field string) error {
	switch values := value.(type) {
	case []string:
		return nil
	case []any:
		for _, entry := range values {
			if _, ok := entry.(string); !ok {
				return domain.Validation(
					fmt.Sprintf("environment %s values must be strings", field),
				)
			}
		}
		return nil
	default:
		return domain.Validation(
			fmt.Sprintf("environment %s must be an array", field),
		)
	}
}

func rejectUnknownEnvironmentFields(
	values map[string]any,
	allowed map[string]struct{},
	object string,
) error {
	unknown := make([]string, 0)
	for key := range values {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return domain.Validation(fmt.Sprintf("unknown environment %s field %q", object, unknown[0]))
}

func (s *EnvironmentService) Get(ctx context.Context, id string) (domain.Environment, error) {
	return s.env.Get(ctx, id)
}

func (s *EnvironmentService) List(
	ctx context.Context,
	query EnvironmentListQuery,
) (EnvironmentListPage, error) {
	if query.Limit <= 0 {
		query.Limit = DefaultEnvironmentListLimit
	}
	return s.env.List(ctx, query)
}

func (s *EnvironmentService) Archive(ctx context.Context, id string) (domain.Environment, error) {
	return s.env.Archive(ctx, id, s.clock.Now().UTC())
}

func (s *EnvironmentService) Delete(ctx context.Context, id string) error {
	return s.env.DeleteIfUnreferenced(ctx, id)
}
