package app

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// These repositories are deliberately test-only. They exercise application
// validation without introducing a second production persistence backend.
type memoryAgentRepository struct {
	mu       sync.Mutex
	versions map[string][]domain.Agent
}

func newMemoryAgentRepository() *memoryAgentRepository {
	return &memoryAgentRepository{versions: make(map[string][]domain.Agent)}
}

func (r *memoryAgentRepository) PutVersion(_ context.Context, agent domain.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions[agent.ID] = append(r.versions[agent.ID], agent)
	return nil
}

func (r *memoryAgentRepository) UpdateVersion(
	_ context.Context,
	id string,
	update func(domain.Agent) (domain.Agent, bool, error),
) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if len(versions) == 0 {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	current := versions[len(versions)-1]
	next, changed, err := update(current)
	if err != nil {
		return domain.Agent{}, err
	}
	if !changed {
		return current, nil
	}
	next.Version = current.Version + 1
	r.versions[id] = append(versions, next)
	return next, nil
}

func (r *memoryAgentRepository) Archive(
	_ context.Context,
	id string,
	at time.Time,
) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if len(versions) == 0 {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	if versions[len(versions)-1].ArchivedAt == nil {
		for index := range versions {
			versions[index].ArchivedAt = &at
			versions[index].UpdatedAt = at
		}
		r.versions[id] = versions
	}
	return versions[len(versions)-1], nil
}

func (r *memoryAgentRepository) Latest(_ context.Context, id string) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if len(versions) == 0 {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	return versions[len(versions)-1], nil
}

func (r *memoryAgentRepository) GetVersion(
	_ context.Context,
	id string,
	version int,
) (domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, agent := range r.versions[id] {
		if agent.Version == version {
			return agent, nil
		}
	}
	return domain.Agent{}, domain.NotFound("agent version not found")
}

func (r *memoryAgentRepository) Versions(
	_ context.Context,
	id string,
	query AgentVersionListQuery,
) (AgentVersionListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if len(versions) == 0 {
		return AgentVersionListPage{}, domain.NotFound("agent not found")
	}
	pageVersions := make([]domain.Agent, 0, query.Limit+1)
	for _, version := range versions {
		if version.Version <= query.AfterVersion {
			continue
		}
		pageVersions = append(pageVersions, version)
		if query.Limit > 0 && len(pageVersions) > query.Limit {
			break
		}
	}
	page := AgentVersionListPage{Versions: pageVersions}
	if query.Limit > 0 && len(pageVersions) > query.Limit {
		page.Versions = pageVersions[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

func (r *memoryAgentRepository) ListLatest(
	_ context.Context,
	query AgentListQuery,
) (AgentListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agents := make([]domain.Agent, 0, len(r.versions))
	for _, versions := range r.versions {
		latest := versions[len(versions)-1]
		if !query.IncludeArchived && latest.ArchivedAt != nil {
			continue
		}
		if query.CreatedAtGte != nil && latest.CreatedAt.Before(*query.CreatedAtGte) {
			continue
		}
		if query.CreatedAtLte != nil && latest.CreatedAt.After(*query.CreatedAtLte) {
			continue
		}
		if query.After != nil &&
			(latest.CreatedAt.After(query.After.CreatedAt) ||
				(latest.CreatedAt.Equal(query.After.CreatedAt) && latest.ID >= query.After.ID)) {
			continue
		}
		agents = append(agents, latest)
	}
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].CreatedAt.After(agents[j].CreatedAt) ||
			(agents[i].CreatedAt.Equal(agents[j].CreatedAt) && agents[i].ID > agents[j].ID)
	})
	page := AgentListPage{Agents: agents}
	if query.Limit > 0 && len(agents) > query.Limit {
		page.Agents = agents[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

type memoryEnvironmentRepository struct {
	mu         sync.Mutex
	values     map[string]domain.Environment
	referenced map[string]bool
}

func newMemoryEnvironmentRepository() *memoryEnvironmentRepository {
	return &memoryEnvironmentRepository{
		values:     make(map[string]domain.Environment),
		referenced: make(map[string]bool),
	}
}

func (r *memoryEnvironmentRepository) Put(
	_ context.Context,
	environment domain.Environment,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[environment.ID] = environment
	return nil
}

func (r *memoryEnvironmentRepository) Update(
	_ context.Context,
	environment domain.Environment,
) (domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.values[environment.ID]
	if !ok {
		return domain.Environment{}, domain.NotFound("environment not found")
	}
	if current.ArchivedAt != nil {
		return domain.Environment{}, domain.Validation("archived environment is read-only")
	}
	environment.ArchivedAt = current.ArchivedAt
	r.values[environment.ID] = environment
	return environment, nil
}

func (r *memoryEnvironmentRepository) Archive(
	_ context.Context,
	id string,
	archivedAt time.Time,
) (domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	environment, ok := r.values[id]
	if !ok {
		return domain.Environment{}, domain.NotFound("environment not found")
	}
	if environment.ArchivedAt == nil {
		environment.ArchivedAt = &archivedAt
		environment.UpdatedAt = archivedAt
		r.values[id] = environment
	}
	return environment, nil
}

func (r *memoryEnvironmentRepository) Get(
	_ context.Context,
	id string,
) (domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	environment, ok := r.values[id]
	if !ok {
		return domain.Environment{}, domain.NotFound("environment not found")
	}
	return environment, nil
}

func (r *memoryEnvironmentRepository) List(
	_ context.Context,
	query EnvironmentListQuery,
) (EnvironmentListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	environments := make([]domain.Environment, 0, len(r.values))
	for _, environment := range r.values {
		if !query.IncludeArchived && environment.ArchivedAt != nil {
			continue
		}
		if query.After != nil &&
			(environment.CreatedAt.After(query.After.CreatedAt) ||
				(environment.CreatedAt.Equal(query.After.CreatedAt) &&
					environment.ID >= query.After.ID)) {
			continue
		}
		environments = append(environments, environment)
	}
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].CreatedAt.After(environments[j].CreatedAt) ||
			(environments[i].CreatedAt.Equal(environments[j].CreatedAt) &&
				environments[i].ID > environments[j].ID)
	})
	page := EnvironmentListPage{Environments: environments}
	if query.Limit > 0 && len(environments) > query.Limit {
		page.Environments = environments[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

func (r *memoryEnvironmentRepository) DeleteIfUnreferenced(
	_ context.Context,
	id string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[id]; !ok {
		return domain.NotFound("environment not found")
	}
	if r.referenced[id] {
		return domain.Conflict("environment is referenced by a session")
	}
	delete(r.values, id)
	return nil
}

func (r *memoryEnvironmentRepository) markReferenced(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.referenced[id] = true
}
