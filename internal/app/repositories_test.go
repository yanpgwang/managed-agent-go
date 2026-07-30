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
) ([]domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[id]
	if len(versions) == 0 {
		return nil, domain.NotFound("agent not found")
	}
	return append([]domain.Agent(nil), versions...), nil
}

func (r *memoryAgentRepository) List(_ context.Context) ([]domain.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agents := make([]domain.Agent, 0, len(r.versions))
	for _, versions := range r.versions {
		agents = append(agents, versions[len(versions)-1])
	}
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].CreatedAt.Before(agents[j].CreatedAt) ||
			(agents[i].CreatedAt.Equal(agents[j].CreatedAt) && agents[i].ID < agents[j].ID)
	})
	return agents, nil
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

func (r *memoryEnvironmentRepository) List(_ context.Context) ([]domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	environments := make([]domain.Environment, 0, len(r.values))
	for _, environment := range r.values {
		environments = append(environments, environment)
	}
	sort.Slice(environments, func(i, j int) bool {
		return environments[i].CreatedAt.Before(environments[j].CreatedAt) ||
			(environments[i].CreatedAt.Equal(environments[j].CreatedAt) &&
				environments[i].ID < environments[j].ID)
	})
	return environments, nil
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
