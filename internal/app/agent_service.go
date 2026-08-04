package app

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type AgentService struct {
	repo     AgentRepository
	ids      domain.IDGenerator
	clock    domain.Clock
	skillRef SkillReferenceResolver
}

func NewAgentService(
	repo AgentRepository,
	ids domain.IDGenerator,
	clock domain.Clock,
	skillResolvers ...SkillReferenceResolver,
) *AgentService {
	service := &AgentService{repo: repo, ids: ids, clock: clock}
	if len(skillResolvers) > 0 {
		service.skillRef = skillResolvers[0]
	}
	return service
}

func (s *AgentService) Create(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	if err := validateAgent(a); err != nil {
		return domain.Agent{}, err
	}
	resolved, err := ResolveAgentSkillReferences(ctx, s.skillRef, a.Skills)
	if err != nil {
		return domain.Agent{}, err
	}
	a.Skills = resolved
	a.Model = domain.NormalizeModel(a.Model)
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

func (s *AgentService) List(ctx context.Context, query AgentListQuery) (AgentListPage, error) {
	if query.Limit <= 0 {
		query.Limit = DefaultAgentListLimit
	}
	return s.repo.ListLatest(ctx, query)
}

func (s *AgentService) Versions(
	ctx context.Context,
	id string,
	query AgentVersionListQuery,
) (AgentVersionListPage, error) {
	if query.Limit <= 0 {
		query.Limit = DefaultAgentListLimit
	}
	return s.repo.Versions(ctx, id, query)
}

func (s *AgentService) Update(ctx context.Context, id string, patch domain.AgentPatch) (domain.Agent, error) {
	effectivePatch := patch
	if patch.Skills != nil {
		// Resolve before UpdateVersion opens its serialization transaction. A
		// production resolver may use the same connection pool as the Agent
		// repository; calling it from the mutation callback can exhaust that pool
		// while every transaction is holding an Agent row lock.
		current, err := s.repo.Latest(ctx, id)
		if err != nil {
			return domain.Agent{}, err
		}
		if current.ArchivedAt != nil {
			return domain.Agent{}, domain.Validation("archived agent is read-only")
		}
		if patch.ExpectedVersion != nil && *patch.ExpectedVersion != current.Version {
			return domain.Agent{}, domain.Conflict("agent version mismatch")
		}
		resolved, err := ResolveAgentSkillReferences(ctx, s.skillRef, *patch.Skills)
		if err != nil {
			return domain.Agent{}, err
		}
		effectivePatch.Skills = &resolved
	}
	return s.repo.UpdateVersion(ctx, id, func(cur domain.Agent) (domain.Agent, bool, error) {
		if cur.ArchivedAt != nil {
			return domain.Agent{}, false, domain.Validation("archived agent is read-only")
		}
		next, changed, err := cur.Apply(effectivePatch)
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
	if err := domain.ValidateModel(a.Model); err != nil {
		return err
	}
	if err := domain.ValidateToolConfiguration(a.Tools, a.MCPServers); err != nil {
		return domain.Validation("invalid tool configuration: " + err.Error())
	}
	if err := domain.ValidateSkillToolConfiguration(a.Tools, len(a.Skills) > 0); err != nil {
		return domain.Validation("invalid Skill tool configuration: " + err.Error())
	}
	return validateMetadata(a.Metadata)
}
