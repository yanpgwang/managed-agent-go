package pg

import (
	"context"
	"sync"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// PostgreSQL-backed coverage for the two list endpoints and for the locked
// Environment update. These skip without MANAGED_AGENT_TEST_DATABASE_URL.

func TestPostgresListAgentsPagesLatestVersionsOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	agents := app.NewAgentService(NewAgentRepository(store), &seqIDGen{}, fixedClock{})

	created := make([]domain.Agent, 0, 5)
	for range 5 {
		agent, err := agents.Create(ctx, domain.Agent{
			Name: "coder", Model: domain.Model{ID: "claude-test"},
		})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		created = append(created, agent)
	}
	// Superseding one agent must not add a second row to the list: the agents
	// table is append-only with PRIMARY KEY (id, version).
	renamed := "coder-v2"
	if _, err := agents.Update(ctx, created[0].ID, domain.AgentPatch{Name: &renamed}); err != nil {
		t.Fatalf("update agent: %v", err)
	}

	seen := map[string]int{}
	versions := map[string]int{}
	var boundary *app.AgentPageBoundary
	pages := 0
	for {
		page, err := store.ListAgents(ctx, app.AgentListQuery{Limit: 2, After: boundary})
		if err != nil {
			t.Fatalf("list agents: %v", err)
		}
		if len(page.Agents) > 2 {
			t.Fatalf("page returned %d agents, want at most 2", len(page.Agents))
		}
		for _, agent := range page.Agents {
			seen[agent.ID]++
			versions[agent.ID] = agent.Version
		}
		pages++
		if !page.HasNext {
			break
		}
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		last := page.Agents[len(page.Agents)-1]
		boundary = &app.AgentPageBoundary{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if pages != 3 {
		t.Fatalf("paged in %d requests, want 3", pages)
	}
	if len(seen) != len(created) {
		t.Fatalf("saw %d distinct agents, want %d", len(seen), len(created))
	}
	for _, agent := range created {
		if seen[agent.ID] != 1 {
			t.Errorf("agent %s appeared %d times", agent.ID, seen[agent.ID])
		}
	}
	if versions[created[0].ID] != 2 {
		t.Fatalf("listed version for the updated agent = %d, want 2", versions[created[0].ID])
	}
}

func TestPostgresListAgentsFilters(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewAgentRepository(store)
	agents := app.NewAgentService(repo, &seqIDGen{}, fixedClock{})

	first, err := agents.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "m"}})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := agents.Create(ctx, domain.Agent{Name: "b", Model: domain.Model{ID: "m"}})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	// Newest-first ordering by the relational (created_at, id) key.
	page, err := store.ListAgents(ctx, app.AgentListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Agents) != 2 || page.Agents[0].ID != second.ID || page.Agents[1].ID != first.ID {
		t.Fatalf("order = %v, want newest first", agentIDs(page.Agents))
	}
	if page.HasNext {
		t.Fatal("HasNext set on a complete page")
	}

	// created_at[gte] / created_at[lte] are inclusive.
	gte := second.CreatedAt
	page, err = store.ListAgents(ctx, app.AgentListQuery{Limit: 10, CreatedAtGte: &gte})
	if err != nil {
		t.Fatalf("gte list: %v", err)
	}
	if len(page.Agents) != 1 || page.Agents[0].ID != second.ID {
		t.Fatalf("created_at[gte] = %v, want only %s", agentIDs(page.Agents), second.ID)
	}
	lte := first.CreatedAt
	page, err = store.ListAgents(ctx, app.AgentListQuery{Limit: 10, CreatedAtLte: &lte})
	if err != nil {
		t.Fatalf("lte list: %v", err)
	}
	if len(page.Agents) != 1 || page.Agents[0].ID != first.ID {
		t.Fatalf("created_at[lte] = %v, want only %s", agentIDs(page.Agents), first.ID)
	}

	// Archival is projected across versions, so it filters from the list.
	if _, err := agents.Archive(ctx, first.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	page, err = store.ListAgents(ctx, app.AgentListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("post-archive list: %v", err)
	}
	if len(page.Agents) != 1 || page.Agents[0].ID != second.ID {
		t.Fatalf("archived agent still listed: %v", agentIDs(page.Agents))
	}
	page, err = store.ListAgents(ctx, app.AgentListQuery{Limit: 10, IncludeArchived: true})
	if err != nil {
		t.Fatalf("include_archived list: %v", err)
	}
	if len(page.Agents) != 2 {
		t.Fatalf("include_archived list = %v, want 2", agentIDs(page.Agents))
	}
	for _, agent := range page.Agents {
		if agent.ID == first.ID && agent.ArchivedAt == nil {
			t.Fatal("archived agent has no archived_at in the list projection")
		}
	}
}

func TestPostgresListEnvironmentsPagination(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewEnvironmentRepository(store)
	environments := app.NewEnvironmentService(repo, &seqIDGen{}, fixedClock{})

	created := make([]domain.Environment, 0, 5)
	for range 5 {
		environment, err := environments.Create(ctx, domain.Environment{
			Name: "cloud", ConfigType: "cloud", Config: map[string]any{"type": "cloud"},
		})
		if err != nil {
			t.Fatalf("create environment: %v", err)
		}
		created = append(created, environment)
	}

	seen := map[string]int{}
	var boundary *app.EnvironmentPageBoundary
	pages := 0
	for {
		page, err := store.ListEnvironments(ctx, app.EnvironmentListQuery{Limit: 2, After: boundary})
		if err != nil {
			t.Fatalf("list environments: %v", err)
		}
		if len(page.Environments) > 2 {
			t.Fatalf("page returned %d environments, want at most 2", len(page.Environments))
		}
		for _, environment := range page.Environments {
			seen[environment.ID]++
		}
		pages++
		if !page.HasNext {
			break
		}
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		last := page.Environments[len(page.Environments)-1]
		boundary = &app.EnvironmentPageBoundary{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if pages != 3 {
		t.Fatalf("paged in %d requests, want 3", pages)
	}
	for _, environment := range created {
		if seen[environment.ID] != 1 {
			t.Errorf("environment %s appeared %d times", environment.ID, seen[environment.ID])
		}
	}

	if _, err := environments.Archive(ctx, created[0].ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	page, err := store.ListEnvironments(ctx, app.EnvironmentListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("post-archive list: %v", err)
	}
	if len(page.Environments) != 4 {
		t.Fatalf("archived environment still listed: %d rows", len(page.Environments))
	}
	page, err = store.ListEnvironments(ctx, app.EnvironmentListQuery{
		Limit: 10, IncludeArchived: true,
	})
	if err != nil {
		t.Fatalf("include_archived list: %v", err)
	}
	if len(page.Environments) != 5 {
		t.Fatalf("include_archived list = %d rows, want 5", len(page.Environments))
	}
}

func TestPostgresUpdateEnvironmentIsAtomicUnderConcurrency(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewEnvironmentRepository(store)
	environments := app.NewEnvironmentService(repo, &seqIDGen{}, fixedClock{})

	created, err := environments.Create(ctx, domain.Environment{
		Name: "cloud", ConfigType: "cloud", Config: map[string]any{"type": "cloud"},
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	// Two disjoint partial updates issued concurrently must both survive: the
	// row lock serializes the read-modify-write so neither writes a body derived
	// from a stale snapshot.
	description := "from writer A"
	patches := []domain.EnvironmentPatch{
		{Description: &description},
		{Metadata: map[string]any{"team": "ml"}},
	}
	var wait sync.WaitGroup
	errs := make([]error, len(patches))
	for index, patch := range patches {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, errs[index] = environments.Update(ctx, created.ID, patch)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent update %d: %v", index, err)
		}
	}

	final, err := environments.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.Description != description {
		t.Errorf("description = %q, want %q", final.Description, description)
	}
	if final.Metadata["team"] != "ml" {
		t.Errorf("metadata = %v, want team=ml", final.Metadata)
	}
	if final.CreatedAt.IsZero() || !final.UpdatedAt.After(final.CreatedAt) {
		t.Errorf("timestamps not advanced: created=%v updated=%v", final.CreatedAt, final.UpdatedAt)
	}
}

func TestPostgresUpdateEnvironmentUnknownIDIsNotFound(t *testing.T) {
	store := testStore(t)
	_, err := store.UpdateEnvironment(context.Background(), "env_missing",
		func(e domain.Environment) (domain.Environment, bool, error) { return e, true, nil })
	if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindNotFound {
		t.Fatalf("update unknown environment = %v, want not found", err)
	}
}

func agentIDs(agents []domain.Agent) []string {
	out := make([]string, len(agents))
	for index, agent := range agents {
		out[index] = agent.ID
	}
	return out
}
