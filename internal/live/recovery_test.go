package live

import (
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

func TestNATSStreamReconnectsAfterProcessReplacement(t *testing.T) {
	fixture := newLiveTestFixture(t)
	session := createLiveSession(t, fixture, "sesn_live_reconnect")

	firstStream := NewStream(
		fixture.store,
		fixture.broker,
		fixture.ids,
		fixture.clock,
		20*time.Millisecond,
	)
	firstFrames, cancelFirst, err := firstStream.SubscribeContext(
		fixture.ctx,
		session.ID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstAdmission := admitLiveMessage(t, fixture, session.ID, "before restart")
	for _, event := range firstAdmission.Events {
		frame := receiveFrame(t, firstFrames)
		if frame.Event == nil || frame.Event.ID != event.ID {
			t.Fatalf("initial frame = %+v, want event %s", frame, event.ID)
		}
	}
	cancelFirst()
	requireLiveChannelClosed(t, firstFrames)

	// Simulate an API process exiting: its NATS connection and Stream disappear,
	// while PostgreSQL remains authoritative. The event accepted in this gap has
	// no wakeup and must be recovered by the documented open-stream-then-list
	// procedure rather than replayed by the replacement stream.
	fixture.store.SetEventNotifier(nil)
	fixture.broker.Close()
	gapAdmission := admitLiveMessage(t, fixture, session.ID, "during restart")

	replacementBroker, err := Connect(fixture.natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replacementBroker.Close)
	replacementStore := pg.NewStore(fixture.pool, fixture.ids, fixture.clock)
	replacementStore.SetEventNotifier(replacementBroker)
	fixture.store = replacementStore
	replacementStream := NewStream(
		replacementStore,
		replacementBroker,
		fixture.ids,
		fixture.clock,
		20*time.Millisecond,
	)
	replacementFrames, cancelReplacement, err := replacementStream.SubscribeContext(
		fixture.ctx,
		session.ID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelReplacement()

	history, err := fixture.store.EventsAfter(fixture.ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !liveEventsContain(history, gapAdmission.Events[0].ID) {
		t.Fatalf("history omitted restart-gap event %s", gapAdmission.Events[0].ID)
	}
	assertLiveChannelQuiet(t, replacementFrames, 100*time.Millisecond)

	resumed := admitLiveMessage(t, fixture, session.ID, "after restart")
	frame := receiveFrame(t, replacementFrames)
	if frame.Event == nil || frame.Event.ID != resumed.Events[0].ID {
		t.Fatalf("replacement frame = %+v, want event %s", frame, resumed.Events[0].ID)
	}
}

func TestNATSStreamDropsOnlyTheSlowSubscriber(t *testing.T) {
	fixture := newLiveTestFixture(t)
	session := createLiveSession(t, fixture, "sesn_live_backpressure")
	fixture.store.SetEventNotifier(nil)

	initialSubscriptions := fixture.broker.connection.NumSubscriptions()
	slowStream := NewStream(
		fixture.store,
		fixture.broker,
		fixture.ids,
		fixture.clock,
		time.Hour,
	)
	slowStream.bufferSize = 1
	slowFrames, cancelSlow, err := slowStream.SubscribeContext(
		fixture.ctx,
		session.ID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSlow()

	healthyStream := NewStream(
		fixture.store,
		fixture.broker,
		fixture.ids,
		fixture.clock,
		time.Hour,
	)
	healthyFrames, cancelHealthy, err := healthyStream.SubscribeContext(
		fixture.ctx,
		session.ID,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelHealthy()
	if got := fixture.broker.connection.NumSubscriptions(); got != initialSubscriptions+2 {
		t.Fatalf("active subscriptions = %d, want %d", got, initialSubscriptions+2)
	}

	// One admission commits a user.message and session.status_running. The slow
	// stream can buffer only the first; its non-blocking second send must close
	// that subscriber without affecting the independently tailed healthy stream.
	admission := admitLiveMessage(t, fixture, session.ID, "overflow")
	if len(admission.Events) < 2 {
		t.Fatalf("admission events = %d, want at least 2", len(admission.Events))
	}
	if err := fixture.broker.NotifySession(fixture.ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.broker.connection.FlushTimeout(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	for _, event := range admission.Events {
		frame := receiveFrame(t, healthyFrames)
		if frame.Event == nil || frame.Event.ID != event.ID {
			t.Fatalf("healthy frame = %+v, want event %s", frame, event.ID)
		}
	}
	waitForLiveSubscriptionCount(t, fixture.broker, initialSubscriptions+1)

	first, open := <-slowFrames
	if !open || first.Event == nil || first.Event.ID != admission.Events[0].ID {
		t.Fatalf("slow subscriber first frame = %+v, open=%v", first, open)
	}
	if _, open := <-slowFrames; open {
		t.Fatal("slow subscriber remained open after overflowing its buffer")
	}

	ledger, err := fixture.store.EventsAfter(fixture.ctx, session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range admission.Events {
		if !liveEventsContain(ledger, event.ID) {
			t.Errorf("durable ledger omitted event %s after subscriber drop", event.ID)
		}
	}
}

func createLiveSession(t *testing.T, fixture *liveTestFixture, id string) domain.Session {
	t.Helper()
	session := domain.Session{
		ID: id, Status: domain.StatusIdle, Metadata: map[string]any{},
		CreatedAt: fixture.clock.Now(), UpdatedAt: fixture.clock.Now(),
	}
	if _, err := fixture.store.CreateSession(fixture.ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	return session
}

func admitLiveMessage(
	t *testing.T,
	fixture *liveTestFixture,
	sessionID string,
	text string,
) pg.Admission {
	t.Helper()
	admission, err := fixture.store.AdmitEvents(
		fixture.ctx,
		sessionID,
		[]domain.EventDraft{{
			Type: domain.EvUserMessage,
			Payload: map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return admission
}

func requireLiveChannelClosed(t *testing.T, frames <-chan app.Frame) {
	t.Helper()
	select {
	case frame, open := <-frames:
		if open {
			t.Fatalf("stream emitted frame while closing: %+v", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close after cancellation")
	}
}

func assertLiveChannelQuiet(t *testing.T, frames <-chan app.Frame, duration time.Duration) {
	t.Helper()
	select {
	case frame, open := <-frames:
		if !open {
			t.Fatal("replacement stream closed while checking replay behavior")
		}
		t.Fatalf("replacement stream replayed historical frame: %+v", frame)
	case <-time.After(duration):
	}
}

func waitForLiveSubscriptionCount(t *testing.T, broker *Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if broker.connection.NumSubscriptions() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf(
		"active subscriptions = %d, want %d",
		broker.connection.NumSubscriptions(),
		want,
	)
}

func liveEventsContain(events []domain.Event, id string) bool {
	for _, event := range events {
		if event.ID == id {
			return true
		}
	}
	return false
}
