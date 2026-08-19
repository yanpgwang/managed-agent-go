package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

func TestModelRequestUsageIsIdempotentAndBudgetPauseResumes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_budget_pause")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "coordinator",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-opus-4-8"}),
	}
	session.ListCostKnown = true
	session.Budget = &domain.SessionBudget{MaxListCostCents: 1}
	_, err := store.CreateSession(ctx, session, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{
			"type": "text", "text": "run",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 10},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Threads = %+v, err=%v", threads, err)
	}
	primaryID := threads[0].ID
	usage := domain.TokenUsage{InputTokens: 2_000}
	if err := store.AccountModelRequest(
		ctx, session.ID, primaryID, "sevt_request_end", session.AgentSnapshot.Model, usage, "end_turn",
	); err != nil {
		t.Fatal(err)
	}
	// Temporal Activity retries and duplicate completions must not bill twice.
	if err := store.AccountModelRequest(
		ctx, session.ID, primaryID, "sevt_request_end", session.AgentSnapshot.Model, usage, "end_turn",
	); err != nil {
		t.Fatal(err)
	}

	accounted, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accounted.Usage.InputTokens != 2_000 ||
		accounted.ModelListCostNanoUSD != domain.NanoUSDPerCent {
		t.Fatalf("accounted Session = %+v", accounted)
	}
	allowed, err := store.AdmitModelRequest(ctx, session.ID, primaryID)
	if err != nil || allowed {
		t.Fatalf("budget admission = %v, err=%v", allowed, err)
	}
	paused, err := store.GetSession(ctx, session.ID)
	if err != nil || paused.Status != domain.StatusIdle {
		t.Fatalf("paused Session = %+v, err=%v", paused, err)
	}
	primary, err := store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil || !primary.BudgetPaused || primary.Status != domain.StatusIdle ||
		primary.Usage.InputTokens != 2_000 ||
		primary.ModelListCostNanoUSD != domain.NanoUSDPerCent {
		t.Fatalf("paused primary Thread = %+v, err=%v", primary, err)
	}
	events, err := store.QueryEvents(ctx, session.ID, app.EventQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var usageEvent, idleEvent *domain.Event
	for index := range events {
		switch events[index].Type {
		case domain.EvSessionUsage:
			usageEvent = &events[index]
		case domain.EvSessionStatusIdle:
			stopReason, _ := events[index].Payload["stop_reason"].(map[string]any)
			if stopReason["type"] == "budget_reached" {
				idleEvent = &events[index]
			}
		}
	}
	if usageEvent == nil || idleEvent == nil || usageEvent.Sequence+1 != idleEvent.Sequence {
		t.Fatalf("terminal budget events = %+v", events)
	}
	stopReason, _ := idleEvent.Payload["stop_reason"].(map[string]any)
	if stopReason["type"] != "budget_reached" {
		t.Fatalf("budget stop reason = %#v", stopReason)
	}

	_, err = store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		Budget: &domain.SessionBudgetUpdate{Budget: &domain.SessionBudget{MaxListCostCents: 0}},
	})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindValidation {
		t.Fatalf("lower consumed budget update = %v", err)
	}
	resumed, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		Budget: &domain.SessionBudgetUpdate{Budget: &domain.SessionBudget{MaxListCostCents: 2}},
	})
	if err != nil || resumed.Status != domain.StatusRunning ||
		resumed.Budget == nil || resumed.Budget.MaxListCostCents != 2 {
		t.Fatalf("resumed Session = %+v, err=%v", resumed, err)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil || primary.BudgetPaused || primary.Status != domain.StatusRunning {
		t.Fatalf("resumed primary Thread = %+v, err=%v", primary, err)
	}
}

func TestModelRequestUsageAggregatesAcrossIndependentThreads(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_shared_budget")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "coordinator",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-opus-4-8"}),
		Multiagent: &domain.Multiagent{Type: "coordinator", Agents: []domain.AgentReference{{
			Type: "agent", ID: "agent_reviewer", Version: 1,
		}}},
	}
	session.MultiagentRoster = []domain.Agent{{
		ID: "agent_reviewer", Version: 1, Name: "reviewer",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-sonnet-4-5"}),
	}}
	session.ListCostKnown = true
	session.Budget = &domain.SessionBudget{MaxListCostCents: 10}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 10},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Threads = %+v, err=%v", threads, err)
	}
	primaryID := threads[0].ID
	child, _, err := store.CreateChildSessionThread(
		ctx, session.ID, primaryID, "reviewer",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AccountModelRequest(
		ctx, session.ID, primaryID, "sevt_primary_end",
		session.AgentSnapshot.Model, domain.TokenUsage{InputTokens: 1_000}, "end_turn",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AccountModelRequest(
		ctx, session.ID, child.ID, "sevt_child_end",
		child.Agent.Model, domain.TokenUsage{InputTokens: 1_000}, "end_turn",
	); err != nil {
		t.Fatal(err)
	}
	// Request ids are Session-wide idempotency keys, even across Threads.
	if err := store.AccountModelRequest(
		ctx, session.ID, child.ID, "sevt_primary_end",
		child.Agent.Model, domain.TokenUsage{InputTokens: 10_000}, "end_turn",
	); err != nil {
		t.Fatal(err)
	}

	shared, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if shared.Usage.InputTokens != 2_000 || shared.ModelListCostNanoUSD != 8_000_000 {
		t.Fatalf("shared Session usage = %+v", shared)
	}
	primary, err := store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil {
		t.Fatal(err)
	}
	child, err = store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !primary.ListCostKnown || primary.ModelListCostNanoUSD != 5_000_000 ||
		!child.ListCostKnown || child.ModelListCostNanoUSD != 3_000_000 {
		t.Fatalf("Thread list costs: primary=%+v child=%+v", primary, child)
	}

	// Session-owned Agent and archive projections must not copy the aggregate
	// usage or list cost back into the independently accounted primary Thread.
	tools := []any{map[string]any{"type": domain.BuiltinToolsetType}}
	if _, err := store.UpdateSession(ctx, session.ID, domain.SessionUpdate{
		AgentTools: &tools,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveSession(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	primary, err = store.GetSessionThread(ctx, session.ID, primaryID)
	if err != nil {
		t.Fatal(err)
	}
	child, err = store.GetSessionThread(ctx, session.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if primary.Status != domain.StatusTerminated || primary.Usage.InputTokens != 1_000 ||
		primary.ModelListCostNanoUSD != 5_000_000 ||
		child.Usage.InputTokens != 1_000 || child.ModelListCostNanoUSD != 3_000_000 {
		t.Fatalf("projected Thread usage: primary=%+v child=%+v", primary, child)
	}
}

func TestUnknownModelMakesListCostUnknownOnlyAfterItIsUsed(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_unknown_list_cost")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "router",
		Model: domain.NormalizeModel(domain.Model{ID: "router/claude"}),
	}
	session.ListCostKnown = true
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 1},
	)
	if err != nil || len(threads) != 1 || !threads[0].ListCostKnown {
		t.Fatalf("initial Thread = %+v, err=%v", threads, err)
	}
	if err := store.AccountModelRequest(
		ctx, session.ID, threads[0].ID, "sevt_unknown_end",
		session.AgentSnapshot.Model, domain.TokenUsage{InputTokens: 10}, "end_turn",
	); err != nil {
		t.Fatal(err)
	}
	used, err := store.GetSession(ctx, session.ID)
	if err != nil || used.ListCostKnown || used.Usage.InputTokens != 10 {
		t.Fatalf("used unknown model Session = %+v, err=%v", used, err)
	}
}

func TestFableRefusalRecordsUsageWithoutChargingListCost(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_fable_refusal")
	session.AgentSnapshot = domain.Agent{
		ID: session.AgentID, Version: session.AgentVersion, Name: "fable",
		Model: domain.NormalizeModel(domain.Model{ID: "claude-fable-5"}),
	}
	session.ListCostKnown = true
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	threads, err := store.ListSessionThreads(
		ctx, session.ID, app.SessionThreadListQuery{Limit: 1},
	)
	if err != nil || len(threads) != 1 {
		t.Fatalf("primary Thread = %+v, err=%v", threads, err)
	}
	if err := store.AccountModelRequest(
		ctx, session.ID, threads[0].ID, "sevt_refusal_end",
		session.AgentSnapshot.Model, domain.TokenUsage{InputTokens: 1_000}, "refusal",
	); err != nil {
		t.Fatal(err)
	}
	accounted, err := store.GetSession(ctx, session.ID)
	if err != nil || !accounted.ListCostKnown || accounted.Usage.InputTokens != 1_000 ||
		accounted.ModelListCostNanoUSD != 0 {
		t.Fatalf("refused Fable Session = %+v, err=%v", accounted, err)
	}
}
