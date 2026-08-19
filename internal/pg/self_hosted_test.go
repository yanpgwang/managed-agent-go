package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestSelfHostedToolResultParksClaimsAndResumes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_self_hosted_result")
	actionID := "sevt_self_hosted_read"

	parked, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		triggerID,
		[]domain.EventDraft{
			{
				ID: actionID, Type: domain.EvAgentToolUse,
				Payload: map[string]any{
					"name": "read", "input": map[string]any{"path": "report.md"},
					"evaluated_permission":            "allow",
					domain.InternalToolExecutionOwner: "self_hosted",
				},
			},
			requiresActionDraft([]string{actionID}),
		},
		domain.StatusIdle,
		"",
		"",
		nil,
		[]string{actionID},
		nil,
	)
	if err != nil {
		t.Fatalf("park self-hosted tool: %v", err)
	}
	if !parked.Parked {
		t.Fatal("self-hosted tool did not park")
	}
	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	if pending[0].Kind != domain.PendingToolResult {
		t.Fatalf("pending kind = %q", pending[0].Kind)
	}

	admitted, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolResult,
		Payload: map[string]any{
			"tool_use_id": actionID,
			"content":     []any{map[string]any{"type": "text", "text": "contents"}},
			"is_error":    false,
		},
	}})
	if err != nil {
		t.Fatalf("admit user.tool_result: %v", err)
	}
	if len(admitted.Events) < 1 || admitted.Events[0].Type != domain.EvUserToolResult {
		t.Fatalf("admitted events = %+v", admitted.Events)
	}
	resolutionID := admitted.Events[0].ID
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 1 || pending[0].ResolvingEventID == nil ||
		*pending[0].ResolvingEventID != resolutionID {
		t.Fatalf("claimed pending = %+v err=%v", pending, err)
	}

	_, err = store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		resolutionID,
		[]domain.EventDraft{{
			Type:    domain.EvSessionStatusIdle,
			Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
		}},
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		[]string{resolutionID},
	)
	if err != nil {
		t.Fatalf("complete self-hosted resume: %v", err)
	}
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after resume = %+v err=%v", pending, err)
	}
}
