package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestAdmitEvents_InterruptOnlyWakesWithoutRunning(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sess_interrupt_idle")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserInterrupt, Payload: map[string]any{},
	}})
	if err != nil {
		t.Fatalf("admit interrupt: %v", err)
	}
	if !admission.Enqueued {
		t.Fatal("interrupt did not write a durable wakeup")
	}
	if admission.Session.Status != domain.StatusIdle {
		t.Fatalf("status = %s, want idle", admission.Session.Status)
	}
	if len(admission.Events) != 1 ||
		admission.Events[0].Type != domain.EvUserInterrupt {
		t.Fatalf("events = %#v, want only user.interrupt", admission.Events)
	}
	if admission.Events[0].ProcessedAt != nil {
		t.Fatal("interrupt was marked processed before the Workflow handled it")
	}
}

func TestInterruptWhilePendingAcknowledgesWithoutOpeningBarrier(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session, triggerID := pendingTurn(t, store, "sess_interrupt_pending")
	const actionID = "sevt_interrupt_pending_action"
	parkCustomActions(t, store, session.ID, triggerID, []string{actionID})

	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserInterrupt, Payload: map[string]any{},
	}})
	if err != nil {
		t.Fatalf("admit interrupt: %v", err)
	}
	if !admission.Enqueued {
		t.Fatal("parked interrupt did not write a durable wakeup")
	}
	if admission.Session.Status != domain.StatusIdle {
		t.Fatalf("admission status = %s, want idle", admission.Session.Status)
	}
	for _, event := range admission.Events {
		if event.Type == domain.EvSessionStatusRunning {
			t.Fatal("parked interrupt emitted session.status_running")
		}
	}
	interruptID := admission.Events[0].ID

	completion, err := store.CompleteWorkflowTurn(
		ctx,
		session.ID,
		interruptID,
		nil,
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("acknowledge interrupt: %v", err)
	}
	if completion.Session.Status != domain.StatusIdle {
		t.Fatalf("completion status = %s, want idle", completion.Session.Status)
	}
	if len(completion.Events) != 0 {
		t.Fatalf("idle interrupt output = %v, want no duplicate terminal event", eventTypes(completion.Events))
	}

	pending, err := store.UnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		t.Fatalf("pending actions: %v", err)
	}
	if len(pending) != 1 || pending[0].ActionEventID != actionID {
		t.Fatalf("pending actions = %+v, want original barrier", pending)
	}
	if pending[0].ResolvingEventID != nil || pending[0].ResolvedAt != nil {
		t.Fatalf("interrupt changed pending barrier: %+v", pending[0])
	}
	interrupt, err := store.GetEvent(ctx, session.ID, interruptID)
	if err != nil {
		t.Fatalf("get interrupt: %v", err)
	}
	if interrupt.ProcessedAt == nil {
		t.Fatal("parked interrupt was not acknowledged")
	}
}

func TestCompleteWorkflowTurn_InterruptWinsCompletionRace(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const sessionID = "sess_interrupt_wins"
	trigger := journalTurn(t, store, sessionID)

	attempt, err := store.EnsureAttempt(
		ctx,
		sessionID,
		trigger,
		"ratm_interrupt_wins",
	)
	if err != nil {
		t.Fatalf("ensure attempt: %v", err)
	}
	step, err := store.EnsureToolStep(
		ctx,
		attempt.ID,
		"tstep_interrupt_wins",
		0,
		"sevt_interrupt_tool",
		"bash",
		map[string]any{"command": "touch marker"},
	)
	if err != nil {
		t.Fatalf("ensure step: %v", err)
	}
	if err := store.StartToolStep(ctx, step.ID); err != nil {
		t.Fatalf("start step: %v", err)
	}

	interruptAdmission, err := store.AdmitEvents(
		ctx,
		sessionID,
		[]domain.EventDraft{{
			Type: domain.EvUserInterrupt, Payload: map[string]any{},
		}},
	)
	if err != nil {
		t.Fatalf("admit interrupt: %v", err)
	}
	interruptID := interruptAdmission.Events[0].ID
	failure := "provider failed after interrupt admission"
	transcriptDelta := []domain.Message{
		{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "text", Text: "run tools",
			}},
		},
		{
			Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{
				{
					Type: "tool_use", ToolUseID: "provider_orphan",
					ToolName: "client_tool", Input: map[string]any{},
				},
				{
					Type: "tool_use", ToolUseID: "provider_completed",
					ToolName: "read", Input: map[string]any{"path": "done.txt"},
				},
			},
		},
		{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "tool_result", ToolResultFor: "provider_completed",
				Text: "done",
			}},
		},
	}
	mappings := []domain.ProviderToolUseMapping{
		{
			PublicEventID:     "sevt_orphan_custom",
			ProviderToolUseID: "provider_orphan",
			ToolName:          "client_tool",
		},
		{
			PublicEventID:     "sevt_completed_tool",
			ProviderToolUseID: "provider_completed",
			ToolName:          "read",
		},
	}
	completion, err := store.CompleteWorkflowTurnWithTranscript(
		ctx,
		sessionID,
		trigger,
		[]domain.EventDraft{
			{
				Type: domain.EvAgentMessage,
				Payload: map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "partial"}},
				},
			},
			{
				ID:   "sevt_orphan_custom",
				Type: domain.EvAgentCustomToolUse,
				Payload: map[string]any{
					"name": "client_tool", "input": map[string]any{},
				},
			},
			{
				ID:   "sevt_completed_tool",
				Type: domain.EvAgentToolUse,
				Payload: map[string]any{
					"name": "read", "input": map[string]any{"path": "done.txt"},
				},
			},
			{
				Type: domain.EvAgentToolResult,
				Payload: map[string]any{
					"tool_use_id": "sevt_completed_tool",
					"content": []any{
						map[string]any{"type": "text", "text": "done"},
					},
					"is_error": false,
				},
			},
			{
				Type: domain.EvSessionError,
				Payload: map[string]any{
					"error": map[string]any{"type": "api_error", "message": failure},
				},
			},
			{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
		},
		domain.StatusTerminated,
		attempt.ID,
		domain.RunAttemptFailed,
		&failure,
		nil,
		nil,
		transcriptDelta,
		mappings,
	)
	if err != nil {
		t.Fatalf("complete interrupted turn: %v", err)
	}
	if completion.Session.Status != domain.StatusIdle {
		t.Fatalf("status = %s, want idle", completion.Session.Status)
	}
	var idle int
	var orphanCustom, completedUse, completedResult bool
	for _, event := range completion.Events {
		switch event.Type {
		case domain.EvSessionError, domain.EvSessionStatusTerminated:
			t.Fatalf("interrupt committed terminal failure event %s", event.Type)
		case domain.EvAgentCustomToolUse:
			if event.ID == "sevt_orphan_custom" {
				orphanCustom = true
			}
		case domain.EvAgentToolUse:
			if event.ID == "sevt_completed_tool" {
				completedUse = true
			}
		case domain.EvAgentToolResult:
			if event.Payload["tool_use_id"] == "sevt_completed_tool" {
				completedResult = true
			}
		case domain.EvSessionStatusIdle:
			idle++
			stopReason, _ := event.Payload["stop_reason"].(map[string]any)
			if stopReason["type"] != "end_turn" {
				t.Fatalf("stop reason = %#v, want end_turn", stopReason)
			}
		}
	}
	if idle != 1 {
		t.Fatalf("idle events = %d, want 1", idle)
	}
	if orphanCustom {
		t.Fatal("interrupt committed an orphan custom tool call")
	}
	if !completedUse || !completedResult {
		t.Fatalf(
			"completed tool pair was not preserved: use=%v result=%v",
			completedUse,
			completedResult,
		)
	}

	var attemptState, stepState string
	if err := store.pool.QueryRow(
		ctx,
		`SELECT state FROM turn_attempts WHERE id = $1`,
		attempt.ID,
	).Scan(&attemptState); err != nil {
		t.Fatalf("attempt state: %v", err)
	}
	if err := store.pool.QueryRow(
		ctx,
		`SELECT state FROM tool_steps WHERE id = $1`,
		step.ID,
	).Scan(&stepState); err != nil {
		t.Fatalf("step state: %v", err)
	}
	if attemptState != string(domain.RunAttemptInterrupted) {
		t.Fatalf("attempt state = %s, want interrupted", attemptState)
	}
	if stepState != string(domain.ToolStepAmbiguous) {
		t.Fatalf("step state = %s, want ambiguous", stepState)
	}
	interrupt, err := store.GetEvent(ctx, sessionID, interruptID)
	if err != nil {
		t.Fatalf("get interrupt: %v", err)
	}
	if interrupt.ProcessedAt == nil {
		t.Fatal("winning interrupt was not marked processed atomically")
	}
	transcript, err := store.LoadProviderTranscript(ctx, sessionID)
	if err != nil {
		t.Fatalf("load provider transcript: %v", err)
	}
	if len(transcript.Messages) != 3 ||
		len(transcript.Messages[2].Content) != 2 {
		t.Fatalf("interrupted provider transcript = %#v", transcript.Messages)
	}
	synthetic := transcript.Messages[2].Content[1]
	if synthetic.ToolResultFor != "provider_orphan" || !synthetic.IsError {
		t.Fatalf("interrupted synthetic result = %#v", synthetic)
	}
	if len(transcript.ToolUseMappings) != 1 ||
		transcript.ToolUseMappings[0].PublicEventID != "sevt_completed_tool" {
		t.Fatalf(
			"interrupted provider mappings = %#v",
			transcript.ToolUseMappings,
		)
	}
}

func TestCompleteWorkflowTurn_InterruptRedirectPublishesIdleThenRunning(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const sessionID = "sess_interrupt_redirect"
	trigger := journalTurn(t, store, sessionID)

	admission, err := store.AdmitEvents(
		ctx,
		sessionID,
		[]domain.EventDraft{
			{Type: domain.EvUserInterrupt, Payload: map[string]any{}},
			{
				Type: domain.EvUserMessage,
				Payload: map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "redirect"}},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("admit interrupt redirect: %v", err)
	}
	interruptID := admission.Events[0].ID
	redirectID := admission.Events[1].ID

	completion, err := store.CompleteWorkflowTurn(
		ctx,
		sessionID,
		trigger,
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
		nil,
	)
	if err != nil {
		t.Fatalf("complete interrupted turn: %v", err)
	}
	if completion.Session.Status != domain.StatusRunning {
		t.Fatalf("status = %s, want running redirect", completion.Session.Status)
	}
	if got := eventTypes(completion.Events); len(got) != 2 ||
		got[0] != domain.EvSessionStatusIdle ||
		got[1] != domain.EvSessionStatusRunning {
		t.Fatalf("completion event order = %v, want idle then running", got)
	}
	interrupt, err := store.GetEvent(ctx, sessionID, interruptID)
	if err != nil || interrupt.ProcessedAt == nil {
		t.Fatalf("interrupt processed = %v err=%v", interrupt.ProcessedAt, err)
	}
	redirect, err := store.GetEvent(ctx, sessionID, redirectID)
	if err != nil {
		t.Fatalf("get redirect: %v", err)
	}
	if redirect.ProcessedAt != nil {
		t.Fatal("redirect was processed by the interrupted turn")
	}
}

func TestCompleteWorkflowTurn_FinishBeforeInterruptStaysCompleted(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const sessionID = "sess_finish_before_interrupt"
	trigger := journalTurn(t, store, sessionID)

	if _, err := store.CompleteWorkflowTurn(
		ctx,
		sessionID,
		trigger,
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
		nil,
	); err != nil {
		t.Fatalf("complete turn: %v", err)
	}
	admission, err := store.AdmitEvents(
		ctx,
		sessionID,
		[]domain.EventDraft{{
			Type: domain.EvUserInterrupt, Payload: map[string]any{},
		}},
	)
	if err != nil {
		t.Fatalf("admit late interrupt: %v", err)
	}
	interruptID := admission.Events[0].ID
	if _, err := store.CompleteWorkflowTurn(
		ctx,
		sessionID,
		interruptID,
		nil,
		domain.StatusIdle,
		"",
		"",
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatalf("acknowledge late interrupt: %v", err)
	}

	events, err := store.EventsAfter(ctx, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	idle := 0
	for _, event := range events {
		if event.Type == domain.EvSessionStatusIdle {
			idle++
		}
	}
	if idle != 1 {
		t.Fatalf("idle events = %d, want original completion only", idle)
	}
}

func eventTypes(events []domain.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}
