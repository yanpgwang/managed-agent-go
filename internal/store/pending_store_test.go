package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// parkRun claims the session's next run and completes it as a parked
// custom-tool run: it commits an agent.custom_tool_use action event plus the
// terminal status_idle{requires_action}, and persists a durable pending action
// for that action event in the same completion transaction. Returns the
// committed action event id (the custom_tool_use_id a resolution must reference).
func parkRun(t *testing.T, runs *RunStore, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	claim, ok, err := runs.ClaimNext(ctx, sessionID)
	if err != nil || !ok {
		t.Fatalf("park: claim ok=%v err=%v", ok, err)
	}
	actionID := "sevt_action_" + claim.Run.ID
	completion, err := runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: actionID, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{"name": "get_metrics", "input": map[string]any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{actionID})
	if err != nil {
		t.Fatalf("park: complete: %v", err)
	}
	if completion.Session.Status != domain.StatusIdle {
		t.Fatalf("park: session status = %s, want idle", completion.Session.Status)
	}
	return actionID
}

// TestPending_ParkPersistsPendingActionInCompletionTx proves the pending action
// and the run completion are one transaction: after Complete returns, the
// session is idle, the run is completed, the action event exists, and a durable
// unresolved pending action of the derived kind references it.
func TestPending_ParkPersistsPendingActionInCompletionTx(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	actionID := parkRun(t, runs, session.ID)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pending, err := unresolvedPendingActions(ctx, tx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("unresolved pending = %d, want 1", len(pending))
	}
	if pending[0].actionEventID != actionID {
		t.Fatalf("pending action event id = %s, want %s", pending[0].actionEventID, actionID)
	}
	if pending[0].kind != domain.PendingCustomToolResult {
		t.Fatalf("pending kind = %s, want custom_tool_result", pending[0].kind)
	}
	if pending[0].resolvingEventID != nil {
		t.Fatalf("pending resolving id = %v, want nil (open)", *pending[0].resolvingEventID)
	}
}

// TestPending_QueuedRunBeforeParkStaysGated proves an ordinary run queued BEFORE
// a park remains unclaimable while the pending action is unresolved, even though
// it was admitted first.
func TestPending_QueuedRunBeforeParkStaysGated(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	// Two ordinary user messages admitted up front: run 1 will park, run 2 is the
	// earlier-queued ordinary work that must be gated.
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "one"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "two"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(admission.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(admission.Runs))
	}
	// Run 1 parks.
	parkRun(t, runs, session.ID)

	// Run 2 was queued before the park; it must not be claimable while the pending
	// action is unresolved.
	if claim, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("gated queued run was claimed: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}
}

// TestPending_MatchingResolutionBypassesEarlierQueued proves a matching
// resolution trigger admitted LATER bypasses the earlier ordinary queued run:
// the resume run is claimed even though a lower-admission-seq ordinary run is
// still queued and gated.
func TestPending_MatchingResolutionBypassesEarlierQueued(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "one"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "two"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	earlierQueuedRunID := admission.Runs[1].ID
	actionID := parkRun(t, runs, session.ID)

	// Admit the matching resolution. This creates a new (highest admission_seq)
	// run whose trigger resolves the pending action.
	resume, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}})
	if err != nil {
		t.Fatalf("admit resolution: %v", err)
	}
	if len(resume.Runs) != 1 {
		t.Fatalf("resume runs = %d, want 1", len(resume.Runs))
	}
	resumeRunID := resume.Runs[0].ID

	// The claim must be the resume run, NOT the earlier ordinary queued run.
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim resume: ok=%v err=%v", ok, err)
	}
	if claim.Run.ID != resumeRunID {
		t.Fatalf("claimed run = %s, want resume run %s (earlier queued %s must stay gated)",
			claim.Run.ID, resumeRunID, earlierQueuedRunID)
	}
	if claim.Run.AdmissionSeq <= admission.Runs[1].AdmissionSeq {
		t.Fatalf("resume admission_seq %d should exceed earlier queued %d",
			claim.Run.AdmissionSeq, admission.Runs[1].AdmissionSeq)
	}
}

// TestPending_ResumeCompletionReleasesEarlierQueued proves the gate clears only
// after the resume run closes successfully: the previously blocked ordinary run
// becomes claimable, and the session reopens to running for it.
func TestPending_ResumeCompletionReleasesEarlierQueued(t *testing.T) {
	_, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "one"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "two"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	earlierQueuedRunID := admission.Runs[1].ID
	actionID := parkRun(t, runs, session.ID)
	resume, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resumeClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || resumeClaim.Run.ID != resume.Runs[0].ID {
		t.Fatalf("resume claim = %#v ok=%v err=%v", resumeClaim.Run, ok, err)
	}
	// Before the resume closes, the earlier queued ordinary run is still gated
	// (also blocked by the one-running rule).
	if _, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("earlier queued claimable before resume closed: ok=%v err=%v", ok, err)
	}

	// Resume closes successfully; the pending action resolves in this same
	// transaction, so the session must reopen to running for the still-queued run.
	done, err := runs.Complete(ctx, resumeClaim.Run.ID, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Session.Status != domain.StatusRunning {
		t.Fatalf("session after resume = %s, want running (gate cleared, queued work remains)", done.Session.Status)
	}
	// The previously blocked ordinary run is now claimable.
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || claim.Run.ID != earlierQueuedRunID {
		t.Fatalf("post-resume claim = %#v ok=%v err=%v, want earlier queued %s", claim.Run, ok, err, earlierQueuedRunID)
	}
}

// TestPending_AdmissionRejectsBadResolutions proves unknown, wrong-kind,
// duplicate, and already-resolved references fail atomically at admission
// without creating any runnable work.
func TestPending_AdmissionRejectsBadResolutions(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	actionID := parkRun(t, runs, session.ID)

	// Unknown reference: no pending action exists for this id.
	if _, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": "sevt_nope", "content": []any{}},
	}}); !isValidation(err) {
		t.Fatalf("unknown reference err = %v, want validation", err)
	}

	// Wrong kind: a tool_confirmation cannot resolve a custom_tool_result park.
	if _, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserToolConfirmation,
		Payload: map[string]any{"tool_use_id": actionID, "result": "allow"},
	}}); !isValidation(err) {
		t.Fatalf("wrong-kind reference err = %v, want validation", err)
	}

	// First valid resolution claims the pending action.
	if _, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}}); err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	// Duplicate resolution for the same still-open action is a conflict.
	if _, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}}); !isConflict(err) {
		t.Fatalf("duplicate resolution err = %v, want conflict", err)
	}

	// A rejected resolution must not have created a runnable run: only the one
	// valid resume run exists beyond the original message run.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_runs WHERE session_id=?`, session.ID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	// original message run (parked) + exactly one valid resume run = 2.
	if total != 2 {
		t.Fatalf("total runs = %d, want 2 (rejected resolutions created no runs)", total)
	}
}

// TestPending_WrongSessionReferenceRejected proves a resolution referencing a
// pending action that belongs to a different session is rejected atomically.
func TestPending_WrongSessionReferenceRejected(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	actionID := parkRun(t, runs, session.ID)

	// A second session with its own message run, never parked.
	other := session
	other.ID = "sesn_other"
	if err := NewSessionRepo(db).CreateIfDependenciesActive(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Admit(ctx, other.ID, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	// Referencing session A's parked action from session B must be rejected: the
	// pending action does not exist in session B.
	if _, err := runs.Admit(ctx, other.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}}); !isValidation(err) {
		t.Fatalf("cross-session reference err = %v, want validation", err)
	}
}

// TestPending_GateSurvivesReopen proves the durable pending gate survives a
// file-backed database close/reopen: after reopening, the earlier queued run is
// still gated, and a matching resolution admitted post-reopen resumes.
func TestPending_GateSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1, 0).UTC()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: now}
	if err := NewAgentRepo(db).PutVersion(ctx, domain.Agent{
		ID: "agent_1", Version: 1, Name: "agent", Model: domain.Model{ID: "model"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewEnvironmentRepo(db).Put(ctx, domain.Environment{
		ID: "env_1", Name: "environment", ConfigType: "cloud", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "sesn_1", AgentID: "agent_1", AgentVersion: 1,
		EnvironmentID: "env_1", Status: domain.StatusIdle, CreatedAt: now, UpdatedAt: now,
	}
	runs := NewRunStore(db, ids, clk)
	admission, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "one"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "two"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	earlierQueuedRunID := admission.Runs[1].ID
	actionID := parkRun(t, runs, session.ID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedRuns := NewRunStore(reopened, ids, clk)

	// Gate survived: the earlier queued run is still not claimable.
	if claim, ok, err := reopenedRuns.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("gated run claimable after reopen: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}

	// A matching resolution admitted after reopen resumes.
	resume, err := reopenedRuns.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}})
	if err != nil {
		t.Fatalf("admit resolution after reopen: %v", err)
	}
	claim, ok, err := reopenedRuns.ClaimNext(ctx, session.ID)
	if err != nil || !ok || claim.Run.ID != resume.Runs[0].ID {
		t.Fatalf("resume claim after reopen = %#v ok=%v err=%v", claim.Run, ok, err)
	}
	if claim.Run.ID == earlierQueuedRunID {
		t.Fatal("earlier queued run was claimed instead of the resume run after reopen")
	}
}

// TestPending_DeleteRemovesPendingState proves session deletion removes durable
// pending-action rows with the session.
func TestPending_DeleteRemovesPendingState(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	parkRun(t, runs, session.ID)

	if err := NewSessionRepo(db).Delete(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_actions WHERE session_id=?`, session.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending_actions after delete = %d, want 0", count)
	}
}

// TestPending_MultiActionParkGatesAllButNoAggregateResume documents the explicit
// multi-action limitation: a run that parks with multiple action events persists
// and gates ALL of them, but this milestone does not implement an aggregated
// multi-action resume protocol. Resolving one action leaves the others
// unresolved, so the gate stays closed until every action is individually
// resolved.
func TestPending_MultiActionParkGatesAllButNoAggregateResume(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// Park with TWO custom-tool action events in one completion.
	a1 := "sevt_a1_" + claim.Run.ID
	a2 := "sevt_a2_" + claim.Run.ID
	if _, err := runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: a1, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{"name": "t1", "input": map[string]any{}}},
		{ID: a2, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{"name": "t2", "input": map[string]any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{a1, a2}); err != nil {
		t.Fatal(err)
	}
	// Both are gated.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := unresolvedPendingActions(ctx, tx, session.ID)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("unresolved pending = %d, want 2 (both action events gated)", len(pending))
	}

	// Resolve only the first action. The second is still unresolved, so the gate
	// remains closed for ordinary work — the resume for a1 is the only claimable
	// run, and after it closes a2 still gates.
	resume, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": a1, "content": []any{}},
	}})
	if err != nil {
		t.Fatalf("admit a1 resolution: %v", err)
	}
	rc, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || rc.Run.ID != resume.Runs[0].ID {
		t.Fatalf("a1 resume claim = %#v ok=%v err=%v", rc.Run, ok, err)
	}
	done, err := runs.Complete(ctx, rc.Run.ID, []domain.EventDraft{
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// a2 still gates: the session must stay idle, not reopen to running, and no
	// ordinary run is claimable.
	if done.Session.Status != domain.StatusIdle {
		t.Fatalf("session after partial resume = %s, want idle (a2 still gates)", done.Session.Status)
	}
	if claim, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("claim while a2 unresolved: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}
}

// TestPending_OrdinaryWorkAdmittedDuringParkStaysIdleAndGated proves the
// state-machine fix: with no ordinary work queued behind a park, admitting a new
// ordinary user.message while the pending action is still OPEN creates its queued
// run but leaves the session idle, emits NO session.status_running, and the run
// is not claimable. Admitting the matching resolution afterward reopens the
// session to running and its resume run is claimed before the ordinary queued
// run; a normal release then lets the ordinary run proceed.
func TestPending_OrdinaryWorkAdmittedDuringParkStaysIdleAndGated(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	// A single trigger: run 1 parks. Nothing else is queued behind it.
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	actionID := parkRun(t, runs, session.ID)

	// Admit an ORDINARY user.message (not the matching resolution) while the park
	// is still open. It must be durably queued but leave the session idle.
	admission, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "meanwhile"}}},
	}})
	if err != nil {
		t.Fatalf("admit ordinary message during park: %v", err)
	}
	if len(admission.Runs) != 1 {
		t.Fatalf("ordinary admission runs = %d, want 1 (durably queued)", len(admission.Runs))
	}
	ordinaryRunID := admission.Runs[0].ID
	// The session projection must remain idle...
	if admission.Session.Status != domain.StatusIdle {
		t.Fatalf("session after gated admission = %s, want idle", admission.Session.Status)
	}
	// ...and NO session.status_running may have been emitted by this admission.
	for _, ev := range admission.Events {
		if ev.Type == domain.EvSessionStatusRunning {
			t.Fatalf("gated admission emitted %s, want none", domain.EvSessionStatusRunning)
		}
	}
	// The stored session must also be idle.
	stored, err := getSessionTx(ctx, db, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.StatusIdle {
		t.Fatalf("stored session = %s, want idle (no status_running committed)", stored.Status)
	}

	// The gated ordinary run must not be claimable while the park is open.
	if claim, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("gated ordinary run was claimed: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}

	// Admit the matching resolution: this reopens the session to running and its
	// resume run must be claimed before the earlier ordinary queued run.
	resume, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": actionID, "content": []any{}},
	}})
	if err != nil {
		t.Fatalf("admit resolution: %v", err)
	}
	if resume.Session.Status != domain.StatusRunning {
		t.Fatalf("session after resolution admission = %s, want running", resume.Session.Status)
	}
	sawRunning := false
	for _, ev := range resume.Events {
		if ev.Type == domain.EvSessionStatusRunning {
			sawRunning = true
		}
	}
	if !sawRunning {
		t.Fatalf("resolution admission did not emit %s", domain.EvSessionStatusRunning)
	}

	resumeClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || resumeClaim.Run.ID != resume.Runs[0].ID {
		t.Fatalf("resume claim = %#v ok=%v err=%v, want resume run %s (ordinary %s must stay gated)",
			resumeClaim.Run, ok, err, resume.Runs[0].ID, ordinaryRunID)
	}

	// Close the resume run normally; the gate clears, so the ordinary queued run
	// becomes claimable and the session reopens to running for it.
	done, err := runs.Complete(ctx, resumeClaim.Run.ID, []domain.EventDraft{
		{Type: domain.EvAgentMessage, Payload: map[string]any{"content": []any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Session.Status != domain.StatusRunning {
		t.Fatalf("session after resume = %s, want running (gate cleared, ordinary run queued)", done.Session.Status)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || claim.Run.ID != ordinaryRunID {
		t.Fatalf("post-resume claim = %#v ok=%v err=%v, want ordinary run %s", claim.Run, ok, err, ordinaryRunID)
	}
}

// TestPending_InterruptWhileParkedStaysGated proves the honest gate semantics: a
// user.interrupt admitted while an unresolved pending action gates the session is
// enqueued like any ordinary run but is NOT claimable — it is not the matching
// resolution, so the pending-action claim gate blocks it. It stays queued while the
// park is open; only the matching resolution bypasses the gate. This documents that
// interrupting a parked session is unsupported/unproven in this milestone: a
// mid-park interrupt does not resolve the blocking action, and the session's
// requires_action projection must not be replaced by an idle/end_turn.
func TestPending_InterruptWhileParkedStaysGated(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	// Run 1 parks, leaving an unresolved pending action that gates the session.
	parkRun(t, runs, session.ID)

	// Admit a user.interrupt while the park is open. It is durably queued but does
	// not resolve the pending action.
	admission, err := runs.Admit(ctx, session.ID, []domain.EventDraft{
		{Type: domain.EvUserInterrupt, Payload: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("admit interrupt during park: %v", err)
	}
	if len(admission.Runs) != 1 {
		t.Fatalf("interrupt admission runs = %d, want 1 (durably queued)", len(admission.Runs))
	}
	// The gated admission must leave the session idle and emit no status_running.
	if admission.Session.Status != domain.StatusIdle {
		t.Fatalf("session after gated interrupt admission = %s, want idle", admission.Session.Status)
	}
	for _, ev := range admission.Events {
		if ev.Type == domain.EvSessionStatusRunning {
			t.Fatalf("gated interrupt admission emitted %s, want none", domain.EvSessionStatusRunning)
		}
	}

	// The interrupt run is NOT claimable while the pending action is unresolved: the
	// gate blocks it exactly like any other non-resolution run.
	if claim, ok, err := runs.ClaimNext(ctx, session.ID); err != nil || ok {
		t.Fatalf("gated interrupt run was claimed: claim=%#v ok=%v err=%v", claim.Run, ok, err)
	}

	// The pending action remains unresolved, confirming the interrupt did not
	// disturb the gate or replace the requires_action projection.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pending, err := unresolvedPendingActions(ctx, tx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("unresolved pending = %d, want 1 (interrupt did not resolve the park)", len(pending))
	}
}

func isValidation(err error) bool {
	var de *domain.DomainError
	return errors.As(err, &de) && de.Kind == domain.KindValidation
}

func isConflict(err error) bool {
	var de *domain.DomainError
	return errors.As(err, &de) && de.Kind == domain.KindConflict
}

// TestPending_CompleteRejectsOldSameSessionActionAtomically proves Complete only
// accepts a pending action id that names an action event committed by THIS
// Complete call. An action event id from an EARLIER run in the same session is
// rejected, and the whole completion transaction rolls back: the current run
// stays running, its drafts are not committed, its trigger stays unprocessed,
// and no new pending action is created.
func TestPending_CompleteRejectsOldSameSessionActionAtomically(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	// Two ordinary triggers: run 1 parks (producing an old action event id), run 2
	// is the run whose Complete we will feed the STALE id.
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "one"}}}},
		{Type: domain.EvUserMessage, Payload: map[string]any{"content": []any{map[string]any{"type": "text", "text": "two"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	oldActionID := parkRun(t, runs, session.ID)
	// Resolve the park so ordinary queued work becomes claimable again.
	resume, err := runs.Admit(ctx, session.ID, []domain.EventDraft{{
		Type:    domain.EvUserCustomToolResult,
		Payload: map[string]any{"custom_tool_use_id": oldActionID, "content": []any{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resumeClaim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok || resumeClaim.Run.ID != resume.Runs[0].ID {
		t.Fatalf("resume claim = %#v ok=%v err=%v", resumeClaim.Run, ok, err)
	}
	if _, err := runs.Complete(ctx, resumeClaim.Run.ID, []domain.EventDraft{
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}}},
	}, domain.StatusIdle, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Claim the earlier ordinary run and try to Complete it while parking on the
	// STALE action event id from run 1. It must be rejected.
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim ordinary run: ok=%v err=%v", ok, err)
	}
	runningRunID := claim.Run.ID
	trigger := claim.Triggers[0].ID
	newActionID := "sevt_new_" + claim.Run.ID
	_, err = runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: newActionID, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{"name": "t", "input": map[string]any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{oldActionID})
	if !isValidation(err) {
		t.Fatalf("stale action id err = %v, want validation", err)
	}

	// Atomic rollback: the run is still running, its trigger unprocessed, its new
	// draft action event never committed, and no pending action for the stale or
	// new id was created by this failed call.
	run, err := runs.Get(ctx, runningRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunRunning {
		t.Fatalf("run state = %s, want running (Complete rolled back)", run.State)
	}
	var processed sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT processed_at FROM events WHERE session_id=? AND id=?`, session.ID, trigger).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed.Valid {
		t.Fatalf("trigger processed_at = %q, want NULL (rolled back)", processed.String)
	}
	var newDraftCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE session_id=? AND id=?`, session.ID, newActionID).Scan(&newDraftCount); err != nil {
		t.Fatal(err)
	}
	if newDraftCount != 0 {
		t.Fatalf("new draft event count = %d, want 0 (rolled back)", newDraftCount)
	}
	// The only pending action ever created was the original park's, now resolved.
	var unresolved int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_actions WHERE session_id=? AND resolved_at IS NULL`, session.ID).Scan(&unresolved); err != nil {
		t.Fatal(err)
	}
	if unresolved != 0 {
		t.Fatalf("unresolved pending actions = %d, want 0 (failed Complete created none)", unresolved)
	}
}

// TestPending_CompleteRejectsNonAskToolUseAtomically proves a committed
// agent.tool_use whose evaluated_permission is NOT "ask" cannot park: it is
// rejected and the completion transaction rolls back, leaving the run running.
func TestPending_CompleteRejectsNonAskToolUseAtomically(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	actionID := "sevt_allow_" + claim.Run.ID
	_, err = runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: actionID, Type: domain.EvAgentToolUse, Payload: map[string]any{"name": "bash", "input": map[string]any{}, "evaluated_permission": "always_allow"}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{actionID})
	if !isValidation(err) {
		t.Fatalf("non-ask tool_use park err = %v, want validation", err)
	}
	run, err := runs.Get(ctx, claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunRunning {
		t.Fatalf("run state = %s, want running (Complete rolled back)", run.State)
	}
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_actions WHERE session_id=?`, session.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending actions = %d, want 0", pending)
	}
}

// TestPending_CompleteAcceptsAskToolUse proves a committed agent.tool_use with
// evaluated_permission "ask" parks: a durable PendingToolConfirmation is created
// in the completion transaction and the session goes idle.
func TestPending_CompleteAcceptsAskToolUse(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	actionID := "sevt_ask_" + claim.Run.ID
	completion, err := runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: actionID, Type: domain.EvAgentToolUse, Payload: map[string]any{"name": "bash", "input": map[string]any{}, "evaluated_permission": "ask"}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{actionID})
	if err != nil {
		t.Fatalf("ask tool_use park: %v", err)
	}
	if completion.Session.Status != domain.StatusIdle {
		t.Fatalf("session status = %s, want idle", completion.Session.Status)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pending, err := unresolvedPendingActions(ctx, tx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("unresolved pending = %d, want 1", len(pending))
	}
	if pending[0].actionEventID != actionID || pending[0].kind != domain.PendingToolConfirmation {
		t.Fatalf("pending = %+v, want tool_confirmation for %s", pending[0], actionID)
	}
}

// TestPending_CompleteRejectsDuplicateActionIDs proves duplicate pending action
// ids in one park are rejected explicitly rather than silently collapsing to a
// single gate, and the completion transaction rolls back.
func TestPending_CompleteRejectsDuplicateActionIDs(t *testing.T) {
	db, runs, session := newRunStoreFixture(t)
	ctx := context.Background()
	if _, err := runs.CreateSession(ctx, session, []domain.EventDraft{{Type: domain.EvUserMessage}}); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := runs.ClaimNext(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	actionID := "sevt_dup_" + claim.Run.ID
	_, err = runs.Complete(ctx, claim.Run.ID, []domain.EventDraft{
		{ID: actionID, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{"name": "t", "input": map[string]any{}}},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{"stop_reason": map[string]any{"type": "requires_action"}}},
	}, domain.StatusIdle, nil, []string{actionID, actionID})
	if !isValidation(err) {
		t.Fatalf("duplicate action id err = %v, want validation", err)
	}
	run, err := runs.Get(ctx, claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunRunning {
		t.Fatalf("run state = %s, want running (Complete rolled back)", run.State)
	}
	var pending int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_actions WHERE session_id=?`, session.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending actions = %d, want 0 (duplicate park rolled back)", pending)
	}
}
