package domain

import "testing"

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
