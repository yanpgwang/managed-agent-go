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

func TestNATSStreamReconcilesLedgerAndCarriesPreviews(t *testing.T) {
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
	defer broker.Close()
	ids := &liveIDs{}
	clock := &liveClock{}
	store := pg.NewStore(pool, ids, clock)
	store.SetEventNotifier(broker)
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
