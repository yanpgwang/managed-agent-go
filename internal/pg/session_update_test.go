package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func updatableStoreSession(id string) domain.Session {
	session := newSession(id)
	session.Title = "before"
	session.Metadata = map[string]any{"keep": "yes", "drop": "later"}
	session.AgentSnapshot = domain.Agent{
		ID: "agent_1", Version: 1, Name: "coder",
		Model: domain.Model{ID: "claude-test"},
		Tools: []any{map[string]any{"type": domain.BuiltinToolsetType}},
	}
	return session
}

func TestPostgresUpdateSessionCommitsPatchAndEventTogether(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := updatableStoreSession("sesn_update")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	threads, err := store.ListSessionThreads(ctx, session.ID, app.SessionThreadListQuery{Limit: 1})
	if err != nil || len(threads) != 1 {
		t.Fatalf("list primary Thread = %+v, err=%v", threads, err)
	}
	primaryID := threads[0].ID

	title := "after"
	updated, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		Title:    &title,
		Metadata: map[string]any{"keep": "still", "drop": nil, "added": "new"},
	})
	if err != nil {
		t.Fatalf("update session: %v", err)
	}
	if updated.Title != "after" {
		t.Fatalf("title = %q", updated.Title)
	}
	reloaded, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.Metadata["keep"] != "still" || reloaded.Metadata["added"] != "new" {
		t.Fatalf("persisted metadata = %#v", reloaded.Metadata)
	}
	if _, present := reloaded.Metadata["drop"]; present {
		t.Fatalf("null did not delete the key: %#v", reloaded.Metadata)
	}
	primary, err := store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(primary.Agent.Tools) != 1 {
		t.Fatalf("Session-only update changed primary Agent: %#v", primary.Agent.Tools)
	}

	// A tools replacement is session-local and reaches the durable projection
	// the turn loop reads.
	tools := []any{}
	if _, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		AgentTools: &tools,
	}); err != nil {
		t.Fatalf("replace tools: %v", err)
	}
	reloaded, err = store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload after tools update: %v", err)
	}
	if len(reloaded.AgentSnapshot.Tools) != 0 {
		t.Fatalf("persisted tools = %#v", reloaded.AgentSnapshot.Tools)
	}
	if reloaded.AgentSnapshot.ID != "agent_1" || reloaded.AgentSnapshot.Version != 1 {
		t.Fatalf("session update renumbered the snapshot: %#v", reloaded.AgentSnapshot)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(primary.Agent.Tools) != 0 || primary.Agent.ID != "agent_1" || primary.Agent.Version != 1 {
		t.Fatalf("primary Agent projection did not follow Agent update: %#v", primary.Agent)
	}

	// A request that changes nothing emits no event.
	if _, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		Title: &title,
	}); err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	events, err := store.QueryEvents(ctx, session.ID, app.EventQuery{
		Types: []string{domain.EvSessionUpdated}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("session.updated count = %d, want 2", len(events))
	}
	first, second := events[0].Payload, events[1].Payload
	if first["title"] != "after" || first["metadata"] == nil {
		t.Fatalf("first payload = %#v", first)
	}
	if _, present := first["agent"]; present {
		t.Fatalf("first payload carries an unchanged agent: %#v", first)
	}
	if _, present := second["agent"]; !present {
		t.Fatalf("second payload is missing the agent: %#v", second)
	}
	if _, present := second["title"]; present {
		t.Fatalf("second payload carries an unchanged title: %#v", second)
	}
	agent, _ := second["agent"].(map[string]any)
	if agent["type"] != "agent" || agent["id"] != "agent_1" {
		t.Fatalf("second payload agent = %#v", second["agent"])
	}
}

func TestPostgresUpdateSessionRequiresIdleForAgentChanges(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := updatableStoreSession("sesn_running")
	session.Status = domain.StatusRunning
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	tools := []any{}
	_, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{AgentTools: &tools})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict {
		t.Fatalf("agent update while running = %v, want a conflict", err)
	}

	// Nothing was written: no projection change and no event.
	reloaded, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.AgentSnapshot.Tools) != 1 {
		t.Fatalf("rejected update still changed the snapshot: %#v", reloaded.AgentSnapshot.Tools)
	}
	events, err := store.QueryEvents(ctx, session.ID, app.EventQuery{
		Types: []string{domain.EvSessionUpdated}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("rejected update emitted %d events", len(events))
	}

	// Title and metadata are not gated on status.
	title := "mid-turn"
	if _, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		Title: &title,
	}); err != nil {
		t.Fatalf("title update while running: %v", err)
	}
}
