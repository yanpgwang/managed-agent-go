package live

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

var liveSchemaSequence atomic.Int64

type liveTestFixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	broker  *Broker
	store   *pg.Store
	ids     *liveIDs
	clock   *liveClock
	natsURL string
}

func newLiveTestFixture(t *testing.T) *liveTestFixture {
	t.Helper()
	databaseURL := os.Getenv("MANAGED_AGENT_TEST_DATABASE_URL")
	natsURL := os.Getenv("MANAGED_AGENT_TEST_NATS_URL")
	if databaseURL == "" || natsURL == "" {
		t.Skip("PostgreSQL/NATS integration environment is not configured")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "live_" + liveSafeName(t.Name()) + "_" +
		liveIntString(liveSchemaSequence.Add(1))
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})

	broker, err := Connect(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	ids := &liveIDs{}
	clock := &liveClock{}
	store := pg.NewStore(pool, ids, clock)
	store.SetEventNotifier(broker)
	return &liveTestFixture{
		ctx: ctx, pool: pool, broker: broker, store: store,
		ids: ids, clock: clock, natsURL: natsURL,
	}
}

func TestNATSStreamReconcilesLedgerAndCarriesPreviews(t *testing.T) {
	fixture := newLiveTestFixture(t)
	ctx := fixture.ctx
	broker := fixture.broker
	store := fixture.store
	ids := fixture.ids
	clock := fixture.clock
	session := domain.Session{
		ID: "sesn_live", Status: domain.StatusIdle, Metadata: map[string]any{},
		CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}

	stream := NewStream(store, broker, ids, clock, 20*time.Millisecond)
	frames, cancel, err := stream.SubscribeContext(
		ctx,
		session.ID,
		map[string]bool{domain.EvAgentMessage: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	admission, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserDefineOutcome,
		Payload: map[string]any{
			"description": "done",
			"rubric":      map[string]any{"type": "text", "content": "ok"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, admitted := range admission.Events {
		frame := receiveFrame(t, frames)
		if frame.Event == nil || frame.Event.ID != admitted.ID {
			t.Fatalf("ledger frame = %+v, want event %s", frame, admitted.ID)
		}
	}

	preview := domain.PreviewFrame{
		Kind: domain.PreviewEventDelta, EventID: "sevt_preview",
		EventType: domain.EvAgentMessage, Index: 0, Text: "hel",
	}
	if err := broker.PublishPreview(ctx, session.ID, preview); err != nil {
		t.Fatal(err)
	}
	frame := receiveFrame(t, frames)
	if frame.Preview == nil || frame.Preview.Text != preview.Text {
		t.Fatalf("preview frame = %+v, want %+v", frame, preview)
	}

	// A preview may win the local select race against the persisted-event wakeup.
	// Disable wakeups and use a deliberately long reconciliation interval to
	// prove the first preview frame itself catches up the authoritative cursor.
	orderedSession := domain.Session{
		ID: "sesn_live_preview_order", Status: domain.StatusIdle,
		Metadata: map[string]any{}, CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	}
	if _, err := store.CreateSession(ctx, orderedSession, nil); err != nil {
		t.Fatal(err)
	}
	orderedStream := NewStream(store, broker, ids, clock, time.Hour)
	orderedFrames, cancelOrdered, err := orderedStream.SubscribeContext(
		ctx,
		orderedSession.ID,
		map[string]bool{domain.EvAgentMessage: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelOrdered()
	orderedAdmission, err := store.AdmitEvents(ctx, orderedSession.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "stream"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, admitted := range orderedAdmission.Events {
		ordered := receiveFrame(t, orderedFrames)
		if ordered.Event == nil || ordered.Event.ID != admitted.ID {
			t.Fatalf("ordered admission frame = %+v, want event %s", ordered, admitted.ID)
		}
	}
	store.SetEventNotifier(nil)
	start := domain.EventDraft{
		ID: "sevt_live_model_start", Type: domain.EvSpanModelRequestStart,
		Payload: map[string]any{},
	}
	if err := store.AppendWorkflowEvents(
		ctx,
		orderedSession.ID,
		orderedAdmission.Events[0].ID,
		[]domain.EventDraft{start},
	); err != nil {
		t.Fatal(err)
	}
	orderedPreview := domain.PreviewFrame{
		Kind: domain.PreviewEventDelta, EventID: "sevt_live_preview_order",
		EventType: domain.EvAgentMessage, ModelRequestStartID: start.ID,
		Index: 0, Text: "first delta",
	}
	if err := broker.PublishPreview(ctx, orderedSession.ID, orderedPreview); err != nil {
		t.Fatal(err)
	}
	ordered := receiveFrame(t, orderedFrames)
	if ordered.Event == nil || ordered.Event.ID != start.ID {
		t.Fatalf("frame before first preview = %+v, want start event %s", ordered, start.ID)
	}
	ordered = receiveFrame(t, orderedFrames)
	if ordered.Preview == nil || ordered.Preview.Text != orderedPreview.Text {
		t.Fatalf("frame after model start = %+v, want preview %+v", ordered, orderedPreview)
	}
	store.SetEventNotifier(broker)
	end := domain.EventDraft{
		ID: "sevt_live_model_end", Type: domain.EvSpanModelRequestEnd,
		Payload: map[string]any{
			"model_request_start_id": start.ID,
			"is_error":               true,
		},
	}
	if err := store.AppendWorkflowEvents(
		ctx,
		orderedSession.ID,
		orderedAdmission.Events[0].ID,
		[]domain.EventDraft{end},
	); err != nil {
		t.Fatal(err)
	}
	ordered = receiveFrame(t, orderedFrames)
	if ordered.Event == nil || ordered.Event.ID != end.ID {
		t.Fatalf("preview-closing frame = %+v, want end event %s", ordered, end.ID)
	}
	nextStart := domain.EventDraft{
		ID: "sevt_live_next_model_start", Type: domain.EvSpanModelRequestStart,
		Payload: map[string]any{},
	}
	if err := store.AppendWorkflowEvents(
		ctx,
		orderedSession.ID,
		orderedAdmission.Events[0].ID,
		[]domain.EventDraft{nextStart},
	); err != nil {
		t.Fatal(err)
	}
	ordered = receiveFrame(t, orderedFrames)
	if ordered.Event == nil || ordered.Event.ID != nextStart.ID {
		t.Fatalf("next model start frame = %+v, want %s", ordered, nextStart.ID)
	}
	if err := broker.PublishPreview(ctx, orderedSession.ID, domain.PreviewFrame{
		Kind: domain.PreviewEventDelta, EventID: orderedPreview.EventID,
		EventType: domain.EvAgentMessage, ModelRequestStartID: start.ID,
		Index: 0, Text: "late delta",
	}); err != nil {
		t.Fatal(err)
	}
	assertNoFrame(t, orderedFrames)
	activePreview := domain.PreviewFrame{
		Kind: domain.PreviewEventDelta, EventID: "sevt_live_next_preview",
		EventType: domain.EvAgentMessage, ModelRequestStartID: nextStart.ID,
		Index: 0, Text: "active delta",
	}
	if err := broker.PublishPreview(ctx, orderedSession.ID, activePreview); err != nil {
		t.Fatal(err)
	}
	ordered = receiveFrame(t, orderedFrames)
	if ordered.Preview == nil || ordered.Preview.Text != activePreview.Text {
		t.Fatalf("active model preview = %+v, want %+v", ordered, activePreview)
	}
	cancelOrdered()

	// Core NATS is at-most-once. Drop the publisher deliberately and prove the
	// stream's periodic cursor reconciliation repairs the missed wakeup.
	store.SetEventNotifier(nil)
	missed, err := store.AdmitEvents(ctx, session.ID, []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "still working"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	frame = receiveFrame(t, frames)
	if frame.Event == nil || frame.Event.ID != missed.Events[0].ID {
		t.Fatalf("reconciled frame = %+v, want event %s", frame, missed.Events[0].ID)
	}

	// Deletion has its own idle-only lifecycle rule. Use a separate idle Session
	// so the missed-wakeup case above can remain truthfully running.
	store.SetEventNotifier(broker)
	deletable := domain.Session{
		ID: "sesn_live_delete", Status: domain.StatusIdle, Metadata: map[string]any{},
		CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	}
	if _, err := store.CreateSession(ctx, deletable, nil); err != nil {
		t.Fatal(err)
	}
	deleteFrames, cancelDelete, err := stream.SubscribeContext(ctx, deletable.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelDelete()
	if err := store.DeleteSession(ctx, deletable.ID); err != nil {
		t.Fatal(err)
	}
	frame = receiveFrame(t, deleteFrames)
	if frame.Event == nil || frame.Event.Type != domain.EvSessionDeleted {
		t.Fatalf("delete frame = %+v", frame)
	}
}

func receiveFrame(t *testing.T, frames <-chan app.Frame) app.Frame {
	t.Helper()
	select {
	case frame, open := <-frames:
		if !open {
			t.Fatal("stream closed")
		}
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live frame")
		return app.Frame{}
	}
}

func assertNoFrame(t *testing.T, frames <-chan app.Frame) {
	t.Helper()
	select {
	case frame, open := <-frames:
		if open {
			t.Fatalf("unexpected live frame after model request end: %+v", frame)
		}
		t.Fatal("stream closed while checking for a late preview")
	case <-time.After(200 * time.Millisecond):
	}
}

type liveIDs struct {
	mu sync.Mutex
	n  int64
}

func (g *liveIDs) NewID(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return prefix + liveIntString(g.n)
}

type liveClock struct {
	n atomic.Int64
}

func (c *liveClock) Now() time.Time {
	return time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC).
		Add(time.Duration(c.n.Add(1)) * time.Millisecond)
}

func liveSafeName(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func liveIntString(value int64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
