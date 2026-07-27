package store

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func newRunStoreFixture(t *testing.T) (*DB, *RunStore, domain.Session) {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	ids := domain.NewSeqIDGen()
	if err := NewAgentRepo(db).PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "agent",
		Model: domain.Model{ID: "model"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewEnvironmentRepo(db).Put(ctx, domain.Environment{
		ID: "env_1", Name: "environment", ConfigType: "cloud",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "sesn_1", AgentID: "agent_1", AgentVersion: 1,
		EnvironmentID: "env_1", Status: domain.StatusIdle,
		CreatedAt: now, UpdatedAt: now,
	}
	return db, NewRunStore(db, ids, domain.FixedClock{T: now}), session
}

func TestRunStore_CreateSessionAdmissionIsOneCommit(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()

	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "hello"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Session.Status != domain.StatusRunning {
		t.Fatalf("session status = %s, want running", admission.Session.Status)
	}
	if admission.Run == nil || admission.Run.State != domain.RunQueued {
		t.Fatalf("run = %#v", admission.Run)
	}
	if len(admission.Events) != 2 ||
		admission.Events[0].Type != domain.EvUserMessage ||
		admission.Events[1].Type != domain.EvSessionStatusRunning {
		t.Fatalf("admission events = %#v", admission.Events)
	}

	storedSession, err := NewSessionRepo(db).Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedRun, err := runs.Get(ctx, admission.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewEventStore(db, domain.NewSeqIDGen(),
		domain.FixedClock{T: session.CreatedAt}).History(ctx, session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if storedSession.Status != domain.StatusRunning ||
		storedRun.State != domain.RunQueued ||
		len(history) != 2 {
		t.Fatalf("stored state session=%s run=%s events=%d",
			storedSession.Status, storedRun.State, len(history))
	}
}

func TestRunStore_ClaimAndCompleteClosesRunAtomically(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
	}})
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if len(claim.Triggers) != 1 || claim.Triggers[0].ID != admission.Events[0].ID {
		t.Fatalf("claim triggers = %#v", claim.Triggers)
	}

	completion, err := runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{}}},
		{Type: domain.EvSessionStatusIdle},
	}, domain.StatusIdle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Run.State != domain.RunCompleted ||
		completion.Session.Status != domain.StatusIdle {
		t.Fatalf("completion = %#v", completion)
	}
	history, err := NewEventStore(db, domain.NewSeqIDGen(),
		domain.FixedClock{T: session.CreatedAt}).History(ctx, session.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4", len(history))
	}
	if history[0].ProcessedAt == nil {
		t.Fatal("trigger was not marked processed in completion commit")
	}
	if history[2].Type != domain.EvAgentMessage ||
		history[3].Type != domain.EvSessionStatusIdle {
		t.Fatalf("completion event order = %#v", history)
	}
}

func TestRunStore_QueuesRunsPerSessionInAdmissionOrder(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	first, err := runs.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.AdmissionSeq != 1 || second.Run.AdmissionSeq != 2 {
		t.Fatalf("run sequences = %d, %d", first.Run.AdmissionSeq, second.Run.AdmissionSeq)
	}

	firstClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || firstClaim.Run.ID != first.Run.ID {
		t.Fatalf("first claim = %#v ok=%v err=%v", firstClaim.Run, ok, err)
	}
	if _, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("second claim while first running: ok=%v err=%v", ok, err)
	}
	firstDone, err := runs.Complete(ctx, first.Run.ID,
		[]domain.EventDraft{{Type: domain.EvSessionStatusIdle}},
		domain.StatusIdle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstDone.Session.Status != domain.StatusRunning {
		t.Fatalf("queued successor should keep projection running, got %s", firstDone.Session.Status)
	}
	secondClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || secondClaim.Run.ID != second.Run.ID {
		t.Fatalf("second claim = %#v ok=%v err=%v", secondClaim.Run, ok, err)
	}
}
