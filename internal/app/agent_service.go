package app

import (
	"context"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// AgentListQuery is the List Agents query. It is intentionally not shared with
// EnvironmentListQuery: the two endpoints document different parameter sets and
// a common struct would make it easy to accept a parameter one of them does not
// support. List Agents documents exactly created_at[gte], created_at[lte],
// include_archived, limit, and page.
type AgentListQuery struct {
	CreatedAtGte    *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	// After is the forward-only keyset boundary decoded from `page`.
	After *AgentPageBoundary
	Limit int
}

// AgentPageBoundary is the last row of the previous page.
type AgentPageBoundary struct {
	CreatedAt time.Time
	ID        string
}

// AgentListPage carries one page plus whether another page exists. The
// documented envelope is `data` + `next_page` only: forward-only, no prev.
type AgentListPage struct {
	Agents  []domain.Agent
	HasNext bool
}

// DefaultAgentListLimit and MaxAgentListLimit are the documented List Agents
// bounds ("Maximum results per page. Default 20, maximum 100."). They are
// specific to this endpoint and must not be reused for other list resources.
const (
	DefaultAgentListLimit = 20
	MaxAgentListLimit     = 100
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
	if err := domain.ValidateModel(a.Model); err != nil {
		return err
	}
	if err := domain.ValidateToolConfiguration(a.Tools, a.MCPServers); err != nil {
		return domain.Validation("invalid tool configuration: " + err.Error())
	}
	return validateMetadata(a.Metadata)
}
