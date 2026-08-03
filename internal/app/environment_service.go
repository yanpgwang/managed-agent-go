package app

import (
	"context"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// EnvironmentListQuery is the List Environments query. Upstream documents
// exactly three parameters for this endpoint — include_archived, limit, and
// page — and, unlike List Agents, no created_at filters at all. Keeping this
// type separate from AgentListQuery keeps that asymmetry impossible to blur.
type EnvironmentListQuery struct {
	IncludeArchived bool
	// After is the forward-only keyset boundary decoded from `page`.
	After *EnvironmentPageBoundary
	Limit int
}

// EnvironmentPageBoundary is the last row of the previous page.
type EnvironmentPageBoundary struct {
	CreatedAt time.Time
	ID        string
}

// EnvironmentListPage carries one page plus whether another page exists.
type EnvironmentListPage struct {
	Environments []domain.Environment
	HasNext      bool
}

// DefaultEnvironmentListLimit and MaxEnvironmentListLimit are a local choice.
// The List Environments reference documents `limit` with no default and no
// maximum, so Mango applies its general list bounds rather than borrowing the
// List Agents 20/100, which belongs to that endpoint alone.
const (
	DefaultEnvironmentListLimit = 100
	MaxEnvironmentListLimit     = 1000
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
	if e.Scope != "" {
		if err := domain.ValidateEnvironmentScope(e.Scope); err != nil {
			return domain.Environment{}, err
		}
	}
	if err := validateMetadata(e.Metadata); err != nil {
		return domain.Environment{}, err
	}
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
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

func (s *EnvironmentService) List(
	ctx context.Context,
	query EnvironmentListQuery,
) (EnvironmentListPage, error) {
	if query.Limit <= 0 {
		query.Limit = DefaultEnvironmentListLimit
	}
	return s.env.List(ctx, query)
}

// Update applies a partial Environment update under a repository row lock.
// Upstream does not document any effect on Sessions that are already running
// against the environment, so Mango deliberately does not propagate the change
// to live Sessions; each Session keeps the sandbox it was admitted with.
func (s *EnvironmentService) Update(
	ctx context.Context,
	id string,
	patch domain.EnvironmentPatch,
) (domain.Environment, error) {
	return s.env.Update(ctx, id, func(current domain.Environment) (domain.Environment, bool, error) {
		if current.ArchivedAt != nil {
			return domain.Environment{}, false, domain.Validation("archived environment is read-only")
		}
		next, changed, err := current.Apply(patch)
		if err != nil {
			return domain.Environment{}, false, err
		}
		if next.Name == "" {
			return domain.Environment{}, false, domain.Validation("name is required")
		}
		if err := validateMetadata(next.Metadata); err != nil {
			return domain.Environment{}, false, err
		}
		if changed {
			next.UpdatedAt = s.clock.Now().UTC()
		}
		return next, changed, nil
	})
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
