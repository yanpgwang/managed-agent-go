package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func newAgentService(t *testing.T) *AgentService {
	t.Helper()
	return NewAgentService(newMemoryAgentRepository(), domain.NewSeqIDGen(),
		domain.FixedClock{T: time.Unix(1, 0).UTC()})
}

func TestAgentService_CreateThenVersionedUpdate(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()
	a, err := s.Create(ctx, domain.Agent{Name: "a", Model: domain.Model{ID: "claude-opus-4-8"}})
	if err != nil || a.Version != 1 {
		t.Fatalf("create: %+v err=%v", a, err)
	}
	name := "b"
	up, err := s.Update(ctx, a.ID, domain.AgentPatch{Name: &name})
	if err != nil || up.Version != 2 || up.Name != "b" {
		t.Fatalf("update: %+v err=%v", up, err)
	}
	// no-op update returns same version
	noop, _ := s.Update(ctx, a.ID, domain.AgentPatch{})
	if noop.Version != 2 {
		t.Fatalf("no-op should stay v2, got %d", noop.Version)
	}
	// stale expected version -> conflict
	bad := 1
	if _, err := s.Update(ctx, a.ID, domain.AgentPatch{Name: &name, ExpectedVersion: &bad}); err == nil {
		t.Fatal("expected conflict on stale version")
	}
}

func TestAgentService_CreateValidation(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()

	// empty name
	_, err := s.Create(ctx, domain.Agent{})
	if err == nil {
		t.Fatal("expected validation error for empty name")
	}
	de, ok := err.(*domain.DomainError)
	if !ok || de.Kind != domain.KindValidation {
		t.Fatalf("expected DomainError KindValidation, got %v", err)
	}

	// name set but no model ID
	_, err = s.Create(ctx, domain.Agent{Name: "ok-name"})
	if err == nil {
		t.Fatal("expected validation error for empty model ID")
	}
	de, ok = err.(*domain.DomainError)
	if !ok || de.Kind != domain.KindValidation {
		t.Fatalf("expected DomainError KindValidation for missing model, got %v", err)
	}
}

func TestAgentService_ArchiveIdempotent(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()

	a, err := s.Create(ctx, domain.Agent{Name: "x", Model: domain.Model{ID: "m"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.Version != 1 {
		t.Fatalf("expected v1 after create, got %d", a.Version)
	}
	name := "x v2"
	current, err := s.Update(ctx, a.ID, domain.AgentPatch{Name: &name})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// Archiving is lifecycle state, not a configuration change: it must keep
	// the current version and must not append to version history.
	ar1, err := s.Archive(ctx, a.ID)
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if ar1.ArchivedAt == nil {
		t.Fatal("ArchivedAt should be set after first archive")
	}
	if ar1.Version != current.Version {
		t.Fatalf("archive changed version: got %d want %d", ar1.Version, current.Version)
	}
	versions, err := s.Versions(ctx, a.ID)
	if err != nil {
		t.Fatalf("versions after archive: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("archive appended configuration history: got %d versions, want 2", len(versions))
	}
	for _, version := range versions {
		if version.ArchivedAt == nil {
			t.Fatalf("version %d did not reflect resource archival", version.Version)
		}
	}

	// A second archive is idempotent.
	ar2, err := s.Archive(ctx, a.ID)
	if err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if ar2.ArchivedAt == nil {
		t.Fatal("ArchivedAt should still be set on second archive")
	}
	if ar2.Version != ar1.Version {
		t.Fatalf("version should not bump on idempotent archive: got %d want %d", ar2.Version, ar1.Version)
	}

	// Archived agents are read-only, including otherwise-no-op updates.
	if _, err := s.Update(ctx, a.ID, domain.AgentPatch{}); err == nil {
		t.Fatal("expected update of archived agent to fail")
	} else if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindValidation {
		t.Fatalf("expected validation error for archived update, got %v", err)
	}
}

func TestAgentService_MultiagentReplacementPersists(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()
	a, err := s.Create(ctx, domain.Agent{
		Name:       "coordinator",
		Model:      domain.Model{ID: "m"},
		Multiagent: map[string]any{"type": "coordinator", "agents": []any{"agent_one"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.Multiagent["type"] != "coordinator" {
		t.Fatalf("create lost multiagent: %#v", a.Multiagent)
	}

	replacement := map[string]any{"type": "coordinator", "agents": []any{"agent_two"}}
	updated, err := s.Update(ctx, a.ID, domain.AgentPatch{Multiagent: &replacement})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	agents, ok := updated.Multiagent["agents"].([]any)
	if !ok || len(agents) != 1 || agents[0] != "agent_two" {
		t.Fatalf("replacement was not persisted: %#v", updated.Multiagent)
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Multiagent["type"] != "coordinator" {
		t.Fatalf("stored multiagent missing: %#v", got.Multiagent)
	}

	var cleared map[string]any
	updated, err = s.Update(ctx, a.ID, domain.AgentPatch{Multiagent: &cleared})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if updated.Multiagent != nil {
		t.Fatalf("clear retained multiagent: %#v", updated.Multiagent)
	}
	got, err = s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.Multiagent != nil {
		t.Fatalf("stored multiagent was not cleared: %#v", got.Multiagent)
	}
}

func TestAgentService_MetadataConstraintsAndPostPatchValidation(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()

	cases := []struct {
		name     string
		metadata map[string]any
	}{
		{name: "non-string", metadata: map[string]any{"key": 1}},
		{name: "long-key", metadata: map[string]any{strings.Repeat("k", 65): "value"}},
		{name: "long-value", metadata: map[string]any{"key": strings.Repeat("v", 513)}},
	}
	tooMany := make(map[string]any, 17)
	for i := 0; i < 17; i++ {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	cases = append(cases, struct {
		name     string
		metadata map[string]any
	}{name: "too-many", metadata: tooMany})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Create(ctx, domain.Agent{
				Name: "agent", Model: domain.Model{ID: "m"}, Metadata: tc.metadata,
			}); err == nil {
				t.Fatal("expected invalid metadata to fail")
			} else if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindValidation {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}

	full := make(map[string]any, 16)
	for i := 0; i < 16; i++ {
		full[fmt.Sprintf("k%d", i)] = "v"
	}
	a, err := s.Create(ctx, domain.Agent{
		Name: "agent", Model: domain.Model{ID: "m"}, Metadata: full,
	})
	if err != nil {
		t.Fatalf("create at metadata limit: %v", err)
	}
	if _, err := s.Update(ctx, a.ID, domain.AgentPatch{
		Metadata: map[string]any{"overflow": "v"},
	}); err == nil {
		t.Fatal("expected resulting 17-key metadata bag to fail")
	}
	got, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("get after rejected update: %v", err)
	}
	if got.Version != 1 || len(got.Metadata) != 16 {
		t.Fatalf("rejected metadata patch mutated state: v%d %#v", got.Version, got.Metadata)
	}

	updated, err := s.Update(ctx, a.ID, domain.AgentPatch{
		Metadata: map[string]any{"k0": nil, "replacement": "ok"},
	})
	if err != nil {
		t.Fatalf("delete-and-add patch at limit: %v", err)
	}
	if len(updated.Metadata) != 16 || updated.Metadata["replacement"] != "ok" {
		t.Fatalf("post-patch metadata validation/merge wrong: %#v", updated.Metadata)
	}
}

func TestAgentService_ConcurrentExpectedVersionOnlyOneCommits(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()
	agent, err := s.Create(ctx, domain.Agent{
		Name: "v1", Model: domain.Model{ID: "m"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	expected := 1
	start := make(chan struct{})
	type updateResult struct {
		agent domain.Agent
		err   error
	}
	results := make(chan updateResult, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			updated, err := s.Update(ctx, agent.ID, domain.AgentPatch{
				Name: &name, ExpectedVersion: &expected,
			})
			results <- updateResult{agent: updated, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			successes++
			if result.agent.Version != 2 {
				t.Errorf("successful update version = %d, want 2", result.agent.Version)
			}
			continue
		}
		de, ok := result.err.(*domain.DomainError)
		if !ok || de.Kind != domain.KindConflict {
			t.Errorf("losing update error = %v, want conflict/HTTP 409", result.err)
			continue
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	versions, err := s.Versions(ctx, agent.ID)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 || versions[1].Version != 2 {
		t.Fatalf("version history after concurrent update = %#v", versions)
	}
}

func TestAgentService_UpdateArchiveRaceCannotResurrectAgent(t *testing.T) {
	s := newAgentService(t)
	ctx := context.Background()

	for i := 0; i < 40; i++ {
		agent, err := s.Create(ctx, domain.Agent{
			Name: fmt.Sprintf("agent-%d", i), Model: domain.Model{ID: "m"},
		})
		if err != nil {
			t.Fatalf("iteration %d create: %v", i, err)
		}

		expected := 1
		updatedName := fmt.Sprintf("updated-%d", i)
		start := make(chan struct{})
		var updateErr, archiveErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, updateErr = s.Update(ctx, agent.ID, domain.AgentPatch{
				Name: &updatedName, ExpectedVersion: &expected,
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, archiveErr = s.Archive(ctx, agent.ID)
		}()
		close(start)
		wg.Wait()

		if archiveErr != nil {
			t.Fatalf("iteration %d archive: %v", i, archiveErr)
		}
		if updateErr != nil {
			de, ok := updateErr.(*domain.DomainError)
			if !ok || (de.Kind != domain.KindValidation && de.Kind != domain.KindConflict) {
				t.Fatalf("iteration %d update error = %v", i, updateErr)
			}
		}

		latest, err := s.Get(ctx, agent.ID)
		if err != nil {
			t.Fatalf("iteration %d get: %v", i, err)
		}
		if latest.ArchivedAt == nil {
			t.Fatalf("iteration %d race left latest v%d unarchived", i, latest.Version)
		}
		versions, err := s.Versions(ctx, agent.ID)
		if err != nil {
			t.Fatalf("iteration %d versions: %v", i, err)
		}
		for _, version := range versions {
			if version.ArchivedAt == nil {
				t.Fatalf("iteration %d race left v%d unarchived: %#v", i, version.Version, versions)
			}
		}
	}
}
