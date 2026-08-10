package pg

import (
	"errors"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

func completeLifecycleChildIdle(t *testing.T, fixture multiagentInterruptFixture) {
	t.Helper()
	completion, err := fixture.store.CompleteThreadWorkflowTurn(
		fixture.ctx, fixture.session.ID, fixture.child.ID, fixture.childTrigger.ID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle, "", "", nil, nil, nil, nil, nil,
		domain.TokenUsage{},
	)
	if err != nil || completion.ThreadStatus != domain.StatusIdle {
		t.Fatalf("complete child idle = %+v, err=%v", completion, err)
	}
}

func TestArchiveSessionThread_RejectsRunningChild(t *testing.T) {
	fixture := newMultiagentInterruptFixture(t, "archive_running")
	_, err := fixture.store.ArchiveSessionThread(
		fixture.ctx, fixture.session.ID, fixture.child.ID,
	)
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict {
		t.Fatalf("archive running child error = %v", err)
	}
}

func TestArchiveSessionThread_CommitsLifecycleAndTerminateIntentAtomically(t *testing.T) {
	fixture := newMultiagentInterruptFixture(t, "archive_idle")
	completeLifecycleChildIdle(t, fixture)

	archived, err := fixture.store.ArchiveSessionThread(
		fixture.ctx, fixture.session.ID, fixture.child.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || archived.TerminatedAt == nil ||
		archived.Status != domain.StatusTerminated {
		t.Fatalf("archived child = %+v", archived)
	}

	childEvents, err := fixture.store.ThreadEventsAfter(
		fixture.ctx, fixture.session.ID, fixture.child.ID, 0, 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	primaryEvents, err := fixture.store.QueryEvents(
		fixture.ctx, fixture.session.ID, app.EventQuery{Limit: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	if countEventsOfType(childEvents, domain.EvSessionThreadStatusTerminated) != 1 ||
		countEventsOfType(primaryEvents, domain.EvSessionThreadStatusTerminated) != 1 {
		t.Fatalf(
			"termination events child=%+v primary=%+v",
			childEvents, primaryEvents,
		)
	}
	var (
		intent string
		maxSeq int64
	)
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
SELECT intent, max_event_seq
FROM thread_orchestration_outbox
WHERE session_id = $1 AND thread_id = $2`,
		fixture.session.ID, fixture.child.ID,
	).Scan(&intent, &maxSeq); err != nil {
		t.Fatal(err)
	}
	var ledgerMax int64
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
SELECT COALESCE(MAX(seq), 0) FROM events WHERE session_id = $1`,
		fixture.session.ID,
	).Scan(&ledgerMax); err != nil {
		t.Fatal(err)
	}
	if intent != string(OrchestrationTerminate) || maxSeq != ledgerMax {
		t.Fatalf("terminate outbox intent=%q max=%d ledger=%d", intent, maxSeq, ledgerMax)
	}
	if err := fixture.store.q.UpsertThreadOutbox(
		fixture.ctx,
		pgstore.UpsertThreadOutboxParams{
			SessionID: fixture.session.ID, ThreadID: fixture.child.ID,
			MaxEventSeq: maxSeq + 1, EnqueuedAt: tsUTC(fixture.store.clock.Now()),
		},
	); err != nil {
		t.Fatal(err)
	}
	deleted, err := fixture.store.DeleteThreadWakeupIfUnchanged(
		fixture.ctx, fixture.session.ID, fixture.child.ID, maxSeq+1,
		OrchestrationWake,
	)
	if err != nil || deleted {
		t.Fatalf("stale wake deleted termination: deleted=%v err=%v", deleted, err)
	}
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
SELECT intent, max_event_seq
FROM thread_orchestration_outbox
WHERE session_id = $1 AND thread_id = $2`,
		fixture.session.ID, fixture.child.ID,
	).Scan(&intent, &maxSeq); err != nil ||
		intent != string(OrchestrationTerminate) {
		t.Fatalf("stale wake downgraded termination: intent=%q err=%v", intent, err)
	}

	firstArchivedAt := *archived.ArchivedAt
	again, err := fixture.store.ArchiveSessionThread(
		fixture.ctx, fixture.session.ID, fixture.child.ID,
	)
	if err != nil || again.ArchivedAt == nil || !again.ArchivedAt.Equal(firstArchivedAt) {
		t.Fatalf("idempotent archive = %+v, err=%v", again, err)
	}
	childEvents, err = fixture.store.ThreadEventsAfter(
		fixture.ctx, fixture.session.ID, fixture.child.ID, 0, 100,
	)
	if err != nil || countEventsOfType(
		childEvents, domain.EvSessionThreadStatusTerminated,
	) != 1 {
		t.Fatalf("duplicate termination events = %+v, err=%v", childEvents, err)
	}

	stale, err := fixture.store.CompleteThreadWorkflowTurn(
		fixture.ctx, fixture.session.ID, fixture.child.ID, fixture.childTrigger.ID,
		nil, domain.StatusIdle, "", "", nil, nil, nil, nil, nil,
		domain.TokenUsage{},
	)
	if err != nil || stale.Applied || stale.ThreadStatus != domain.StatusTerminated {
		t.Fatalf("stale completion = %+v, err=%v", stale, err)
	}
}

func TestArchiveSessionThread_ClosesPendingBarrier(t *testing.T) {
	fixture := newMultiagentInterruptFixture(t, "archive_pending")
	const actionID = "sevt_archive_pending_action"
	if _, err := fixture.store.CompleteThreadWorkflowTurn(
		fixture.ctx, fixture.session.ID, fixture.child.ID, fixture.childTrigger.ID,
		[]domain.EventDraft{
			{ID: actionID, Type: domain.EvAgentCustomToolUse, Payload: map[string]any{
				"name": "review_result", "input": map[string]any{},
			}},
			{Type: domain.EvSessionStatusIdle, Payload: map[string]any{
				"stop_reason": map[string]any{
					"type": "requires_action", "event_ids": []string{actionID},
				},
			}},
		},
		domain.StatusIdle, "", "", nil, []string{actionID}, nil, nil, nil,
		domain.TokenUsage{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ArchiveSessionThread(
		fixture.ctx, fixture.session.ID, fixture.child.ID,
	); err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.store.UnresolvedThreadPendingActions(
		fixture.ctx, fixture.session.ID, fixture.child.ID,
	)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending barrier survived = %+v, err=%v", pending, err)
	}
	_, err = fixture.store.AdmitEvents(
		fixture.ctx, fixture.session.ID,
		[]domain.EventDraft{{
			Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{
				"custom_tool_use_id": actionID, "content": []any{},
			},
		}},
	)
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindValidation {
		t.Fatalf("resolution after archive error = %v", err)
	}

}

func TestArchiveSessionThread_StopsReschedulingWork(t *testing.T) {
	retryFixture := newMultiagentInterruptFixture(t, "archive_rescheduling")
	if err := retryFixture.store.RecordThreadWorkflowRetry(
		retryFixture.ctx, retryFixture.session.ID, retryFixture.child.ID,
		retryFixture.childTrigger.ID, "sevt_archive_retry_error",
		"sevt_archive_retry_status", map[string]any{
			"type": "model_overloaded_error", "message": "retry",
			"retry_status": map[string]any{"type": "retrying"},
		},
	); err != nil {
		t.Fatal(err)
	}
	archived, err := retryFixture.store.ArchiveSessionThread(
		retryFixture.ctx, retryFixture.session.ID, retryFixture.child.ID,
	)
	if err != nil || archived.Status != domain.StatusTerminated ||
		archived.ArchivedAt == nil {
		t.Fatalf("archive rescheduling child = %+v, err=%v", archived, err)
	}
	trigger, err := retryFixture.store.GetEvent(
		retryFixture.ctx, retryFixture.session.ID, retryFixture.childTrigger.ID,
	)
	if err != nil || trigger.ProcessedAt == nil {
		t.Fatalf("rescheduling trigger was not flushed = %+v, err=%v", trigger, err)
	}
}

func TestPrepareSessionDeletion_UpgradesEveryChildOutboxToTermination(t *testing.T) {
	fixture := newMultiagentInterruptFixture(t, "delete_children")
	completeLifecycleChildIdle(t, fixture)
	if err := fixture.store.PrepareSessionDeletion(
		fixture.ctx, fixture.session.ID,
	); err != nil {
		t.Fatal(err)
	}
	threadIDs, err := fixture.store.ListChildSessionThreadIDs(
		fixture.ctx, fixture.session.ID,
	)
	if err != nil || len(threadIDs) != 1 || threadIDs[0] != fixture.child.ID {
		t.Fatalf("child workflow identities = %v, err=%v", threadIDs, err)
	}
	var intent string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
SELECT intent
FROM thread_orchestration_outbox
WHERE session_id = $1 AND thread_id = $2`,
		fixture.session.ID, fixture.child.ID,
	).Scan(&intent); err != nil {
		t.Fatal(err)
	}
	if intent != string(OrchestrationTerminate) {
		t.Fatalf("deletion child outbox intent = %q", intent)
	}
}

func countEventsOfType(events []domain.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
