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
	if e.Name == "" {
		return domain.Environment{}, domain.Validation("name is required")
	}
	if e.ConfigType == "" {
		e.ConfigType = "cloud"
	}
	if e.ConfigType != "cloud" && e.ConfigType != "self_hosted" {
		return domain.Environment{}, domain.Validation("config type must be cloud or self_hosted")
	}
	if e.Scope != "" && e.Scope != "organization" && e.Scope != "account" {
		return domain.Environment{}, domain.Validation("scope must be organization or account")
	}
	if e.ConfigType == "cloud" && e.Scope != "" {
		return domain.Environment{}, domain.Validation("scope is only supported for self_hosted environments")
	}
	if err := validateMetadata(e.Metadata); err != nil {
		return domain.Environment{}, err
	}
	if err := validateEnvironmentConfig(e.Config, e.ConfigType); err != nil {
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
	e, err := s.env.Get(ctx, id)
	if err != nil {
		return domain.Environment{}, err
	}
	if e.ArchivedAt != nil {
		return e, nil
	}
	now := s.clock.Now().UTC()
	e.ArchivedAt = &now
	e.UpdatedAt = now
	return e, s.env.Put(ctx, e)
}

func (s *EnvironmentService) Delete(ctx context.Context, id string) error {
	return s.env.DeleteIfUnreferenced(ctx, id)
}
