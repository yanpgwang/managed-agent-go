package pg

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestQueryEventsOrdersAndPagesByProcessedAt(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_event_order")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	early := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)
	created := early.Add(-time.Hour)
	events := []struct {
		id          string
		sequence    int64
		eventType   string
		processedAt *time.Time
	}{
		{id: "late-a", sequence: 1, eventType: domain.EvAgentMessage, processedAt: &late},
		{id: "pending-a", sequence: 2, eventType: domain.EvUserMessage},
		{id: "early", sequence: 3, eventType: domain.EvAgentMessage, processedAt: &early},
		{id: "late-b", sequence: 4, eventType: domain.EvAgentMessage, processedAt: &late},
		{id: "pending-b", sequence: 5, eventType: domain.EvUserMessage},
	}
	for _, event := range events {
		if _, err := store.pool.Exec(ctx, `
INSERT INTO events (id, session_id, seq, type, payload, created_at, processed_at)
VALUES ($1, $2, $3, $4, '{}'::jsonb, $5, $6)`,
			event.id,
			session.ID,
			event.sequence,
			event.eventType,
			created.Add(time.Duration(event.sequence)*time.Second),
			event.processedAt,
		); err != nil {
			t.Fatalf("insert event %s: %v", event.id, err)
		}
	}

	assertQuery := func(name string, query app.EventQuery, want []string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			result, err := store.QueryEvents(ctx, session.ID, query)
			if err != nil {
				t.Fatalf("query events: %v", err)
			}
			got := make([]string, len(result))
			for index := range result {
				got[index] = result[index].ID
			}
			if !equalIDs(got, want) {
				t.Fatalf("ids = %v, want %v", got, want)
			}
		})
	}

	assertQuery("ascending", app.EventQuery{Limit: 10},
		[]string{"early", "late-a", "late-b", "pending-a", "pending-b"})
	assertQuery("descending", app.EventQuery{Limit: 10, Desc: true},
		[]string{"pending-b", "pending-a", "late-b", "late-a", "early"})
	assertQuery("processed filter", app.EventQuery{Limit: 10, ProcessedAtGte: &late},
		[]string{"late-a", "late-b"})
	assertQuery("ascending processed boundary", app.EventQuery{
		Limit: 10,
		Boundary: &app.EventPageBoundary{
			ProcessedAt: &late,
			Sequence:    1,
		},
	}, []string{"late-b", "pending-a", "pending-b"})
	assertQuery("ascending null boundary", app.EventQuery{
		Limit: 10,
		Boundary: &app.EventPageBoundary{
			Sequence: 2,
		},
	}, []string{"pending-b"})
	assertQuery("descending null boundary", app.EventQuery{
		Limit: 10,
		Desc:  true,
		Boundary: &app.EventPageBoundary{
			Sequence: 5,
		},
	}, []string{"pending-a", "late-b", "late-a", "early"})
	assertQuery("descending processed boundary", app.EventQuery{
		Limit: 10,
		Desc:  true,
		Boundary: &app.EventPageBoundary{
			ProcessedAt: &late,
			Sequence:    4,
		},
	}, []string{"late-a", "early"})
}
