package pg

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestCoordinatorDelegationRunsAsPersistentIndependentThread(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_coordinator_runtime")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "coordinator",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-test"}),
		Multiagent: &domain.Multiagent{Type: "coordinator", Agents: []domain.AgentReference{{
			Type: "agent", ID: "agent_reviewer", Version: 3,
		}}},
	}
	description := "Reviews a focused change."
	session.MultiagentRoster = []domain.Agent{{
		ID: "agent_reviewer", Version: 3, Name: "reviewer",
		Description: &description,
		Model:       domain.NormalizeModel(domain.Model{ID: "claude-reviewer"}),
	}}
	created, err := store.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{
			"type": "text", "text": "delegate the review",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var trigger domain.Event
	for _, event := range created.Events {
		if event.Type == domain.EvUserMessage {
			trigger = event
		}
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 10},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Threads = %+v, err=%v", threads, err)
	}
	primary := threads[0]

	if _, err := store.EnsureAttempt(ctx, session.ID, trigger.ID, "ratm_delegate"); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"agent_name": "reviewer", "message": "Review internal/auth.go and report issues.",
	}
	if _, err := store.EnsureToolStep(
		ctx, "ratm_delegate", "tstep_delegate", 0,
		"sevt_private_delegate", agentruntime.SendToAgentToolName, input,
	); err != nil {
		t.Fatal(err)
	}
	executed, err := store.ExecuteCoordinatorToolStep(
		ctx, session.ID, primary.ID, trigger.ID, "tstep_delegate",
		agentruntime.SendToAgentToolName, input,
	)
	if err != nil || executed.Result.IsError || executed.WakeThreadID == "" {
		t.Fatalf("delegation = %+v, err=%v", executed, err)
	}
	threads, err = store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 10},
	)
	if err != nil || len(threads) != 2 {
		t.Fatalf("delegated Threads = %+v, err=%v", threads, err)
	}
	child := threads[1]
	if child.ID != executed.WakeThreadID || child.Status != domain.StatusRunning ||
		child.Agent.Name != "reviewer" {
		t.Fatalf("child projection = %+v", child)
	}
	if _, err := store.EnsureToolStep(
		ctx, "ratm_delegate", "tstep_list_agents", 1,
		"sevt_private_list_agents", agentruntime.ListAgentsToolName,
		map[string]any{},
	); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ExecuteCoordinatorToolStep(
		ctx, session.ID, primary.ID, trigger.ID, "tstep_list_agents",
		agentruntime.ListAgentsToolName, map[string]any{},
	)
	if err != nil || listed.Result.IsError || len(listed.Result.Content) != 1 {
		t.Fatalf("list_agents = %+v, err=%v", listed, err)
	}
	listedBlock, _ := listed.Result.Content[0].(map[string]any)
	listedJSON, _ := listedBlock["text"].(string)
	var roster struct {
		Agents []struct {
			Name    string `json:"name"`
			Threads []struct {
				ID     string `json:"session_thread_id"`
				Status string `json:"status"`
			} `json:"threads"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(listedJSON), &roster); err != nil {
		t.Fatal(err)
	}
	if len(roster.Agents) != 1 || roster.Agents[0].Name != "reviewer" ||
		len(roster.Agents[0].Threads) != 1 ||
		roster.Agents[0].Threads[0].ID != child.ID ||
		roster.Agents[0].Threads[0].Status != string(domain.StatusRunning) {
		t.Fatalf("list_agents roster = %+v", roster)
	}
	if err := store.RecordWorkflowRetry(
		ctx, session.ID, trigger.ID,
		"sevt_primary_retry_error", "sevt_primary_rescheduled",
		map[string]any{
			"type": "model_overloaded_error", "message": "retry",
			"retry_status": map[string]any{"type": "retrying"},
		},
	); err != nil {
		t.Fatal(err)
	}
	duringPrimaryRetry, err := store.GetSession(ctx, session.ID)
	if err != nil || duringPrimaryRetry.Status != domain.StatusRunning {
		t.Fatalf("aggregate during coordinator retry = %+v, err=%v", duringPrimaryRetry, err)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || primary.Status != domain.StatusRescheduling {
		t.Fatalf("rescheduling primary = %+v, err=%v", primary, err)
	}
	if err := store.ResumeWorkflowRetry(
		ctx, session.ID, trigger.ID, "sevt_primary_retry_running",
	); err != nil {
		t.Fatal(err)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || primary.Status != domain.StatusRunning {
		t.Fatalf("resumed primary = %+v, err=%v", primary, err)
	}
	childEvents, err := store.ThreadEventsAfter(ctx, session.ID, child.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	childTrigger := eventOfType(t, childEvents, domain.EvAgentThreadMessageReceived)
	eventOfType(t, childEvents, domain.EvSessionThreadStatusRunning)
	primaryEvents, err := store.QueryEvents(
		ctx, session.ID, app.EventQuery{Limit: 30},
	)
	if err != nil {
		t.Fatal(err)
	}
	eventOfType(t, primaryEvents, domain.EvSessionThreadCreated)
	eventOfType(t, primaryEvents, domain.EvAgentThreadMessageSent)
	eventOfType(t, primaryEvents, domain.EvSessionThreadStatusRunning)
	var childWakeups int
	if err := store.pool.QueryRow(ctx, `
SELECT COUNT(*) FROM thread_orchestration_outbox
WHERE session_id = $1 AND thread_id = $2`, session.ID, child.ID).Scan(&childWakeups); err != nil || childWakeups != 1 {
		t.Fatalf("child wakeup count = %d, err=%v", childWakeups, err)
	}
	if err := store.RecordThreadWorkflowRetry(
		ctx, session.ID, child.ID, childTrigger.ID,
		"sevt_child_retry_error", "sevt_child_rescheduled",
		map[string]any{
			"type": "model_overloaded_error", "message": "retry",
			"retry_status": map[string]any{"type": "retrying"},
		},
	); err != nil {
		t.Fatal(err)
	}
	child, err = store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil || child.Status != domain.StatusRescheduling {
		t.Fatalf("rescheduling child = %+v, err=%v", child, err)
	}
	duringRetry, err := store.GetSession(ctx, session.ID)
	if err != nil || duringRetry.Status != domain.StatusRunning {
		t.Fatalf("aggregate during child retry = %+v, err=%v", duringRetry, err)
	}
	if err := store.ResumeThreadWorkflowRetry(
		ctx, session.ID, child.ID, childTrigger.ID, "sevt_child_retry_running",
	); err != nil {
		t.Fatal(err)
	}
	child, err = store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil || child.Status != domain.StatusRunning {
		t.Fatalf("resumed child = %+v, err=%v", child, err)
	}

	completed, err := store.CompleteWorkflowTurnWithUsage(
		ctx, session.ID, trigger.ID,
		[]domain.EventDraft{{
			Type:    domain.EvSessionStatusIdle,
			Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
		}},
		domain.StatusIdle, "ratm_delegate", domain.RunAttemptCompleted,
		nil, nil, nil, domain.TokenUsage{InputTokens: 5},
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Session.Status != domain.StatusRunning {
		t.Fatalf("aggregate status after coordinator wait = %s", completed.Session.Status)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || primary.Status != domain.StatusIdle || primary.Usage.InputTokens != 5 {
		t.Fatalf("independent primary after delegation = %+v, err=%v", primary, err)
	}

	if _, err := store.EnsureAttempt(
		ctx, session.ID, childTrigger.ID, "ratm_child_report",
	); err != nil {
		t.Fatal(err)
	}
	report := []any{map[string]any{"type": "text", "text": "No blocking issues found."}}
	childCompletion, err := store.CompleteThreadWorkflowTurn(
		ctx, session.ID, child.ID, childTrigger.ID,
		[]domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": report}},
			{Type: domain.EvSessionThreadStatusIdle, Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			}},
		},
		domain.StatusIdle, "ratm_child_report", domain.RunAttemptCompleted,
		nil, nil, nil, nil, nil, domain.TokenUsage{OutputTokens: 7},
	)
	if err != nil {
		t.Fatal(err)
	}
	if childCompletion.Session.Status != domain.StatusRunning {
		t.Fatalf("aggregate status after report = %s", childCompletion.Session.Status)
	}
	child, err = store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil || child.Status != domain.StatusIdle || child.Usage.OutputTokens != 7 {
		t.Fatalf("completed child = %+v, err=%v", child, err)
	}
	childEvents, err = store.ThreadEventsAfter(ctx, session.ID, child.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	eventOfType(t, childEvents, domain.EvAgentThreadMessageSent)
	if hasEventType(childEvents, domain.EvAgentMessage) {
		t.Fatalf("child report must not be duplicated as agent.message: %+v", childEvents)
	}
	primaryEvents, err = store.QueryEvents(
		ctx, session.ID, app.EventQuery{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	primaryReport := eventOfType(t, primaryEvents, domain.EvAgentThreadMessageReceived)
	if primaryReport.Payload["from_session_thread_id"] != child.ID {
		t.Fatalf("primary report = %+v", primaryReport)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || primary.Status != domain.StatusRunning {
		t.Fatalf("woken primary = %+v, err=%v", primary, err)
	}

	if _, err := store.EnsureAttempt(
		ctx, session.ID, primaryReport.ID, "ratm_primary_synthesis",
	); err != nil {
		t.Fatal(err)
	}
	final, err := store.CompleteWorkflowTurnWithUsage(
		ctx, session.ID, primaryReport.ID,
		[]domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": report}},
			{Type: domain.EvSessionStatusIdle, Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			}},
		},
		domain.StatusIdle, "ratm_primary_synthesis", domain.RunAttemptCompleted,
		nil, nil, nil, domain.TokenUsage{OutputTokens: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if final.Session.Status != domain.StatusIdle {
		t.Fatalf("final aggregate status = %s", final.Session.Status)
	}

	// A child report that races with a coordinator model retry must queue the
	// later synthesis turn without canceling the coordinator's rescheduling
	// ownership. Once the child idles, the Session aggregate is rescheduling
	// until that exact coordinator retry resumes.
	childCursor := childEvents[len(childEvents)-1].Sequence
	followup, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{
			"type": "text", "text": "delegate one more check",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	followupTrigger := eventOfType(t, followup.Events, domain.EvUserMessage)
	if _, err := store.EnsureAttempt(
		ctx, session.ID, followupTrigger.ID, "ratm_followup_delegate",
	); err != nil {
		t.Fatal(err)
	}
	followupInput := map[string]any{
		"agent_name": "reviewer", "message": "Check the final diff again.",
		"session_thread_id": child.ID,
	}
	if _, err := store.EnsureToolStep(
		ctx, "ratm_followup_delegate", "tstep_followup_delegate", 0,
		"sevt_private_followup", agentruntime.SendToAgentToolName,
		followupInput,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecuteCoordinatorToolStep(
		ctx, session.ID, primary.ID, followupTrigger.ID,
		"tstep_followup_delegate", agentruntime.SendToAgentToolName,
		followupInput,
	); err != nil {
		t.Fatal(err)
	}
	followupChildEvents, err := store.ThreadEventsAfter(
		ctx, session.ID, child.ID, childCursor, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	followupChildTrigger := eventOfType(
		t, followupChildEvents, domain.EvAgentThreadMessageReceived,
	)
	if _, err := store.EnsureAttempt(
		ctx, session.ID, followupChildTrigger.ID, "ratm_followup_child",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkflowRetry(
		ctx, session.ID, followupTrigger.ID,
		"sevt_followup_retry_error", "sevt_followup_rescheduled",
		map[string]any{
			"type": "model_overloaded_error", "message": "retry",
			"retry_status": map[string]any{"type": "retrying"},
		},
	); err != nil {
		t.Fatal(err)
	}
	raceReport := []any{map[string]any{"type": "text", "text": "Follow-up complete."}}
	raceCompletion, err := store.CompleteThreadWorkflowTurn(
		ctx, session.ID, child.ID, followupChildTrigger.ID,
		[]domain.EventDraft{
			{Type: domain.EvAgentMessage, Payload: map[string]any{"content": raceReport}},
			{Type: domain.EvSessionThreadStatusIdle, Payload: map[string]any{
				"stop_reason": map[string]any{"type": "end_turn"},
			}},
		},
		domain.StatusIdle, "ratm_followup_child", domain.RunAttemptCompleted,
		nil, nil, nil, nil, nil, domain.TokenUsage{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if raceCompletion.Session.Status != domain.StatusRescheduling {
		t.Fatalf("aggregate after racing child report = %s", raceCompletion.Session.Status)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primary.ID)
	if err != nil || primary.Status != domain.StatusRescheduling {
		t.Fatalf("coordinator retry was overwritten by child report: %+v, err=%v", primary, err)
	}
	primaryEvents, err = store.QueryEvents(
		ctx, session.ID, app.EventQuery{Limit: 100},
	)
	if err != nil {
		t.Fatal(err)
	}
	eventOfType(t, primaryEvents, domain.EvSessionStatusRescheduling)
	if err := store.ResumeWorkflowRetry(
		ctx, session.ID, followupTrigger.ID, "sevt_followup_retry_running",
	); err != nil {
		t.Fatal(err)
	}
	resumedSession, err := store.GetSession(ctx, session.ID)
	if err != nil || resumedSession.Status != domain.StatusRunning {
		t.Fatalf("aggregate after coordinator retry resume = %+v, err=%v", resumedSession, err)
	}
}

func hasEventType(events []domain.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func eventOfType(t *testing.T, events []domain.Event, eventType string) domain.Event {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("event %s not found in %+v", eventType, events)
	return domain.Event{}
}
