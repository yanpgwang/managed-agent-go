package pg

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestSessionPrimaryThreadIsDurableAndMirrorsSessionProjection(t *testing.T) {
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

func TestSessionThreadMigrationBackfillsExistingSessions(t *testing.T) {
	store := testStoreAtMigration(t, 20)
	ctx := context.Background()
	session := newSession("sesn_thread_backfill")
	body, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO sessions (id, status, body, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)`,
		session.ID, session.Status, body, session.CreatedAt, session.UpdatedAt,
	); err != nil {
		t.Fatalf("insert pre-thread session: %v", err)
	}
	if err := Migrate(ctx, store.pool); err != nil {
		t.Fatalf("migrate to Session Threads: %v", err)
	}
	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 10})
	if err != nil || len(threads) != 1 || threads[0].ID != "sthr_40501844f5ff033680ff228a" {
		t.Fatalf("backfilled threads = %+v, err=%v", threads, err)
	}
}
