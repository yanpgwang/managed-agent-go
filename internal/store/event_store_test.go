package store

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func newEventStore(t *testing.T) *EventStore {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEventStore(db, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1000, 0).UTC()})
}

func TestAppend_StrictlyIncreasingSeq(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	out, err := s.Append(ctx, "sesn_1", []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"x": 1}},
		{Type: domain.EvAgentMessage, Payload: map[string]any{"y": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Sequence != 1 || out[1].Sequence != 2 {
		t.Fatalf("seq: %d,%d", out[0].Sequence, out[1].Sequence)
	}
	more, _ := s.Append(ctx, "sesn_1", []domain.EventDraft{{Type: domain.EvSessionStatusIdle}})
	if more[0].Sequence != 3 {
		t.Fatalf("expected seq 3, got %d", more[0].Sequence)
	}
	// separate session restarts at 1
	other, _ := s.Append(ctx, "sesn_2", []domain.EventDraft{{Type: domain.EvUserMessage}})
	if other[0].Sequence != 1 {
		t.Fatalf("expected seq 1 for new session, got %d", other[0].Sequence)
	}
}

func TestAppend_ProcessedOnReceipt(t *testing.T) {
	s := newEventStore(t)
	out, _ := s.Append(context.Background(), "sesn_1", []domain.EventDraft{
		{Type: domain.EvUserCustomToolResult},
		{Type: domain.EvUserMessage},
		{Type: domain.EvAgentMessage},
		{Type: domain.EvSessionStatusIdle},
	})
	if out[0].ProcessedAt == nil {
		t.Fatal("custom_tool_result should be processed on receipt")
	}
	if out[1].ProcessedAt != nil {
		t.Fatal("user.message should be queued (processed_at nil)")
	}
	for i, event := range out[2:] {
		if event.ProcessedAt == nil {
			t.Fatalf("server event %d (%s) should be processed on persist", i+2, event.Type)
		}
	}
}

// TestHistoryTail_ReturnsNewestWindowInOrder proves that when a session has more
// events than the projection window, HistoryTail returns the NEWEST events (not
// the oldest) and keeps them in ascending chronological order. This is the
// regression guard for the historyProjectionLimit bug, where afterSeq=0 + LIMIT
// projected the OLDEST window. A small limit stands in for the production
// 10000 ceiling so the test stays fast and needs no bulk inserts.
func TestHistoryTail_ReturnsNewestWindowInOrder(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	const sessionID = "sesn_tail"

	// Append 10 user messages numbered 0..9 in order.
	drafts := make([]domain.EventDraft, 10)
	for i := range drafts {
		drafts[i] = domain.EventDraft{Type: domain.EvUserMessage, Payload: map[string]any{"n": i}}
	}
	if _, err := s.Append(ctx, sessionID, drafts); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Ask for the newest 3.
	tail, err := s.HistoryTail(ctx, sessionID, 3)
	if err != nil {
		t.Fatalf("HistoryTail: %v", err)
	}
	if len(tail) != 3 {
		t.Fatalf("HistoryTail len = %d; want 3", len(tail))
	}
	// Must be the newest three (n=7,8,9) in ascending sequence order, NOT the
	// oldest three (n=0,1,2) that the old afterSeq=0 projection returned.
	wantN := []float64{7, 8, 9}
	for i, ev := range tail {
		if ev.Payload["n"] != wantN[i] {
			t.Fatalf("tail[%d].n = %v; want %v (newest window, chronological order): full=%+v",
				i, ev.Payload["n"], wantN[i], tail)
		}
		if i > 0 && tail[i-1].Sequence >= ev.Sequence {
			t.Fatalf("tail not ascending by seq at %d: %d then %d", i, tail[i-1].Sequence, ev.Sequence)
		}
	}

	// A limit larger than the total returns everything, still chronological.
	full, err := s.HistoryTail(ctx, sessionID, 100)
	if err != nil {
		t.Fatalf("HistoryTail(100): %v", err)
	}
	if len(full) != 10 || full[0].Payload["n"] != float64(0) || full[9].Payload["n"] != float64(9) {
		t.Fatalf("HistoryTail(100) = %d events, first=%v last=%v; want 10, 0..9",
			len(full), full[0].Payload["n"], full[len(full)-1].Payload["n"])
	}
}

func TestList_RoundTripAndAfterSeq(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	const sessionID = "sesn_list"

	// Append 3 events: two plain types, one ProcessedOnReceipt.
	_, err := s.Append(ctx, sessionID, []domain.EventDraft{
		{Type: domain.EvUserMessage, Payload: map[string]any{"text": "hi", "n": 42}},
		{Type: domain.EvAgentMessage, Payload: map[string]any{"text": "hello"}},
		{Type: domain.EvUserCustomToolResult, Payload: map[string]any{"result": "ok"}},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// List all events (afterSeq=0, limit=0).
	all, err := s.History(ctx, sessionID, 0, 0)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}

	// Assert seq ordering.
	for i, ev := range all {
		if ev.Sequence != int64(i+1) {
			t.Errorf("event[%d]: expected seq %d, got %d", i, i+1, ev.Sequence)
		}
	}

	// Assert payload round-trip: JSON numbers come back as float64.
	ev0 := all[0]
	if ev0.Payload["text"] != "hi" {
		t.Errorf("payload text: got %v", ev0.Payload["text"])
	}
	if ev0.Payload["n"].(float64) != 42 {
		t.Errorf("payload n: got %v", ev0.Payload["n"])
	}

	// ProcessedAt: user.message -> nil; server events and selected client
	// acknowledgements -> non-nil.
	if all[0].ProcessedAt != nil {
		t.Error("user.message should have nil ProcessedAt after List")
	}
	if all[1].ProcessedAt == nil {
		t.Error("agent.message should have non-nil ProcessedAt after List")
	}
	if all[2].ProcessedAt == nil {
		t.Error("user.custom_tool_result should have non-nil ProcessedAt after List")
	}

	// List with afterSeq=1: should return only seq 2 and 3.
	tail, err := s.History(ctx, sessionID, 1, 0)
	if err != nil {
		t.Fatalf("List afterSeq=1: %v", err)
	}
	if len(tail) != 2 {
		t.Fatalf("afterSeq=1: expected 2 events, got %d", len(tail))
	}
	if tail[0].Sequence != 2 || tail[1].Sequence != 3 {
		t.Errorf("afterSeq=1 seqs: %d, %d", tail[0].Sequence, tail[1].Sequence)
	}
}

func TestQuery_CreatedAtNamedFilterUsesProcessedAt(t *testing.T) {
	s := newEventStore(t)
	ctx := context.Background()
	_, err := s.Append(ctx, "sesn_filter", []domain.EventDraft{
		{Type: domain.EvUserMessage},
		{Type: domain.EvAgentMessage},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound := time.Unix(999, 0).UTC()
	got, err := s.Query(ctx, "sesn_filter", EventQuery{CreatedAtGt: &bound, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != domain.EvAgentMessage {
		t.Fatalf("filter should exclude unprocessed user event and include processed server event: %+v", got)
	}
}

func TestQuery_ProcessedAtFiltersExactAndFractionalWithinSecond(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	ids := domain.NewSeqIDGen()
	exact := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	fractional := exact.Add(100 * time.Millisecond)
	for _, at := range []time.Time{exact, fractional} {
		store := NewEventStore(db, ids, domain.FixedClock{T: at})
		if _, err := store.Append(ctx, "sesn_filter", []domain.EventDraft{
			{Type: domain.EvAgentMessage},
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := NewEventStore(db, ids, domain.FixedClock{T: fractional})

	tests := []struct {
		name  string
		query EventQuery
		want  []time.Time
	}{
		{
			name:  "gt exact",
			query: EventQuery{CreatedAtGt: &exact},
			want:  []time.Time{fractional},
		},
		{
			name:  "gte exact",
			query: EventQuery{CreatedAtGte: &exact},
			want:  []time.Time{exact, fractional},
		},
		{
			name:  "lt fractional",
			query: EventQuery{CreatedAtLt: &fractional},
			want:  []time.Time{exact},
		},
		{
			name:  "lte fractional",
			query: EventQuery{CreatedAtLte: &fractional},
			want:  []time.Time{exact, fractional},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.query.Limit = 10
			got, err := store.Query(ctx, "sesn_filter", tt.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, want := range tt.want {
				if got[i].ProcessedAt == nil || !got[i].ProcessedAt.Equal(want) {
					t.Fatalf("event %d processed_at = %v, want %s", i, got[i].ProcessedAt, want)
				}
			}
		})
	}
}
