package app

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type AgentService struct {
	repo  AgentRepository
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewAgentService(repo AgentRepository, ids domain.IDGenerator, clock domain.Clock) *AgentService {
	return &AgentService{repo: repo, ids: ids, clock: clock}
}

func (s *AgentService) Create(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	if err := validateAgent(a); err != nil {
		return domain.Agent{}, err
	}
	now := s.clock.Now().UTC()
	a.ID = s.ids.NewID(domain.PrefixAgent)
	a.Version = 1
	a.CreatedAt = now
	a.UpdatedAt = now
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	return a, s.repo.PutVersion(ctx, a)
}

func (s *AgentService) Get(ctx context.Context, id string) (domain.Agent, error) {
	return s.repo.Latest(ctx, id)
}

func (s *AgentService) List(ctx context.Context) ([]domain.Agent, error) { return s.repo.List(ctx) }

func (s *AgentService) Versions(ctx context.Context, id string) ([]domain.Agent, error) {
	return s.repo.Versions(ctx, id)
}

func (s *AgentService) Update(ctx context.Context, id string, patch domain.AgentPatch) (domain.Agent, error) {
	return s.repo.UpdateVersion(ctx, id, func(cur domain.Agent) (domain.Agent, bool, error) {
		if cur.ArchivedAt != nil {
			return domain.Agent{}, false, domain.Validation("archived agent is read-only")
		}
		next, changed, err := cur.Apply(patch)
		if err != nil {
			return domain.Agent{}, false, err
		}
		if err := validateAgent(next); err != nil {
			return domain.Agent{}, false, err
		}
		if changed {
			next.UpdatedAt = s.clock.Now().UTC()
		}
		return next, changed, nil
	})
}

func (s *AgentService) Archive(ctx context.Context, id string) (domain.Agent, error) {
	return s.repo.Archive(ctx, id, s.clock.Now().UTC())
}

func validateAgent(a domain.Agent) error {
	if a.Name == "" {
		return domain.Validation("name is required")
	}
	if a.Model.ID == "" {
		return domain.Validation("model is required")
	}
	if err := domain.ValidateToolConfiguration(a.Tools, a.MCPServers); err != nil {
		return domain.Validation("invalid tool configuration: " + err.Error())
	}
	return validateMetadata(a.Metadata)
}
