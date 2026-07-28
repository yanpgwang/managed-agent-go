package store

import (
	"context"
	"path/filepath"
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
	if len(admission.Runs) != 1 || admission.Runs[0].State != domain.RunQueued {
		t.Fatalf("runs = %#v", admission.Runs)
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
	storedRun, err := runs.Get(ctx, admission.Runs[0].ID)
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
	}, domain.StatusIdle, nil, nil)
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
	if len(first.Runs) != 1 || len(second.Runs) != 1 {
		t.Fatalf("run counts = %d, %d, want 1, 1", len(first.Runs), len(second.Runs))
	}
	if first.Runs[0].AdmissionSeq != 1 || second.Runs[0].AdmissionSeq != 2 {
		t.Fatalf("run sequences = %d, %d", first.Runs[0].AdmissionSeq, second.Runs[0].AdmissionSeq)
	}

	firstClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || firstClaim.Run.ID != first.Runs[0].ID {
		t.Fatalf("first claim = %#v ok=%v err=%v", firstClaim.Run, ok, err)
	}
	if _, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("second claim while first running: ok=%v err=%v", ok, err)
	}
	firstDone, err := runs.Complete(ctx, first.Runs[0].ID,
		[]domain.EventDraft{{Type: domain.EvSessionStatusIdle}},
		domain.StatusIdle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstDone.Session.Status != domain.StatusRunning {
		t.Fatalf("queued successor should keep projection running, got %s", firstDone.Session.Status)
	}
	secondClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || secondClaim.Run.ID != second.Runs[0].ID {
		t.Fatalf("second claim = %#v ok=%v err=%v", secondClaim.Run, ok, err)
	}
}

// TestRunStore_AdmitBatchCreatesOneRunPerTrigger proves a single atomic
// admission of multiple triggers yields one durable queued run per trigger, in
// admission order, each holding exactly one trigger id — never one grouped run.
func TestRunStore_AdmitBatchCreatesOneRunPerTrigger(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "first"},
		}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "second"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 2 {
		t.Fatalf("runs = %d, want 2 (one per trigger)", len(admission.Runs))
	}
	// The two user events are events[0] and events[1]; events[2] is the status
	// event emitted by the same admission.
	wantTriggers := []string{admission.Events[0].ID, admission.Events[1].ID}
	for i, run := range admission.Runs {
		if run.AdmissionSeq != int64(i+1) {
			t.Fatalf("run %d admission_seq = %d, want %d", i, run.AdmissionSeq, i+1)
		}
		if len(run.TriggerEventIDs) != 1 || run.TriggerEventIDs[0] != wantTriggers[i] {
			t.Fatalf("run %d triggers = %#v, want [%s]", i, run.TriggerEventIDs, wantTriggers[i])
		}
		if run.State != domain.RunQueued {
			t.Fatalf("run %d state = %s, want queued", i, run.State)
		}
	}
}

// TestRunStore_CompletionBeforeNextClaimObservesOutput proves that after run N
// commits its agent output and marks its trigger processed, run N+1 becomes
// claimable and projects history that includes run N's committed output.
func TestRunStore_CompletionBeforeNextClaimObservesOutput(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "first"},
		}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "second"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(admission.Runs))
	}

	firstClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	// Run N+1 is not claimable while run N is running.
	if _, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("second claim before first completes: ok=%v err=%v", ok, err)
	}

	// Run N commits agent output and marks its trigger processed.
	if _, err := runs.Complete(ctx, firstClaim.Run.ID, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "answer-one"},
		}}},
		{Type: domain.EvSessionStatusIdle},
	}, domain.StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}

	// The first trigger is now processed.
	es := NewEventStore(db, domain.NewSeqIDGen(), domain.FixedClock{T: session.CreatedAt})
	history, err := es.History(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].ProcessedAt == nil {
		t.Fatal("first trigger not marked processed after run N completed")
	}

	// Run N+1 is now claimable, and history projected for it includes the agent
	// output committed by run N.
	secondClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || secondClaim.Run.ID != admission.Runs[1].ID {
		t.Fatalf("second claim = %#v ok=%v err=%v", secondClaim.Run, ok, err)
	}
	history, err = es.History(ctx, session.ID, 0, 100)
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
		t.Fatal("history for run N+1 does not include run N's committed agent output")
	}
}

// TestRunStore_TerminatedSessionNeverClaims proves a terminated session is
// final: ClaimNext must not claim leftover queued work nor reopen the session.
func TestRunStore_TerminatedSessionNeverClaims(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "first"},
		}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "second"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(admission.Runs))
	}

	firstClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	// Run N terminates the session even though run N+1 is still queued.
	msg := "boom"
	if _, err := runs.Complete(ctx, firstClaim.Run.ID, []domain.EventDraft{
		{Type: domain.EvSessionError, Payload: map[string]any{"error": map[string]any{
			"type": "api_error", "message": msg,
		}}},
		{Type: domain.EvSessionStatusTerminated},
	}, domain.StatusTerminated, &msg, nil); err != nil {
		t.Fatal(err)
	}

	// The leftover queued run must never be claimed, and the session must stay
	// terminated.
	if claim, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("claim after termination: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}
	stored, err := NewSessionRepo(db).Get(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusTerminated {
		t.Fatalf("session status = %s, want terminated", stored.Status)
	}
}

// TestRunStore_ModelHistorySurvivesReopenInCausalOrder proves the durable output
// association: after run A completes (persisting its committed output event ids
// in the same transaction that closes it), the process can close and reopen a
// file-backed database, and RunStore.ModelHistory for run B still reconstructs
// trigger(A), output(A), trigger(B) in causal order — reading the persisted
// column, not any in-memory state.
func TestRunStore_ModelHistorySurvivesReopenInCausalOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "causal.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: now}
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
	runs := NewRunStore(db, ids, clk)

	// Admit A and B in one batch (two runs), claim and complete A with a distinct
	// agent output. Then close the database.
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "A"},
		}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "B"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(admission.Runs))
	}
	runBID := admission.Runs[1].ID

	claimA, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim A: ok=%v err=%v", ok, err)
	}
	if _, err := runs.Complete(ctx, claimA.Run.ID, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "reply-A"},
		}}},
		{Type: domain.EvSessionStatusIdle},
	}, domain.StatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen the file-backed database in a fresh RunStore and reconstruct history
	// for run B purely from persisted state.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedRuns := NewRunStore(reopened, domain.NewSeqIDGen(), clk)

	runB, err := reopenedRuns.Get(ctx, runBID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopenedRuns.ModelHistory(ctx, runB, 10000)
	if err != nil {
		t.Fatal(err)
	}

	// Expect exactly trigger(A), output(A) [agent.message reply-A, status_idle],
	// trigger(B), in causal order.
	msgs := domain.ProjectMessages(history)
	if len(msgs) != 3 {
		t.Fatalf("projected messages = %d, want 3: %#v", len(msgs), msgs)
	}
	if msgs[0].Role != domain.RoleUser || msgs[0].Content[0].Text != "A" {
		t.Fatalf("messages[0] = %#v, want user 'A'", msgs[0])
	}
	if msgs[1].Role != domain.RoleAssistant || msgs[1].Content[0].Text != "reply-A" {
		t.Fatalf("messages[1] = %#v, want assistant 'reply-A'", msgs[1])
	}
	if msgs[2].Role != domain.RoleUser || msgs[2].Content[0].Text != "B" {
		t.Fatalf("messages[2] = %#v, want user 'B'", msgs[2])
	}

	// And prove the trigger(A), output(A), trigger(B) event ordering directly on
	// the raw reconstructed events, independent of projection folding.
	if len(history) < 4 {
		t.Fatalf("reconstructed history has %d events, want >=4", len(history))
	}
	if history[0].ID != claimA.Run.TriggerEventIDs[0] {
		t.Fatalf("history[0] is not trigger(A): %#v", history[0])
	}
	if history[len(history)-1].ID != runB.TriggerEventIDs[0] {
		t.Fatalf("last history event is not trigger(B): %#v", history[len(history)-1])
	}
	var sawAgentBetween bool
	for _, e := range history[1 : len(history)-1] {
		if e.Type == domain.EvAgentMessage {
			sawAgentBetween = true
		}
	}
	if !sawAgentBetween {
		t.Fatal("output(A) agent.message did not survive reopen between trigger(A) and trigger(B)")
	}
}
