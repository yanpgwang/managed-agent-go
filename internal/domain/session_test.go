package domain

import (
	"testing"
	"time"
)

func TestStatusTransitions(t *testing.T) {
	ok := [][2]Status{
		{StatusIdle, StatusRunning}, {StatusRunning, StatusIdle},
		{StatusRunning, StatusRescheduling}, {StatusRescheduling, StatusRunning},
		{StatusIdle, StatusTerminated}, {StatusRunning, StatusTerminated},
	}
	for _, p := range ok {
		if !p[0].CanTransitionTo(p[1]) {
			t.Errorf("expected %s->%s allowed", p[0], p[1])
		}
	}
	bad := [][2]Status{
		{StatusTerminated, StatusIdle}, {StatusTerminated, StatusRunning},
		{StatusIdle, StatusRescheduling},
	}
	for _, p := range bad {
		if p[0].CanTransitionTo(p[1]) {
			t.Errorf("expected %s->%s rejected", p[0], p[1])
		}
	}
}

func TestSessionStatsTrackActiveAndFreezeOnTermination(t *testing.T) {
	created := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	s := Session{Status: StatusIdle, CreatedAt: created, UpdatedAt: created}
	s.TransitionStatus(StatusRunning, created.Add(2*time.Second))

	active, duration := s.ObservableStats(created.Add(7 * time.Second))
	if active != 5 || duration != 7 {
		t.Fatalf("running stats = (%v, %v), want (5, 7)", active, duration)
	}

	s.TransitionStatus(StatusIdle, created.Add(10*time.Second))
	s.TransitionStatus(StatusRunning, created.Add(13*time.Second))
	s.TransitionStatus(StatusTerminated, created.Add(17*time.Second))
	active, duration = s.ObservableStats(created.Add(time.Hour))
	if active != 12 || duration != 17 {
		t.Fatalf("terminated stats = (%v, %v), want (12, 17)", active, duration)
	}
}

func TestTokenUsageAdd(t *testing.T) {
	u := TokenUsage{InputTokens: 10, OutputTokens: 2}
	u.Add(TokenUsage{
		CacheCreation:        CacheCreationUsage{Ephemeral1hInputTokens: 3, Ephemeral5mInputTokens: 4},
		CacheReadInputTokens: 5,
		InputTokens:          6,
		OutputTokens:         7,
	})
	if u.InputTokens != 16 || u.OutputTokens != 9 || u.CacheReadInputTokens != 5 ||
		u.CacheCreation.Ephemeral1hInputTokens != 3 || u.CacheCreation.Ephemeral5mInputTokens != 4 {
		t.Fatalf("summed usage = %#v", u)
	}
}
