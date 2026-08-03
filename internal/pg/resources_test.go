package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

func TestPostgresResourcesAndSessionDependencies(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	ids := &seqIDGen{}
	clock := fixedClock{}
	agentRepo := NewAgentRepository(store)
	environmentRepo := NewEnvironmentRepository(store)
	agents := app.NewAgentService(agentRepo, ids, clock)
	environments := app.NewEnvironmentService(environmentRepo, ids, clock)

	agent, err := agents.Create(ctx, domain.Agent{
		Name: "coder", Model: domain.Model{ID: "claude-test"},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	renamed := "coder-v2"
	agent, err = agents.Update(ctx, agent.ID, domain.AgentPatch{Name: &renamed})
	if err != nil {
		t.Fatalf("update agent: %v", err)
	}
	if agent.Version != 2 || agent.Name != renamed {
		t.Fatalf("updated agent = %+v, want version 2/name %q", agent, renamed)
	}
	versionPage, err := agents.Versions(ctx, agent.ID, app.AgentVersionListQuery{})
	if err != nil || len(versionPage.Versions) != 2 {
		t.Fatalf("versions = %d, err=%v; want 2", len(versionPage.Versions), err)
	}

	environment, err := environments.Create(ctx, domain.Environment{
		Name: "cloud", ConfigType: "cloud", Config: map[string]any{"type": "cloud"},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	now := clock.Now().UTC()
	session := domain.Session{
		ID:            "sesn_api",
		AgentID:       agent.ID,
		AgentVersion:  agent.Version,
		EnvironmentID: environment.ID,
		Status:        domain.StatusIdle,
		AgentSnapshot: agent,
		Metadata:      map[string]any{},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := store.CreateAPISession(ctx, session, nil); err != nil {
		t.Fatalf("create checked session: %v", err)
	}
	if err := environments.Delete(ctx, environment.ID); err == nil {
		t.Fatal("delete referenced environment succeeded; want conflict")
	} else if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindConflict {
		t.Fatalf("delete referenced environment = %v, want conflict", err)
	}

	if _, err := agents.Archive(ctx, agent.ID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	session.ID = "sesn_after_archive"
	if _, err := store.CreateAPISession(ctx, session, nil); err == nil {
		t.Fatal("session creation with archived agent succeeded")
	}
}

func TestPostgresSessionLifecyclePaginationAndEventQuery(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 5 {
		session := newSession("sesn_page_" + itoa(int64(i)))
		session.CreatedAt = base.Add(time.Duration(i) * time.Second)
		session.UpdatedAt = session.CreatedAt
		if _, err := store.CreateSession(ctx, session, nil); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		ids = append(ids, session.ID)
	}

	first, err := store.ListSessions(ctx, app.ListPage{Limit: 2, Desc: true})
	if err != nil {
		t.Fatalf("list first: %v", err)
	}
	if got, want := sessionIDs(first.Sessions), []string{ids[4], ids[3]}; !equalIDs(got, want) {
		t.Fatalf("first ids = %v, want %v", got, want)
	}
	if first.HasPrev || !first.HasNext {
		t.Fatalf("first cursors = prev:%v next:%v", first.HasPrev, first.HasNext)
	}
	second, err := store.ListSessions(ctx, app.ListPage{
		Limit: 2,
		Desc:  true,
		Boundary: &app.SessionPageBoundary{
			CreatedAt: first.Sessions[1].CreatedAt,
			ID:        first.Sessions[1].ID,
		},
	})
	if err != nil {
		t.Fatalf("list second: %v", err)
	}
	if got, want := sessionIDs(second.Sessions), []string{ids[2], ids[1]}; !equalIDs(got, want) {
		t.Fatalf("second ids = %v, want %v", got, want)
	}
	if !second.HasPrev || !second.HasNext {
		t.Fatalf("second cursors = prev:%v next:%v", second.HasPrev, second.HasNext)
	}

	title := "new title"
	updated, err := store.UpdateSession(ctx, ids[0], domain.SessionUpdate{Title: &title})
	if err != nil || updated.Title != "new title" {
		t.Fatalf("update title = %+v, err=%v", updated, err)
	}
	events, err := store.QueryEvents(ctx, ids[0], app.EventQuery{
		Types: []string{domain.EvSessionUpdated}, Limit: 10,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("updated events = %d, err=%v; want 1", len(events), err)
	}

	archived, err := store.ArchiveSession(ctx, ids[0])
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archive = %+v, err=%v", archived, err)
	}
	active, err := store.ListSessions(ctx, app.ListPage{Limit: 10})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active.Sessions) != 4 {
		t.Fatalf("active sessions = %d, want 4", len(active.Sessions))
	}
	if err := store.DeleteSession(ctx, ids[0]); err != nil {
		t.Fatalf("delete archived session: %v", err)
	}
	if _, err := store.GetSession(ctx, ids[0]); err == nil {
		t.Fatal("deleted session still exists")
	}
}

func TestSessionPaginationUsesPostgresTimestampPrecision(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 29, 10, 0, 0, 123, time.UTC)
	var ids []string
	for i := range 3 {
		session := newSession("sesn_nanos_" + itoa(int64(i)))
		session.CreatedAt = base.Add(time.Duration(i)*time.Microsecond + time.Duration(i)*111*time.Nanosecond)
		session.UpdatedAt = session.CreatedAt
		if _, err := store.CreateSession(ctx, session, nil); err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		ids = append(ids, session.ID)
	}

	first, err := store.ListSessions(ctx, app.ListPage{Limit: 2, Desc: true})
	if err != nil {
		t.Fatalf("list first: %v", err)
	}
	if got, want := sessionIDs(first.Sessions), []string{ids[2], ids[1]}; !equalIDs(got, want) {
		t.Fatalf("first ids = %v, want %v", got, want)
	}
	boundary := first.Sessions[len(first.Sessions)-1]
	if boundary.CreatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("cursor timestamp = %s, want PostgreSQL microsecond precision", boundary.CreatedAt)
	}
	second, err := store.ListSessions(ctx, app.ListPage{
		Limit: 2,
		Desc:  true,
		Boundary: &app.SessionPageBoundary{
			CreatedAt: boundary.CreatedAt,
			ID:        boundary.ID,
		},
	})
	if err != nil {
		t.Fatalf("list second: %v", err)
	}
	if got, want := sessionIDs(second.Sessions), []string{ids[0]}; !equalIDs(got, want) {
		t.Fatalf("second ids = %v, want %v (page boundary duplicated or skipped)", got, want)
	}
}

func TestActiveResourceLocksFenceConcurrentArchival(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	ids := &seqIDGen{}
	clock := fixedClock{}
	agents := app.NewAgentService(NewAgentRepository(store), ids, clock)
	environments := app.NewEnvironmentService(NewEnvironmentRepository(store), ids, clock)

	agent, err := agents.Create(ctx, domain.Agent{
		Name: "coder", Model: domain.Model{ID: "claude-test"},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agentTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agentQueries := pgstore.New(agentTx)
	if _, err := agentQueries.LockActiveAgentVersion(
		ctx,
		pgstore.LockActiveAgentVersionParams{ID: agent.ID, Version: int32(agent.Version)},
	); err != nil {
		t.Fatalf("lock active agent: %v", err)
	}
	archiveCtx, cancelArchive := context.WithTimeout(ctx, 100*time.Millisecond)
	_, archiveErr := agents.Archive(archiveCtx, agent.ID)
	cancelArchive()
	if !errors.Is(archiveErr, context.DeadlineExceeded) {
		_ = agentTx.Rollback(ctx)
		t.Fatalf("agent archival crossed active-version lock: %v", archiveErr)
	}
	if err := agentTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.Archive(ctx, agent.ID); err != nil {
		t.Fatalf("archive agent after releasing lock: %v", err)
	}

	environment, err := environments.Create(ctx, domain.Environment{
		Name: "cloud", ConfigType: "cloud", Config: map[string]any{"type": "cloud"},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	environmentTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	environmentQueries := pgstore.New(environmentTx)
	if _, err := environmentQueries.LockActiveEnvironment(ctx, environment.ID); err != nil {
		t.Fatalf("lock active environment: %v", err)
	}
	archiveCtx, cancelArchive = context.WithTimeout(ctx, 100*time.Millisecond)
	_, archiveErr = environments.Archive(archiveCtx, environment.ID)
	cancelArchive()
	if !errors.Is(archiveErr, context.DeadlineExceeded) {
		_ = environmentTx.Rollback(ctx)
		t.Fatalf("environment archival crossed active-resource lock: %v", archiveErr)
	}
	if err := environmentTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := environments.Archive(ctx, environment.ID); err != nil {
		t.Fatalf("archive environment after releasing lock: %v", err)
	}
}

func TestSessionDeletionFenceBlocksAdmission(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	fenced := newSession("sesn_delete_fenced")
	if _, err := store.CreateSession(ctx, fenced, nil); err != nil {
		t.Fatalf("create fenced session: %v", err)
	}
	if err := store.PrepareSessionDeletion(ctx, fenced.ID); err != nil {
		t.Fatalf("prepare deletion: %v", err)
	}
	if _, err := store.AdmitEvents(ctx, fenced.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "race"}},
		},
	}}); err == nil {
		t.Fatal("admission crossed deletion fence")
	} else if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindConflict {
		t.Fatalf("admission during deletion = %v, want conflict", err)
	}
	if err := store.FinalizeSessionDeletion(ctx, fenced.ID); err != nil {
		t.Fatalf("finalize deletion: %v", err)
	}

	running := newSession("sesn_delete_running")
	if _, err := store.CreateSession(ctx, running, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "running"}},
		},
	}}); err != nil {
		t.Fatalf("create running session: %v", err)
	}
	if err := store.PrepareSessionDeletion(ctx, running.ID); err == nil {
		t.Fatal("prepared deletion of running session")
	} else if de, ok := err.(*domain.DomainError); !ok || de.Kind != domain.KindConflict {
		t.Fatalf("prepare running deletion = %v, want conflict", err)
	}
}

func sessionIDs(sessions []domain.Session) []string {
	out := make([]string, len(sessions))
	for i, session := range sessions {
		out[i] = session.ID
	}
	return out
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
