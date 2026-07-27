package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestAgentRepo_VersionsAndLatest(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	r := NewAgentRepo(db)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_1", Version: 1, Name: "a", CreatedAt: now, UpdatedAt: now}))
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_1", Version: 2, Name: "b", CreatedAt: now, UpdatedAt: now}))
	got, err := r.Latest(ctx, "agent_1")
	if err != nil || got.Version != 2 || got.Name != "b" {
		t.Fatalf("latest wrong: %+v err=%v", got, err)
	}
	vs, _ := r.Versions(ctx, "agent_1")
	if len(vs) != 2 || vs[0].Version != 1 || vs[1].Version != 2 {
		t.Fatalf("versions wrong: %+v", vs)
	}
	if _, err := r.Latest(ctx, "nope"); err == nil {
		t.Fatal("expected NotFound")
	}
}

func TestSessionRepo_CreateIfDependenciesActive(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	agents := NewAgentRepo(db)
	environments := NewEnvironmentRepo(db)
	sessions := NewSessionRepo(db)
	must(t, agents.PutVersion(ctx, domain.Agent{
		ID: "agent_active", Version: 1, Name: "agent",
		Model: domain.Model{ID: "model"}, CreatedAt: now, UpdatedAt: now,
	}))
	must(t, environments.Put(ctx, domain.Environment{
		ID: "env_active", Name: "environment", ConfigType: "cloud",
		CreatedAt: now, UpdatedAt: now,
	}))

	base := domain.Session{
		ID: "sesn_active", AgentID: "agent_active", AgentVersion: 1,
		EnvironmentID: "env_active", Status: domain.StatusIdle,
		CreatedAt: now, UpdatedAt: now,
	}
	must(t, sessions.CreateIfDependenciesActive(ctx, base))

	missingVersion := base
	missingVersion.ID = "sesn_missing_version"
	missingVersion.AgentVersion = 2
	if err := sessions.CreateIfDependenciesActive(ctx, missingVersion); err == nil {
		t.Fatal("created session for a missing exact agent version")
	}

	archivedAt := now.Add(time.Second)
	if _, err := agents.Archive(ctx, "agent_active", archivedAt); err != nil {
		t.Fatal(err)
	}
	archivedAgent := base
	archivedAgent.ID = "sesn_archived_agent"
	if err := sessions.CreateIfDependenciesActive(ctx, archivedAgent); err == nil {
		t.Fatal("created session for an archived agent")
	}

	environment, err := environments.Get(ctx, "env_active")
	if err != nil {
		t.Fatal(err)
	}
	environment.ArchivedAt = &archivedAt
	environment.UpdatedAt = archivedAt
	must(t, environments.Put(ctx, environment))
	archivedEnvironment := base
	archivedEnvironment.ID = "sesn_archived_environment"
	if err := sessions.CreateIfDependenciesActive(ctx, archivedEnvironment); err == nil {
		t.Fatal("created session for an archived environment")
	}
}

func TestAgentRepo_ListLatestOfEach(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	r := NewAgentRepo(db)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_a", Version: 1, Name: "a-v1", CreatedAt: now, UpdatedAt: now}))
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_a", Version: 2, Name: "a-v2", CreatedAt: now, UpdatedAt: now}))
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_b", Version: 1, Name: "b-v1", CreatedAt: now, UpdatedAt: now}))
	must(t, r.PutVersion(ctx, domain.Agent{ID: "agent_b", Version: 2, Name: "b-v2", CreatedAt: now, UpdatedAt: now}))
	got, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(got), got)
	}
	for _, a := range got {
		if a.Version != 2 {
			t.Errorf("agent %q: expected version 2, got %d", a.ID, a.Version)
		}
	}
}

func TestAgentRepo_ArchiveIsResourceStateNotConfigurationVersion(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	r := NewAgentRepo(db)
	ctx := context.Background()
	createdAt := time.Unix(1, 0).UTC()
	updatedAt := time.Unix(2, 0).UTC()
	archivedAt := time.Unix(3, 0).UTC()
	must(t, r.PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "v1", CreatedAt: createdAt, UpdatedAt: createdAt,
	}))
	must(t, r.PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 2, Name: "v2", CreatedAt: createdAt, UpdatedAt: updatedAt,
	}))

	archived, err := r.Archive(ctx, "agent_1", archivedAt)
	if err != nil {
		t.Fatalf("Archive error: %v", err)
	}
	if archived.Version != 2 {
		t.Fatalf("archive changed version: got %d want 2", archived.Version)
	}
	if archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("archive timestamp = %v, want %v", archived.ArchivedAt, archivedAt)
	}
	if !archived.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("archive changed configuration updated_at: got %v want %v", archived.UpdatedAt, updatedAt)
	}

	versions, err := r.Versions(ctx, "agent_1")
	if err != nil {
		t.Fatalf("Versions error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("archive appended a version: got %d versions", len(versions))
	}
	for _, version := range versions {
		if version.ArchivedAt == nil || !version.ArchivedAt.Equal(archivedAt) {
			t.Fatalf("version %d does not reflect resource archive: %#v", version.Version, version.ArchivedAt)
		}
	}

	again, err := r.Archive(ctx, "agent_1", time.Unix(4, 0).UTC())
	if err != nil {
		t.Fatalf("second Archive error: %v", err)
	}
	if again.ArchivedAt == nil || !again.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("idempotent archive changed timestamp: %v", again.ArchivedAt)
	}
	if _, err := r.Archive(ctx, "missing", archivedAt); err == nil {
		t.Fatal("expected missing agent archive to fail")
	}
}

func TestAgentRepo_ConcurrentExpectedVersionUsesConditionalInsert(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	// Exercise the SQL guard across two real SQLite connections rather than
	// relying on the production pool's single-connection serialization.
	db.SetMaxOpenConns(2)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	must(t, repo.PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "v1", Model: domain.Model{ID: "m"},
		CreatedAt: now, UpdatedAt: now,
	}))

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.UpdateVersion(ctx, "agent_1", func(current domain.Agent) (domain.Agent, bool, error) {
				ready <- struct{}{}
				<-release
				expected := 1
				next, changed, err := current.Apply(domain.AgentPatch{
					Name: &name, ExpectedVersion: &expected,
				})
				next.UpdatedAt = now.Add(time.Second)
				return next, changed, err
			})
			errs <- err
		}()
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(errs)

	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		de, ok := err.(*domain.DomainError)
		if !ok || de.Kind != domain.KindConflict {
			t.Fatalf("concurrent update error = %v, want conflict", err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	versions, err := repo.Versions(ctx, "agent_1")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("conditional insert created %d versions, want 2 total", len(versions))
	}
}

func TestAgentRepo_ArchiveWinsAgainstInFlightUpdate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	must(t, repo.PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "v1", Model: domain.Model{ID: "m"},
		CreatedAt: now, UpdatedAt: now,
	}))

	updateRead := make(chan struct{})
	releaseUpdate := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		_, err := repo.UpdateVersion(ctx, "agent_1", func(current domain.Agent) (domain.Agent, bool, error) {
			close(updateRead)
			<-releaseUpdate
			name := "v2"
			next, changed, err := current.Apply(domain.AgentPatch{Name: &name})
			next.UpdatedAt = now.Add(time.Second)
			return next, changed, err
		})
		updateDone <- err
	}()
	<-updateRead

	// Archive commits on another WAL connection after the updater has read v1
	// but before it attempts the conditional insert.
	archived, err := repo.Archive(ctx, "agent_1", now.Add(2*time.Second))
	if err != nil {
		close(releaseUpdate)
		<-updateDone
		t.Fatalf("archive: %v", err)
	}
	close(releaseUpdate)
	updateErr := <-updateDone
	de, ok := updateErr.(*domain.DomainError)
	if !ok || de.Kind != domain.KindConflict {
		t.Fatalf("in-flight update error = %v, want conflict", updateErr)
	}
	if archived.ArchivedAt == nil || archived.Version != 1 {
		t.Fatalf("archive result = %#v", archived)
	}

	versions, err := repo.Versions(ctx, "agent_1")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 1 || versions[0].ArchivedAt == nil {
		t.Fatalf("in-flight update resurrected archived agent: %#v", versions)
	}
}

func TestSessionRepo_ListOrder(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	r := NewSessionRepo(db)
	ctx := context.Background()
	for i, id := range []string{"ses_1", "ses_2", "ses_3"} {
		ts := time.Unix(int64(i+1), 0).UTC()
		must(t, r.Put(ctx, domain.Session{ID: id, Status: domain.StatusIdle, CreatedAt: ts, UpdatedAt: ts}))
	}
	desc, err := r.List(ctx, SessionListQuery{Limit: 10, Desc: true})
	if err != nil {
		t.Fatalf("List desc error: %v", err)
	}
	asc, err := r.List(ctx, SessionListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List asc error: %v", err)
	}
	if len(desc.Sessions) != 3 || len(asc.Sessions) != 3 {
		t.Fatalf("expected 3 sessions each, got desc=%d asc=%d", len(desc.Sessions), len(asc.Sessions))
	}
	if desc.Sessions[0].ID == asc.Sessions[0].ID {
		t.Fatalf("expected different first elements but both are %q", desc.Sessions[0].ID)
	}
	if desc.Sessions[0].ID != "ses_3" {
		t.Errorf("desc first expected ses_3, got %q", desc.Sessions[0].ID)
	}
	if asc.Sessions[0].ID != "ses_1" {
		t.Errorf("asc first expected ses_1, got %q", asc.Sessions[0].ID)
	}
}

func TestSessionRepo_ListOrdersAndFiltersFractionalTimestampsChronologically(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	repo := NewSessionRepo(db)
	ctx := context.Background()
	exact := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	fractional := exact.Add(100 * time.Millisecond)
	must(t, repo.Put(ctx, domain.Session{
		ID: "ses_exact", Status: domain.StatusIdle, CreatedAt: exact, UpdatedAt: exact,
	}))
	must(t, repo.Put(ctx, domain.Session{
		ID: "ses_fractional", Status: domain.StatusIdle, CreatedAt: fractional, UpdatedAt: fractional,
	}))

	ordered, err := repo.List(ctx, SessionListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered.Sessions) != 2 {
		t.Fatalf("ascending order returned %d sessions, want 2", len(ordered.Sessions))
	}
	if got := []string{ordered.Sessions[0].ID, ordered.Sessions[1].ID}; got[0] != "ses_exact" || got[1] != "ses_fractional" {
		t.Fatalf("ascending order = %v, want [ses_exact ses_fractional]", got)
	}

	filtered, err := repo.List(ctx, SessionListQuery{CreatedAtGt: &exact, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Sessions) != 1 || filtered.Sessions[0].ID != "ses_fractional" {
		t.Fatalf("created_at gt exact = %+v, want ses_fractional", filtered.Sessions)
	}
}

func TestEnvironmentRepo_PutGetUpsert(t *testing.T) {
	db, _ := OpenMemory()
	defer db.Close()
	r := NewEnvironmentRepo(db)
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	env := domain.Environment{ID: "env_1", Name: "original", ConfigType: "cloud", CreatedAt: now, UpdatedAt: now}
	must(t, r.Put(ctx, env))
	got, err := r.Get(ctx, "env_1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.Name != "original" || got.ConfigType != "cloud" {
		t.Fatalf("unexpected values: name=%q config_type=%q", got.Name, got.ConfigType)
	}
	env.Name = "updated"
	must(t, r.Put(ctx, env))
	got2, err := r.Get(ctx, "env_1")
	if err != nil {
		t.Fatalf("Get after upsert error: %v", err)
	}
	if got2.Name != "updated" {
		t.Errorf("upsert did not update name, got %q", got2.Name)
	}
	all, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 environment after upsert, got %d", len(all))
	}
}

func TestEnvironmentRepo_ConcurrentDeleteAndSessionCreateNeverOrphans(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "dependency-race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)

	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	agents := NewAgentRepo(db)
	environments := NewEnvironmentRepo(db)
	sessions := NewSessionRepo(db)
	must(t, agents.PutVersion(ctx, domain.Agent{
		ID: "agent_race", Version: 1, Name: "agent", Model: domain.Model{ID: "model"},
		CreatedAt: now, UpdatedAt: now,
	}))

	const iterations = 100
	for iteration := 0; iteration < iterations; iteration++ {
		environmentID := fmt.Sprintf("env_race_%d", iteration)
		sessionID := fmt.Sprintf("sesn_race_%d", iteration)
		must(t, environments.Put(ctx, domain.Environment{
			ID: environmentID, Name: "environment", ConfigType: "cloud",
			CreatedAt: now, UpdatedAt: now,
		}))
		session := domain.Session{
			ID: sessionID, AgentID: "agent_race", AgentVersion: 1,
			EnvironmentID: environmentID, Status: domain.StatusIdle,
			CreatedAt: now, UpdatedAt: now,
		}

		start := make(chan struct{})
		createDone := make(chan error, 1)
		deleteDone := make(chan error, 1)
		go func() {
			<-start
			createDone <- sessions.CreateIfDependenciesActive(ctx, session)
		}()
		go func() {
			<-start
			deleteDone <- environments.DeleteIfUnreferenced(ctx, environmentID)
		}()
		close(start)
		createErr := <-createDone
		deleteErr := <-deleteDone

		_, sessionErr := sessions.Get(ctx, sessionID)
		_, environmentErr := environments.Get(ctx, environmentID)
		sessionExists := sessionErr == nil
		environmentExists := environmentErr == nil
		if sessionExists && !environmentExists {
			t.Fatalf("iteration %d left an orphan session", iteration)
		}
		if (createErr == nil) == (deleteErr == nil) {
			t.Fatalf(
				"iteration %d outcomes create=%v delete=%v; exactly one must succeed",
				iteration, createErr, deleteErr,
			)
		}
		if createErr == nil && (!sessionExists || !environmentExists) {
			t.Fatalf(
				"iteration %d successful create has session=%v environment=%v",
				iteration, sessionExists, environmentExists,
			)
		}
		if deleteErr == nil && (sessionExists || environmentExists) {
			t.Fatalf(
				"iteration %d successful delete has session=%v environment=%v",
				iteration, sessionExists, environmentExists,
			)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
