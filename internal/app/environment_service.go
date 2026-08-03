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
	if value, present := config["networking"]; present && value != nil {
		return domain.Unsupported("environment networking configuration is not implemented")
	}
	if value, present := config["packages"]; present && value != nil {
		return domain.Unsupported("environment package configuration is not implemented")
	}

	unknown := make([]string, 0)
	for key := range config {
		if key != "type" && key != "networking" && key != "packages" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return domain.Validation(fmt.Sprintf("unknown environment config field %q", unknown[0]))
	}
	return nil
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
