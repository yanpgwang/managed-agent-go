package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestValidatePendingCompletion(t *testing.T) {
	actionID := "sevt_action"
	validDrafts := []domain.EventDraft{{
		Type: domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{
			"type": "requires_action", "event_ids": []string{actionID},
		}},
	}}
	tests := []struct {
		name    string
		status  domain.Status
		drafts  []domain.EventDraft
		ids     []string
		wantErr bool
	}{
		{name: "no pending is unchanged", status: domain.StatusIdle},
		{name: "valid", status: domain.StatusIdle, drafts: validDrafts, ids: []string{actionID}},
		{name: "must idle", status: domain.StatusRunning, drafts: validDrafts, ids: []string{actionID}, wantErr: true},
		{name: "requires boundary", status: domain.StatusIdle, ids: []string{actionID}, wantErr: true},
		{
			name:    "ids must match",
			status:  domain.StatusIdle,
			drafts:  validDrafts,
			ids:     []string{"sevt_other"},
			wantErr: true,
		},
		{
			name:   "duplicate ids rejected",
			status: domain.StatusIdle,
			drafts: []domain.EventDraft{{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{"stop_reason": map[string]any{
					"type": "requires_action", "event_ids": []string{actionID, actionID},
				}},
			}},
			ids:     []string{actionID, actionID},
			wantErr: true,
		},
		{
			name:   "event ids must be strings",
			status: domain.StatusIdle,
			drafts: []domain.EventDraft{{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{"stop_reason": map[string]any{
					"type": "requires_action", "event_ids": []any{actionID, 7},
				}},
			}},
			ids:     []string{actionID},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePendingCompletion(test.status, test.drafts, test.ids)
			if test.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCompleteWorkflowTurn_ParksPendingActionsAtomically(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_pending_park")

	actionIDs := []string{"sevt_custom_action", "sevt_confirmation_action"}
	output := []domain.EventDraft{
		{
			ID:   actionIDs[0],
			Type: domain.EvAgentCustomToolUse,
			Payload: map[string]any{
				"name": "get_metrics", "input": map[string]any{"window": "1h"},
			},
		},
		{
			ID:   actionIDs[1],
			Type: domain.EvAgentToolUse,
			Payload: map[string]any{
				"name": "bash", "input": map[string]any{"command": "deploy"},
				"evaluated_permission": "ask",
			},
		},
		requiresActionDraft(actionIDs),
	}

	first, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		triggerID,
		output,
		domain.StatusIdle,
		"",
		"",
		nil,
		actionIDs,
		nil,
	)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if !first.Applied || first.Session.Status != domain.StatusIdle {
		t.Fatalf("completion = applied:%v status:%s, want applied idle", first.Applied, first.Session.Status)
	}

	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending actions = %d, want 2", len(pending))
	}
	if pending[0].ActionEventID != actionIDs[0] ||
		pending[0].Kind != domain.PendingCustomToolResult {
		t.Fatalf("first pending = %+v", pending[0])
	}
	if pending[1].ActionEventID != actionIDs[1] ||
		pending[1].Kind != domain.PendingToolConfirmation {
		t.Fatalf("second pending = %+v", pending[1])
	}
	for _, action := range pending {
		if action.ResolvingEventID != nil || action.ResolvedAt != nil {
			t.Fatalf("new pending action is not open: %+v", action)
		}
	}

	// A lost Activity acknowledgement re-enters the same completion. The
	// trigger's completed turn is replayed and no duplicate gates are inserted.
	second, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		triggerID,
		output,
		domain.StatusIdle,
		"",
		"",
		nil,
		actionIDs,
		nil,
	)
	if err != nil {
		t.Fatalf("retry park: %v", err)
	}
	if second.Applied {
		t.Fatal("retry must replay the committed park")
	}
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending after retry = %d err=%v, want 2", len(pending), err)
	}
}

func TestCompleteWorkflowTurn_InvalidPendingActionRollsBack(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_pending_rollback")
	actionID := "sevt_not_ask"
	output := []domain.EventDraft{
		{
			ID:   actionID,
			Type: domain.EvAgentToolUse,
			Payload: map[string]any{
				"name": "bash", "input": map[string]any{"command": "echo safe"},
				"evaluated_permission": "always_allow",
			},
		},
		requiresActionDraft([]string{actionID}),
	}

	_, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		triggerID,
		output,
		domain.StatusIdle,
		"",
		"",
		nil,
		[]string{actionID},
		nil,
	)
	requireDomainKind(t, err, domain.KindValidation)

	trigger, err := store.GetEvent(ctx, session.ID, triggerID)
	if err != nil {
		t.Fatalf("get trigger: %v", err)
	}
	if trigger.ProcessedAt != nil {
		t.Fatal("failed park must not mark its trigger processed")
	}
	if _, err := store.GetEvent(ctx, session.ID, actionID); err == nil {
		t.Fatal("failed park committed its action event")
	}
	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("failed park created %d pending actions", len(pending))
	}
	stored, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.Status != domain.StatusRunning {
		t.Fatalf("failed park changed session status to %s", stored.Status)
	}
}

func TestPendingActions_AdmissionGateAndResumeCompletion(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_pending_resume")
	actionID := "sevt_resume_action"
	parkCustomActions(t, store, session.ID, triggerID, []string{actionID})

	// Ordinary work remains durable but cannot make an idle parked session look
	// runnable or enqueue a wakeup of its own.
	ordinary, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "queued behind the tool result"},
		}},
	}})
	if err != nil {
		t.Fatalf("admit ordinary message: %v", err)
	}
	if ordinary.Session.Status != domain.StatusIdle || ordinary.Enqueued {
		t.Fatalf(
			"gated admission = status:%s enqueued:%v, want idle false",
			ordinary.Session.Status,
			ordinary.Enqueued,
		)
	}
	if len(ordinary.Events) != 1 || ordinary.Events[0].Type != domain.EvUserMessage {
		t.Fatalf("gated admission events = %+v", ordinary.Events)
	}
	ordinaryEventID := ordinary.Events[0].ID

	// Unknown and wrong-kind references roll back atomically.
	before, err := store.EventsAfter(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": "sevt_unknown", "content": []any{},
		},
	}})
	requireDomainKind(t, err, domain.KindValidation)
	_, err = store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolConfirmation,
		Payload: map[string]any{
			"tool_use_id": actionID, "result": "allow",
		},
	}})
	requireDomainKind(t, err, domain.KindValidation)
	after, err := store.EventsAfter(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("rejected resolutions changed ledger: before=%d after=%d", len(before), len(after))
	}

	resolution, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": actionID,
			"content":            []any{map[string]any{"type": "text", "text": "42"}},
			"is_error":           false,
		},
	}})
	if err != nil {
		t.Fatalf("admit resolution: %v", err)
	}
	if resolution.Session.Status != domain.StatusRunning || !resolution.Enqueued {
		t.Fatalf(
			"resolution admission = status:%s enqueued:%v, want running true",
			resolution.Session.Status,
			resolution.Enqueued,
		)
	}
	resolutionID := resolution.Events[0].ID
	if resolution.Events[0].ProcessedAt == nil {
		t.Fatal("custom tool result must be processed on receipt")
	}
	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("claimed pending = %+v err=%v", pending, err)
	}
	if pending[0].ResolvingEventID == nil || *pending[0].ResolvingEventID != resolutionID {
		t.Fatalf("pending resolution id = %v, want %s", pending[0].ResolvingEventID, resolutionID)
	}
	if pending[0].ResolvedAt != nil {
		t.Fatal("pending action resolved before the resume turn completed")
	}
	// Model the relay delivering the resolution wakeup before the resume
	// completion. The queued ordinary message had no wakeup of its own, so
	// completion must enqueue a fresh one when it clears the gate.
	deliveredWakeup, ok, err := store.PendingWakeup(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("resolution wakeup = %+v ok=%v err=%v", deliveredWakeup, ok, err)
	}
	if removed, err := store.DeleteWakeupIfUnchanged(
		ctx,
		session.ID,
		deliveredWakeup.MaxEventSeq,
	); err != nil || !removed {
		t.Fatalf("consume resolution wakeup: removed=%v err=%v", removed, err)
	}

	// A second resolution cannot claim the same still-in-flight wait.
	_, err = store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": actionID, "content": []any{},
		},
	}})
	requireDomainKind(t, err, domain.KindConflict)

	// processed_at on the custom result must not make completion short-circuit.
	// The unresolved pending row is the durable discriminator. Completion clears
	// the gate and reopens to the ordinary message already queued behind it.
	done, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		resolutionID,
		[]domain.EventDraft{
			{
				Type: domain.EvAgentMessage,
				Payload: map[string]any{"content": []any{
					map[string]any{"type": "text", "text": "resumed"},
				}},
			},
			{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{
					"stop_reason": map[string]any{"type": "end_turn"},
				},
			},
		},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		[]string{resolutionID},
	)
	if err != nil {
		t.Fatalf("complete resume: %v", err)
	}
	if !done.Applied || done.Session.Status != domain.StatusRunning {
		t.Fatalf("resume completion = applied:%v status:%s", done.Applied, done.Session.Status)
	}
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after resume = %+v err=%v", pending, err)
	}
	ordinaryEvent, err := store.GetEvent(ctx, session.ID, ordinaryEventID)
	if err != nil {
		t.Fatal(err)
	}
	if ordinaryEvent.ProcessedAt != nil {
		t.Fatal("queued ordinary message was processed before its own turn")
	}
	requeued, ok, err := store.PendingWakeup(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("un-gate wakeup = %+v ok=%v err=%v", requeued, ok, err)
	}
	if requeued.MaxEventSeq <= deliveredWakeup.MaxEventSeq {
		t.Fatalf(
			"un-gate wakeup seq = %d, want newer than delivered %d",
			requeued.MaxEventSeq,
			deliveredWakeup.MaxEventSeq,
		)
	}

	retry, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		resolutionID,
		[]domain.EventDraft{{
			Type: domain.EvAgentMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "resumed"},
			}},
		}},
		domain.StatusRunning,
		"",
		"",
		nil,
		nil,
		[]string{resolutionID},
	)
	if err != nil {
		t.Fatalf("retry resume completion: %v", err)
	}
	if retry.Applied {
		t.Fatal("resume retry must replay rather than commit again")
	}
}

func TestPendingActions_PartialResolutionKeepsRemainingGate(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_pending_partial")
	actionIDs := []string{"sevt_partial_a", "sevt_partial_b"}
	if _, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		triggerID,
		[]domain.EventDraft{
			{
				ID:   actionIDs[0],
				Type: domain.EvAgentCustomToolUse,
				Payload: map[string]any{
					"name": "client_tool", "input": map[string]any{},
				},
			},
			{
				ID:   actionIDs[1],
				Type: domain.EvAgentToolUse,
				Payload: map[string]any{
					"name": "bash", "input": map[string]any{"command": "echo ready"},
					"evaluated_permission": "ask",
				},
			},
			requiresActionDraft(actionIDs),
		},
		domain.StatusIdle,
		"",
		"",
		nil,
		actionIDs,
		nil,
	); err != nil {
		t.Fatalf("park actions: %v", err)
	}
	// The initial user.message wakeup has already been delivered by the time the
	// turn parks. Remove it so this test can distinguish a new resolution wakeup.
	wakeup, ok, err := store.PendingWakeup(ctx, session.ID)
	if err != nil || !ok {
		t.Fatalf("initial wakeup = %+v ok=%v err=%v", wakeup, ok, err)
	}
	if removed, err := store.DeleteWakeupIfUnchanged(
		ctx,
		session.ID,
		wakeup.MaxEventSeq,
	); err != nil || !removed {
		t.Fatalf("consume initial wakeup: removed=%v err=%v", removed, err)
	}

	firstResolution, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": actionIDs[0], "content": []any{},
		},
	}})
	if err != nil {
		t.Fatalf("admit first resolution: %v", err)
	}
	if firstResolution.Session.Status != domain.StatusIdle || firstResolution.Enqueued {
		t.Fatalf(
			"partial resolution = status:%s enqueued:%v, want idle false",
			firstResolution.Session.Status,
			firstResolution.Enqueued,
		)
	}
	if _, ok, err := store.PendingWakeup(ctx, session.ID); err != nil || ok {
		t.Fatalf("partial resolution wakeup: ok=%v err=%v", ok, err)
	}
	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 ||
		pending[0].ResolvingEventID == nil ||
		*pending[0].ResolvingEventID != firstResolution.Events[0].ID ||
		pending[1].ResolvingEventID != nil {
		t.Fatalf("partial barrier = %+v", pending)
	}

	secondResolution, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolConfirmation,
		Payload: map[string]any{
			"tool_use_id": actionIDs[1], "result": "allow",
		},
	}})
	if err != nil {
		t.Fatalf("admit second resolution: %v", err)
	}
	if secondResolution.Session.Status != domain.StatusRunning || !secondResolution.Enqueued {
		t.Fatalf(
			"complete barrier admission = status:%s enqueued:%v, want running true",
			secondResolution.Session.Status,
			secondResolution.Enqueued,
		)
	}
	secondResolutionID := secondResolution.Events[0].ID
	secondEvent, err := store.GetEvent(ctx, session.ID, secondResolutionID)
	if err != nil {
		t.Fatal(err)
	}
	if secondEvent.ProcessedAt != nil {
		t.Fatal("tool confirmation processed before the barrier resume completed")
	}

	resolutionIDs := []string{firstResolution.Events[0].ID, secondResolutionID}
	_, err = store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		secondResolutionID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		[]string{secondResolutionID},
	)
	requireDomainKind(t, err, domain.KindValidation)

	// The exact complete set closes the whole barrier and stamps every
	// resolution trigger in the same transaction.
	done, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		secondResolutionID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		resolutionIDs,
	)
	if err != nil {
		t.Fatalf("complete resolution barrier: %v", err)
	}
	if done.Session.Status != domain.StatusIdle {
		t.Fatalf("barrier completion status = %s, want idle", done.Session.Status)
	}
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after all resolutions = %+v err=%v", pending, err)
	}
	secondEvent, err = store.GetEvent(ctx, session.ID, secondResolutionID)
	if err != nil {
		t.Fatal(err)
	}
	if secondEvent.ProcessedAt == nil {
		t.Fatal("barrier completion did not process the tool confirmation")
	}
	retry, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		secondResolutionID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		resolutionIDs,
	)
	if err != nil {
		t.Fatalf("retry barrier completion: %v", err)
	}
	if retry.Applied {
		t.Fatal("barrier completion retry must replay")
	}
}

func TestPendingActions_ProcessesCompanionsFromEveryBarrierResolution(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_pending_companions")
	actionIDs := []string{"sevt_companion_a", "sevt_companion_b"}
	parkCustomActions(t, store, session.ID, triggerID, actionIDs)

	first, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{
		{
			Type: domain.EvUserCustomToolResult,
			Payload: map[string]any{
				"custom_tool_use_id": actionIDs[0], "content": []any{},
			},
		},
		{
			Type: domain.EvSystemMessage,
			Payload: map[string]any{"content": []any{map[string]any{
				"type": "text", "text": "context for the resumed turn",
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstResolution := eventOfType(t, first.Events, domain.EvUserCustomToolResult)
	companion := eventOfType(t, first.Events, domain.EvSystemMessage)
	if companion.ProcessedAt != nil {
		t.Fatal("companion was processed before the complete barrier resumed")
	}

	second, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserCustomToolResult,
		Payload: map[string]any{
			"custom_tool_use_id": actionIDs[1], "content": []any{},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	secondResolution := eventOfType(t, second.Events, domain.EvUserCustomToolResult)
	if _, err := store.CompleteWorkflowTurn(
		ctx, session.ID, secondResolution.ID,
		[]domain.EventDraft{{
			Type: domain.EvSessionStatusIdle,
			Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			},
		}},
		domain.StatusIdle, "", "", nil, nil,
		[]string{firstResolution.ID, secondResolution.ID},
	); err != nil {
		t.Fatal(err)
	}
	companion, err = store.GetEvent(ctx, session.ID, companion.ID)
	if err != nil || companion.ProcessedAt == nil {
		t.Fatalf("processed companion = %+v, err=%v", companion, err)
	}
}

func pendingTurn(t *testing.T, store *Store, sessionID string) (domain.Session, string) {
	t.Helper()
	ctx := context.Background()
	session := newSession(sessionID)
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "use a client action"},
		}},
	}})
	if err != nil {
		t.Fatalf("admit trigger: %v", err)
	}
	return session, admission.Events[0].ID
}

func parkCustomActions(
	t *testing.T,
	store *Store,
	sessionID string,
	triggerID string,
	actionIDs []string,
) {
	t.Helper()
	output := make([]domain.EventDraft, 0, len(actionIDs)+1)
	for _, actionID := range actionIDs {
		output = append(output, domain.EventDraft{
			ID:   actionID,
			Type: domain.EvAgentCustomToolUse,
			Payload: map[string]any{
				"name": "client_tool", "input": map[string]any{},
			},
		})
	}
	output = append(output, requiresActionDraft(actionIDs))
	if _, err := store.CompleteWorkflowTurn(
		context.Background(),
		sessionID,
		triggerID,
		output,
		domain.StatusIdle,
		"",
		"",
		nil,
		actionIDs,
		nil,
	); err != nil {
		t.Fatalf("park actions: %v", err)
	}
}

func requiresActionDraft(actionIDs []string) domain.EventDraft {
	return domain.EventDraft{
		Type: domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{
			"type": "requires_action", "event_ids": append([]string(nil), actionIDs...),
		}},
	}
}

func requireDomainKind(t *testing.T, err error, kind domain.ErrKind) {
	t.Helper()
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != kind {
		t.Fatalf("error = %v, want domain kind %d", err, kind)
	}
}
