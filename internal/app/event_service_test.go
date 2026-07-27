package app

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func newEventService(t *testing.T) *EventService {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	es := store.NewEventStore(db, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1, 0).UTC()})
	return NewEventService(es, NewHub(16))
}

func TestEventService_PersistThenPublish(t *testing.T) {
	s := newEventService(t)
	ch, cancel := s.hub.Subscribe("sesn_1", nil)
	defer cancel()
	ctx := context.Background()
	out, err := s.Append(ctx, "sesn_1", []domain.EventDraft{{Type: domain.EvUserMessage}})
	if err != nil {
		t.Fatal(err)
	}
	// event is in history (committed) ...
	hist, _ := s.History(ctx, "sesn_1", 0, 100)
	if len(hist) != 1 || hist[0].ID != out[0].ID {
		t.Fatalf("history missing committed event: %+v", hist)
	}
	// ... and was published
	select {
	case f := <-ch:
		if f.Event == nil || f.Event.ID != out[0].ID {
			t.Fatalf("published wrong event: %#v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("event not published")
	}
}
