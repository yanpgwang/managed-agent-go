package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// An interrupted turn keeps a tool round only when the round durably completed.
// The MCP pair correlates through mcp_tool_use_id, so the filter must join on
// that field rather than tool_use_id or a completed MCP call would be dropped
// from the public ledger.
func TestInterruptedTurnDrafts_KeepsCompletedMCPPair(t *testing.T) {
	drafts := []domain.EventDraft{
		{
			ID: "sevt_mcp_done", Type: domain.EvAgentMcpToolUse,
			Payload: map[string]any{
				"name": "list_issues", "mcp_server_name": "github",
			},
		},
		{
			Type: domain.EvAgentMcpToolResult,
			Payload: map[string]any{
				"mcp_tool_use_id": "sevt_mcp_done",
				"content":         []any{},
			},
		},
		{
			ID: "sevt_mcp_open", Type: domain.EvAgentMcpToolUse,
			Payload: map[string]any{
				"name": "create_issue", "mcp_server_name": "github",
				"evaluated_permission": "ask",
			},
		},
		{
			ID: "sevt_builtin_open", Type: domain.EvAgentToolUse,
			Payload: map[string]any{"name": "bash"},
		},
		{Type: domain.EvSessionStatusIdle, Payload: map[string]any{}},
	}

	out, _ := interruptedTurnDrafts(drafts)

	got := make([]string, 0, len(out))
	for _, draft := range out {
		got = append(got, draft.Type+"/"+draft.ID)
	}
	want := []string{
		domain.EvAgentMcpToolUse + "/sevt_mcp_done",
		domain.EvAgentMcpToolResult + "/",
	}
	if len(got) != len(want) {
		t.Fatalf("interrupted drafts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interrupted drafts = %v, want %v", got, want)
		}
	}
}

// The MCP tool-use variant must also be recognized when pruning provider
// tool-use mappings down to what a turn actually committed.
func TestRetainCommittedProviderMappings_IncludesMCPToolUse(t *testing.T) {
	mappings := []domain.ProviderToolUseMapping{
		{PublicEventID: "sevt_mcp", ProviderToolUseID: "toolu_mcp"},
		{PublicEventID: "sevt_dropped", ProviderToolUseID: "toolu_dropped"},
	}
	drafts := []domain.EventDraft{{
		ID: "sevt_mcp", Type: domain.EvAgentMcpToolUse,
		Payload: map[string]any{"mcp_server_name": "github"},
	}}

	out := retainCommittedProviderMappings(mappings, drafts)

	if len(out) != 1 || out[0].PublicEventID != "sevt_mcp" {
		t.Fatalf("retained mappings = %#v", out)
	}
}

// The durable half of the MCP barrier. A parked agent.mcp_tool_use must create
// the same tool_confirmation gate a built-in does in the real pending_actions
// table, a user.tool_confirmation carrying tool_use_id must claim it, and the
// resume turn must be able to close it with an agent.mcp_tool_result. The
// committed ledger is then checked for the pairing invariant the public wire
// depends on.
func TestPendingActions_MCPConfirmationBarrierRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_mcp_barrier")

	actionID := "sevt_mcp_park"
	park := []domain.EventDraft{
		{
			ID:   actionID,
			Type: domain.EvAgentMcpToolUse,
			Payload: map[string]any{
				"name":                 "list_issues",
				"mcp_server_name":      "github",
				"input":                map[string]any{"repo": "mango"},
				"evaluated_permission": "ask",
			},
		},
		requiresActionDraft([]string{actionID}),
	}
	if _, err := store.CompleteWorkflowTurn(
		ctx, session.ID, triggerID, park, domain.StatusIdle,
		"", "", nil, []string{actionID}, nil,
	); err != nil {
		t.Fatalf("park mcp confirmation: %v", err)
	}

	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 ||
		pending[0].ActionEventID != actionID ||
		pending[0].Kind != domain.PendingToolConfirmation {
		t.Fatalf("mcp park pending = %+v", pending)
	}

	// The documented confirmation input has one id field for both tool-use
	// variants, so the MCP park is claimed through tool_use_id.
	resolution, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolConfirmation,
		Payload: map[string]any{
			"tool_use_id": actionID, "result": "allow",
		},
	}})
	if err != nil {
		t.Fatalf("admit mcp confirmation: %v", err)
	}
	if resolution.Session.Status != domain.StatusRunning || !resolution.Enqueued {
		t.Fatalf(
			"mcp confirmation admission = status:%s enqueued:%v, want running true",
			resolution.Session.Status,
			resolution.Enqueued,
		)
	}
	resolutionID := resolution.Events[0].ID

	done, err := store.CompleteWorkflowTurn(
		ctx, session.ID, resolutionID,
		[]domain.EventDraft{
			{
				Type: domain.EvAgentMcpToolResult,
				Payload: map[string]any{
					"mcp_tool_use_id": actionID,
					"content": []any{
						map[string]any{"type": "text", "text": "#1, #2"},
					},
					"is_error": false,
				},
			},
			{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{
					"stop_reason": map[string]any{"type": "end_turn"},
				},
			},
		},
		domain.StatusIdle, "", "", nil, nil, []string{resolutionID},
	)
	if err != nil {
		t.Fatalf("complete mcp resume: %v", err)
	}
	if !done.Applied {
		t.Fatal("mcp resume completion was not applied")
	}
	pending, err = store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after mcp resume = %+v err=%v", pending, err)
	}

	ledger, err := store.EventsAfter(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	requireToolPairsAgree(t, ledger)
	if got := eventTypes(ledger); !containsInOrder(got, []string{
		domain.EvAgentMcpToolUse, domain.EvAgentMcpToolResult,
	}) {
		t.Fatalf("committed ledger = %v", got)
	}
	// The projected model request must still see one paired tool round.
	messages := domain.ProjectMessages(ledger)
	if len(messages) < 3 {
		t.Fatalf("projected messages = %#v", messages)
	}
}

// A barrier parked before the mcp-tool-event-types change is durably an
// agent.tool_use, whatever naming the resuming worker would choose for a new
// call. Closing it with an agent.mcp_tool_result would leave mcp_tool_use_id
// pointing at an event that is not an agent.mcp_tool_use, so the legacy pairing
// has to survive the durable round trip.
func TestPendingActions_LegacyMCPParkClosesOnLegacyPair(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_legacy_mcp_barrier")

	actionID := "sevt_legacy_mcp_park"
	// The pre-change shape: an MCP call announced on agent.tool_use, still
	// carrying the server name that lets the alias be rebuilt on resume.
	if _, err := store.CompleteWorkflowTurn(
		ctx, session.ID, triggerID,
		[]domain.EventDraft{
			{
				ID:   actionID,
				Type: domain.EvAgentToolUse,
				Payload: map[string]any{
					"name":                 "list_issues",
					"mcp_server_name":      "github",
					"input":                map[string]any{"repo": "mango"},
					"evaluated_permission": "ask",
				},
			},
			requiresActionDraft([]string{actionID}),
		},
		domain.StatusIdle, "", "", nil, []string{actionID}, nil,
	); err != nil {
		t.Fatalf("park legacy mcp confirmation: %v", err)
	}

	resolution, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserToolConfirmation,
		Payload: map[string]any{
			"tool_use_id": actionID, "result": "allow",
		},
	}})
	if err != nil {
		t.Fatalf("admit legacy confirmation: %v", err)
	}
	resolutionID := resolution.Events[0].ID

	// What the durable action type forces the resume to write. The store keeps
	// the ledger self-consistent only if the workflow answers on the legacy pair.
	parked, err := store.GetEvent(ctx, session.ID, actionID)
	if err != nil {
		t.Fatalf("read parked action: %v", err)
	}
	resultType := domain.AgentToolResultTypeFor(parked.Type)
	if resultType != domain.EvAgentToolResult {
		t.Fatalf("result type for %s = %s, want %s",
			parked.Type, resultType, domain.EvAgentToolResult)
	}

	if _, err := store.CompleteWorkflowTurn(
		ctx, session.ID, resolutionID,
		[]domain.EventDraft{
			{
				Type: resultType,
				Payload: map[string]any{
					"tool_use_id": actionID,
					"content": []any{
						map[string]any{"type": "text", "text": "#1, #2"},
					},
					"is_error": false,
				},
			},
			{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{
					"stop_reason": map[string]any{"type": "end_turn"},
				},
			},
		},
		domain.StatusIdle, "", "", nil, nil, []string{resolutionID},
	); err != nil {
		t.Fatalf("complete legacy mcp resume: %v", err)
	}

	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after legacy resume = %+v err=%v", pending, err)
	}
	ledger, err := store.EventsAfter(ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	requireToolPairsAgree(t, ledger)
	if got := eventTypes(ledger); !containsInOrder(got, []string{
		domain.EvAgentToolUse, domain.EvAgentToolResult,
	}) {
		t.Fatalf("committed ledger = %v", got)
	}
}

// requireToolPairsAgree asserts the ledger invariant the documented wire
// depends on: an agent.mcp_tool_result's mcp_tool_use_id names an
// agent.mcp_tool_use, and an agent.tool_result's tool_use_id names an
// agent.tool_use. A result that crosses the variants is unreadable for a client
// that dispatches on event type.
func requireToolPairsAgree(t *testing.T, events []domain.Event) {
	t.Helper()
	byID := make(map[string]domain.Event, len(events))
	for _, event := range events {
		byID[event.ID] = event
	}
	for _, event := range events {
		useID, ok := domain.AgentToolResultReference(event.Type, event.Payload)
		if !ok {
			continue
		}
		use, present := byID[useID]
		if !present {
			t.Fatalf("%s references unknown event %q", event.Type, useID)
		}
		if want := domain.AgentToolResultTypeFor(use.Type); want != event.Type {
			t.Fatalf(
				"%s answers a %s; the documented answer is %s",
				event.Type, use.Type, want,
			)
		}
	}
}

func containsInOrder(got []string, want []string) bool {
	i := 0
	for _, value := range got {
		if i < len(want) && value == want[i] {
			i++
		}
	}
	return i == len(want)
}
