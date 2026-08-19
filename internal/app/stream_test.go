package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestHub_DeliversToSubscriber(t *testing.T) {
	h := NewHub(4)
	ch, cancel := h.Subscribe("sesn_1", nil)
	defer cancel()
	h.Publish("sesn_1", domain.Event{ID: "sevt_1", Sequence: 1})
	select {
	case f := <-ch:
		if f.Event == nil || f.Event.ID != "sevt_1" {
			t.Fatalf("got %#v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_PreviewOnlyToOptedIn(t *testing.T) {
	h := NewHub(8)
	optedCh, cancelA := h.Subscribe("s1", map[string]bool{"agent.message": true})
	defer cancelA()
	plainCh, cancelB := h.Subscribe("s1", nil)
	defer cancelB()

	h.PublishPreview("s1", domain.PreviewFrame{Kind: domain.PreviewEventStart, EventID: "e1", EventType: "agent.message"})
	h.Publish("s1", domain.Event{ID: "e1", Type: domain.EvAgentMessage})

	// opted-in sub sees preview then persisted event
	f1 := <-optedCh
	if f1.Preview == nil || f1.Preview.Kind != domain.PreviewEventStart {
		t.Fatalf("opted sub frame1 = %#v, want preview start", f1)
	}
	f2 := <-optedCh
	if f2.Event == nil || f2.Event.ID != "e1" {
		t.Fatalf("opted sub frame2 = %#v, want persisted event", f2)
	}
	// plain sub sees ONLY the persisted event (no preview)
	f := <-plainCh
	if f.Event == nil || f.Event.ID != "e1" {
		t.Fatalf("plain sub frame = %#v, want persisted event only", f)
	}
	select {
	case extra := <-plainCh:
		t.Fatalf("plain sub got unexpected extra frame %#v", extra)
	default:
	}
}

func TestHub_SlowSubscriberDroppedNotBlocking(t *testing.T) {
	h := NewHub(1) // tiny buffer
	ch, _ := h.Subscribe("sesn_1", nil)
	// overflow without reading: publisher must not block
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Publish("sesn_1", domain.Event{ID: "sevt", Sequence: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on slow subscriber")
	}
	// slow subscriber channel is eventually closed
	drainClosed := false
	deadline := time.After(time.Second)
	for !drainClosed {
		select {
		case _, ok := <-ch:
			if !ok {
				drainClosed = true
			}
		case <-deadline:
			t.Fatal("slow subscriber channel never closed")
		}
	}
}

func TestHub_OtherSubscriberUnaffected(t *testing.T) {
	// Use a tiny buffer so the slow subscriber overflows quickly.
	h := NewHub(1)

	// slow: subscribe but never read — will overflow after the first event fills
	// its 1-slot buffer and get dropped on the second publish.
	_, _ = h.Subscribe("sesn_1", nil)

	fast, cancel := h.Subscribe("sesn_1", nil)
	defer cancel()

	// Publish 5 events; slow subscriber overflows and is dropped, fast should
	// receive at least the first event (delivered in order) without being affected.
	const n = 5
	for i := 1; i <= n; i++ {
		h.Publish("sesn_1", domain.Event{ID: fmt.Sprintf("sevt_%d", i), Sequence: int64(i)})
	}

	// Assert the fast subscriber received the first event correctly.
	select {
	case f := <-fast:
		if f.Event == nil || f.Event.Sequence != 1 {
			t.Fatalf("expected first event (seq=1), got %#v", f)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy subscriber missed event while slow co-subscriber was being dropped")
	}
}

func TestHub_CloseSessionDeliversTerminalThenCloses(t *testing.T) {
	h := NewHub(4)
	ch, cancel := h.Subscribe("sesn_1", nil)
	defer cancel()
	terminal := domain.Event{ID: "sevt_deleted", Type: domain.EvSessionDeleted}

	h.CloseSession("sesn_1", terminal)

	if got, ok := <-ch; !ok || got.Event == nil || got.Event.ID != terminal.ID {
		t.Fatalf("terminal delivery: got=%#v open=%v", got, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscriber remained open after terminal event")
	}
}
