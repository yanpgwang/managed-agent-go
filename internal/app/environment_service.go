package app

import (
	"context"

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
	now := s.clock.Now().UTC()
	e.ID = s.ids.NewID(domain.PrefixEnv)
	e.CreatedAt = now
	e.UpdatedAt = now
	return e, s.env.Put(ctx, e)
}

func (s *EnvironmentService) Get(ctx context.Context, id string) (domain.Environment, error) {
	return s.env.Get(ctx, id)
}

func (s *EnvironmentService) List(ctx context.Context) ([]domain.Environment, error) {
	return s.env.List(ctx)
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
