package domain

import (
	"testing"
	"time"
)

func TestPrimarySessionThreadOwnsIndependentProjection(t *testing.T) {
	created := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	runningSince := created.Add(time.Second)
	session := Session{
		ID: "sesn_projection", Status: StatusRunning,
		AgentSnapshot: Agent{ID: "agent_primary", Version: 2, Name: "primary"},
		Usage:         TokenUsage{InputTokens: 11}, ActiveSeconds: 3,
		RunningSince: &runningSince, CreatedAt: created, UpdatedAt: runningSince,
	}
	thread := NewPrimarySessionThread("sthr_primary", session)
	if thread.ID != "sthr_primary" || thread.SessionID != session.ID ||
		thread.Status != StatusRunning || thread.Agent.ID != "agent_primary" ||
		thread.Usage.InputTokens != 11 || thread.RunningSince == session.RunningSince {
		t.Fatalf("primary projection = %#v", thread)
	}

	archivedAt := created.Add(time.Minute)
	session.AgentSnapshot.Name = "updated"
	session.Status = StatusIdle
	session.ArchivedAt = &archivedAt
	session.UpdatedAt = archivedAt
	thread.ApplyPrimarySessionProjection(session)
	if thread.ID != "sthr_primary" || !thread.CreatedAt.Equal(created) ||
		thread.Status != StatusTerminated || thread.Agent.Name != "updated" ||
		thread.ArchivedAt == nil || !thread.ArchivedAt.Equal(archivedAt) ||
		thread.TerminatedAt == nil || !thread.TerminatedAt.Equal(archivedAt) {
		t.Fatalf("archived primary projection = %#v", thread)
	}
}

func TestIndependentPrimaryProjectionPreservesThreadExecution(t *testing.T) {
	created := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	thread := SessionThread{
		ID: "sthr_primary", SessionID: "sesn_projection",
		Agent: Agent{ID: "agent_primary", Name: "before"}, Status: StatusIdle,
		Usage: TokenUsage{InputTokens: 5}, ModelListCostNanoUSD: 7,
		ListCostKnown: true, BudgetPaused: true, ActiveSeconds: 3,
		CreatedAt: created, UpdatedAt: created,
	}
	session := Session{
		ID: "sesn_projection", Status: StatusIdle,
		AgentSnapshot: Agent{
			ID: "agent_primary", Name: "after", Multiagent: &Multiagent{Type: "coordinator"},
		},
		Usage: TokenUsage{InputTokens: 11}, ModelListCostNanoUSD: 13,
		ListCostKnown: false, ActiveSeconds: 17, UpdatedAt: updated,
	}

	thread.ApplyIndependentPrimarySessionProjection(session)
	if thread.Agent.Name != "after" || thread.Usage.InputTokens != 5 ||
		thread.ModelListCostNanoUSD != 7 || !thread.ListCostKnown ||
		!thread.BudgetPaused || thread.ActiveSeconds != 3 ||
		thread.Status != StatusIdle || !thread.UpdatedAt.Equal(updated) {
		t.Fatalf("independent primary projection = %+v", thread)
	}

	archivedAt := updated.Add(time.Minute)
	session.ArchivedAt = &archivedAt
	session.UpdatedAt = archivedAt
	thread.ApplyIndependentPrimarySessionProjection(session)
	if thread.Status != StatusTerminated || thread.ArchivedAt == nil ||
		!thread.ArchivedAt.Equal(archivedAt) || thread.TerminatedAt == nil ||
		!thread.TerminatedAt.Equal(archivedAt) || thread.Usage.InputTokens != 5 ||
		thread.ModelListCostNanoUSD != 7 || thread.ActiveSeconds != 3 {
		t.Fatalf("archived independent primary projection = %+v", thread)
	}
}

func TestNewChildSessionThreadCapturesIndependentAgentSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 123456789, time.UTC)
	parent := "sthr_primary"
	child := NewChildSessionThread(
		"sthr_child", "sesn_child", parent,
		Agent{
			ID: "agent_reviewer", Version: 3, Name: "reviewer",
			Multiagent: &Multiagent{Type: "coordinator"},
		},
		now,
	)
	if child.ParentThreadID == nil || *child.ParentThreadID != parent ||
		child.Status != StatusIdle || child.Agent.ID != "agent_reviewer" ||
		child.Agent.Multiagent != nil || child.CreatedAt.Nanosecond() != 123456000 {
		t.Fatalf("child Thread = %+v", child)
	}
}
