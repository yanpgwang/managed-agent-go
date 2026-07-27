package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func TestRecover_RequeuesAndCompletesInterruptedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := domain.NewRandomIDGen()
	clk := domain.FixedClock{T: time.Unix(1, 0).UTC()}
	runs := store.NewRunStore(db, ids, clk)
	ctx := context.Background()

	sr := store.NewSessionRepo(db)
	_ = sr.Put(ctx, domain.Session{ID: "sesn_1", Status: domain.StatusIdle,
		CreatedAt: clk.T, UpdatedAt: clk.T})
	admission, err := runs.Admit(ctx, "sesn_1", []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "recover"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 1 {
		t.Fatalf("admission runs = %d, want 1", len(admission.Runs))
	}
	if _, ok, err := runs.ClaimNext(ctx, "sesn_1"); err != nil || !ok {
		t.Fatalf("claim before simulated crash: ok=%v err=%v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recoveryIDs := domain.NewRandomIDGen()
	recoveryEvents := NewEventService(
		store.NewEventStore(reopened, recoveryIDs, clk), NewHub(64),
	)
	recoveryRuns := store.NewRunStore(reopened, recoveryIDs, clk)
	ss := NewSessionService(
		store.NewSessionRepo(reopened), store.NewAgentRepo(reopened),
		store.NewEnvironmentRepo(reopened), recoveryEvents, recoveryRuns,
		agentruntime.NewFake(), sandbox.NewLocalProvider(), recoveryIDs, clk,
	)
	if err := ss.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got := pollUntilStatus(t, ss, "sesn_1", domain.StatusIdle)
	if got.Status != domain.StatusIdle {
		t.Fatalf("expected recovered idle, got %s", got.Status)
	}
	run, err := recoveryRuns.Get(ctx, admission.Runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunCompleted {
		t.Fatalf("recovered run state = %s, want completed", run.State)
	}
	history, err := recoveryEvents.History(ctx, "sesn_1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawAgentMessage bool
	for _, event := range history {
		if event.Type == domain.EvAgentMessage {
			sawAgentMessage = true
		}
	}
	if !sawAgentMessage {
		t.Fatal("recovered run did not emit agent.message")
	}
}
