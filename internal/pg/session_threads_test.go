package pg

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestSessionPrimaryThreadIsDurableAndOwnsIndependentProjection(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_primary_thread")
	session.AgentSnapshot = domain.Agent{
		ID: "agent_primary", Version: 2, Name: "Coordinator",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-opus-4-8"}),
	}
	session.Usage.InputTokens = 11
	if _, err := store.CreateSession(ctx, session, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "run"}}},
	}}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("thread count = %d, want 1", len(threads))
	}
	primary := threads[0]
	if primary.ID == "" || primary.SessionID != session.ID || primary.ParentThreadID != nil ||
		primary.Status != domain.StatusRunning || primary.Agent.ID != "agent_primary" ||
		primary.Usage.InputTokens != 11 {
		t.Fatalf("primary thread = %+v", primary)
	}
	got, err := store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || got.ID != primary.ID {
		t.Fatalf("get primary = %+v, err=%v", got, err)
	}
	reopened := NewStore(store.pool, &seqIDGen{}, fixedClock{})
	got, err = reopened.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || got.ID != primary.ID {
		t.Fatalf("get primary after store reattachment = %+v, err=%v", got, err)
	}
	if _, err := store.GetSessionThread(ctx, "sesn_other", primary.ID); err == nil {
		t.Fatal("cross-session thread lookup succeeded")
	}

	// Session is an aggregate, not the storage backing for the primary Thread.
	// A future child-only aggregate change must not rewrite primary state on read.
	aggregateOnly := session
	aggregateOnly.Status = domain.StatusIdle
	aggregateOnly.AgentSnapshot.Name = "aggregate-only"
	aggregateOnly.Usage.InputTokens = 99
	aggregateBody, err := json.Marshal(aggregateOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
UPDATE sessions SET status = $2, body = $3 WHERE id = $1`,
		session.ID, aggregateOnly.Status, aggregateBody,
	); err != nil {
		t.Fatal(err)
	}
	independent, err := store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if independent.Status != domain.StatusRunning || independent.Agent.Name != "Coordinator" ||
		independent.Usage.InputTokens != 11 {
		t.Fatalf("primary Thread leaked Session aggregate changes: %+v", independent)
	}
}

func TestPrimaryThreadArchiveAndSessionDeleteShareLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_primary_thread_lifecycle")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 1})
	if err != nil || len(threads) != 1 {
		t.Fatalf("list threads = %+v, err=%v", threads, err)
	}
	if _, err := store.ArchiveSession(ctx, session.ID); err != nil {
		t.Fatalf("archive session: %v", err)
	}
	thread, err := store.GetSessionThread(ctx, session.ID, threads[0].ID)
	if err != nil || thread.ArchivedAt == nil || thread.TerminatedAt == nil ||
		thread.Status != domain.StatusTerminated {
		t.Fatalf("archived primary thread = %+v, err=%v", thread, err)
	}
	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var count int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_threads WHERE session_id = $1`, session.ID,
	).Scan(&count); err != nil || count != 0 {
		t.Fatalf("remaining thread rows = %d, err=%v", count, err)
	}
}

func TestSessionThreadRowsDecodeIndependentChildProjection(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_child_projection")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 10})
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Threads = %+v, err=%v", threads, err)
	}
	primary := threads[0]
	parentID := primary.ID
	child := domain.SessionThread{
		ID: "sthr_child_projection", SessionID: session.ID, ParentThreadID: &parentID,
		Agent: domain.Agent{
			ID: "agent_child", Version: 4, Name: "reviewer",
			Model: domain.NormalizeModel(domain.Model{ID: "claude-sonnet-4-5"}),
		},
		Status: domain.StatusIdle, Usage: domain.TokenUsage{InputTokens: 7},
		CreatedAt: session.CreatedAt.Add(time.Second), UpdatedAt: session.CreatedAt.Add(time.Second),
	}
	body, err := json.Marshal(child)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO session_threads (
    id, session_id, parent_thread_id, kind, status, body, created_at, updated_at
) VALUES ($1, $2, $3, 'child', $4, $5, $6, $7)`,
		child.ID, child.SessionID, child.ParentThreadID, child.Status, body,
		child.CreatedAt, child.UpdatedAt,
	); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.ID != "agent_child" || got.Agent.Version != 4 ||
		got.Usage.InputTokens != 7 || got.ParentThreadID == nil || *got.ParentThreadID != primary.ID {
		t.Fatalf("child projection = %+v", got)
	}
	threads, err = store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 10})
	if err != nil || len(threads) != 2 || threads[0].ID != primary.ID || threads[1].ID != child.ID {
		t.Fatalf("ordered Threads = %+v, err=%v", threads, err)
	}
}

func TestSessionThreadMigrationBackfillsExistingSessions(t *testing.T) {
	store := testStoreAtMigration(t, 20)
	ctx := context.Background()
	session := newSession("sesn_thread_backfill")
	archivedAt := session.CreatedAt.Add(time.Minute)
	session.ArchivedAt = &archivedAt
	session.UpdatedAt = archivedAt
	body, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO sessions (id, status, body, created_at, updated_at, archived_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
		session.ID, session.Status, body, session.CreatedAt, session.UpdatedAt, session.ArchivedAt,
	); err != nil {
		t.Fatalf("insert pre-thread session: %v", err)
	}
	if err := Migrate(ctx, store.pool); err != nil {
		t.Fatalf("migrate to Session Threads: %v", err)
	}
	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 10})
	if err != nil || len(threads) != 1 || threads[0].ID != "sthr_40501844f5ff033680ff228a" ||
		threads[0].Agent.ID != session.AgentSnapshot.ID || threads[0].Status != domain.StatusTerminated ||
		threads[0].ArchivedAt == nil || !threads[0].ArchivedAt.Equal(archivedAt) ||
		threads[0].TerminatedAt == nil || !threads[0].TerminatedAt.Equal(archivedAt) {
		t.Fatalf("backfilled threads = %+v, err=%v", threads, err)
	}
}

func TestSessionResolvedRosterMigrationBackfillsFullAgentSnapshots(t *testing.T) {
	store := testStoreAtMigration(t, 22)
	ctx := context.Background()
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	peer := domain.Agent{
		ID: "agent_roster_peer", Version: 2, Name: "Reviewer",
		Model:  domain.NormalizeModel(domain.Model{ID: "claude-test"}),
		System: stringPointer("peer-system"), CreatedAt: base, UpdatedAt: base,
	}
	coordinator := domain.Agent{
		ID: "agent_roster_coordinator", Version: 4, Name: "Coordinator",
		Model:  domain.NormalizeModel(domain.Model{ID: "claude-test"}),
		System: stringPointer("coordinator-system"), CreatedAt: base, UpdatedAt: base,
		Multiagent: &domain.Multiagent{Type: "coordinator", Agents: []domain.AgentReference{
			{Type: "agent", ID: peer.ID, Version: peer.Version},
			{Type: "agent", ID: "agent_roster_coordinator", Version: 4},
		}},
	}
	for _, agent := range []domain.Agent{peer, coordinator} {
		body, err := json.Marshal(agent)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `
INSERT INTO agents (id, version, name, body, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			agent.ID, agent.Version, agent.Name, body, base, base,
		); err != nil {
			t.Fatal(err)
		}
	}
	session := newSession("sesn_roster_backfill")
	session.AgentID = coordinator.ID
	session.AgentVersion = coordinator.Version
	session.AgentSnapshot = coordinator
	session.AgentSnapshot.System = stringPointer("session-override")
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var legacyBody map[string]any
	if err := json.Unmarshal(encoded, &legacyBody); err != nil {
		t.Fatal(err)
	}
	delete(legacyBody, "MultiagentRoster")
	encoded, err = json.Marshal(legacyBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO sessions (
    id, status, body, created_at, updated_at, agent_id, agent_version
) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		session.ID, session.Status, encoded, session.CreatedAt, session.UpdatedAt,
		session.AgentID, session.AgentVersion,
	); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, store.pool); err != nil {
		t.Fatalf("migrate resolved roster: %v", err)
	}
	migrated, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.MultiagentRoster) != 2 ||
		migrated.MultiagentRoster[0].ID != peer.ID ||
		migrated.MultiagentRoster[0].Version != peer.Version ||
		migrated.MultiagentRoster[1].ID != coordinator.ID ||
		migrated.MultiagentRoster[1].System == nil ||
		*migrated.MultiagentRoster[1].System != "session-override" {
		t.Fatalf("migrated roster = %+v", migrated.MultiagentRoster)
	}
	for _, member := range migrated.MultiagentRoster {
		if member.Multiagent != nil {
			t.Fatalf("migrated member retained nested topology: %+v", member)
		}
	}
}

func stringPointer(value string) *string { return &value }
