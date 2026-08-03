// Package live provides the ephemeral real-time side of the event API. NATS
// carries wakeups and preview deltas; PostgreSQL remains authoritative.
package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

const (
	defaultNATSURL       = nats.DefaultURL
	defaultReconcileTick = 5 * time.Second
)

// Broker owns one reconnecting Core NATS connection. Core NATS is deliberately
// used without JetStream: notifications and previews are ephemeral, while any
// missed persisted-event wakeup is repaired from PostgreSQL.
type Broker struct {
	connection *nats.Conn
}

func Connect(url string) (*Broker, error) {
	if url == "" {
		url = defaultNATSURL
	}
	connection, err := nats.Connect(
		url,
		nats.Name("managed-agent-go"),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("live: connect NATS: %w", err)
	}
	return &Broker{connection: connection}, nil
}

func (b *Broker) Close() {
	if b == nil || b.connection == nil {
		return
	}
	b.connection.Close()
}

// Ping reports whether the live channel is usable right now. It is a readiness
// probe, not a correctness check: NATS is an ephemeral delivery optimization,
// so a failure here degrades stream latency rather than the durable ledger. The
// flush forces a real round trip instead of trusting the cached connection
// state, and the timeout keeps a wedged server from stalling readiness.
func (b *Broker) Ping(timeout time.Duration) error {
	if b == nil || b.connection == nil {
		return errors.New("live: NATS connection is not configured")
	}
	if !b.connection.IsConnected() {
		return fmt.Errorf("live: NATS connection is %s", b.connection.Status())
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if err := b.connection.FlushTimeout(timeout); err != nil {
		return fmt.Errorf("live: NATS round trip failed: %w", err)
	}
	return nil
}

func (b *Broker) NotifySession(_ context.Context, sessionID string) error {
	return b.connection.Publish(eventSubject(sessionID), nil)
}

// PublishPreview emits a best-effort preview frame. A lost or duplicated
// preview never changes the authoritative event history.
func (b *Broker) PublishPreview(
	_ context.Context,
	sessionID string,
	frame domain.PreviewFrame,
) error {
	payload, err := json.Marshal(previewEnvelope{
		Kind: frame.Kind, EventID: frame.EventID, EventType: frame.EventType,
		Index: frame.Index, Text: frame.Text,
	})
	if err != nil {
		return err
	}
	return b.connection.Publish(previewSubject(sessionID), payload)
}

type previewEnvelope struct {
	Kind      string `json:"kind"`
	EventID   string `json:"event_id"`
	EventType string `json:"event_type"`
	Index     int    `json:"index,omitempty"`
	Text      string `json:"text,omitempty"`
}

func eventSubject(sessionID string) string {
	return "managed_agent.session." + subjectToken(sessionID) + ".events"
}

func previewSubject(sessionID string) string {
	return "managed_agent.session." + subjectToken(sessionID) + ".previews"
}

func subjectToken(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// Stream turns NATS wakeups into cursor reads from PostgreSQL. It also forwards
// preview frames only to subscribers that opted into their event type.
type Stream struct {
	store             *pg.Store
	broker            *Broker
	ids               domain.IDGenerator
	clock             domain.Clock
	reconcileInterval time.Duration
	bufferSize        int
}

func NewStream(
	store *pg.Store,
	broker *Broker,
	ids domain.IDGenerator,
	clock domain.Clock,
	reconcileInterval time.Duration,
) *Stream {
	if reconcileInterval <= 0 {
		reconcileInterval = defaultReconcileTick
	}
	return &Stream{
		store: store, broker: broker, ids: ids, clock: clock,
		reconcileInterval: reconcileInterval, bufferSize: 256,
	}
}

func (s *Stream) SubscribeContext(
	parent context.Context,
	sessionID string,
	deltaOptIn map[string]bool,
) (<-chan app.Frame, func(), error) {
	cursor, err := s.store.LatestEventSequence(parent, sessionID)
	if err != nil {
		return nil, nil, err
	}
	wakeups := make(chan *nats.Msg, 64)
	wakeupSubscription, err := s.broker.connection.ChanSubscribe(
		eventSubject(sessionID),
		wakeups,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("live: subscribe event wakeups: %w", err)
	}

	var (
		previews            chan *nats.Msg
		previewSubscription *nats.Subscription
	)
	if len(deltaOptIn) > 0 {
		previews = make(chan *nats.Msg, 256)
		previewSubscription, err = s.broker.connection.ChanSubscribe(
			previewSubject(sessionID),
			previews,
		)
		if err != nil {
			_ = wakeupSubscription.Unsubscribe()
			return nil, nil, fmt.Errorf("live: subscribe previews: %w", err)
		}
	}
	if err := s.broker.connection.FlushTimeout(5 * time.Second); err != nil {
		_ = wakeupSubscription.Unsubscribe()
		if previewSubscription != nil {
			_ = previewSubscription.Unsubscribe()
		}
		return nil, nil, fmt.Errorf("live: activate subscriptions: %w", err)
	}

	ctx, cancelContext := context.WithCancel(parent)
	frames := make(chan app.Frame, s.bufferSize)
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(cancelContext)
	}
	go s.tail(
		ctx,
		sessionID,
		cursor,
		deltaOptIn,
		wakeups,
		previews,
		wakeupSubscription,
		previewSubscription,
		frames,
	)
	return frames, cancel, nil
}

func (s *Stream) tail(
	ctx context.Context,
	sessionID string,
	cursor int64,
	deltaOptIn map[string]bool,
	wakeups <-chan *nats.Msg,
	previews <-chan *nats.Msg,
	wakeupSubscription *nats.Subscription,
	previewSubscription *nats.Subscription,
	frames chan app.Frame,
) {
	defer close(frames)
	defer wakeupSubscription.Unsubscribe() //nolint:errcheck
	if previewSubscription != nil {
		defer previewSubscription.Unsubscribe() //nolint:errcheck
	}
	ticker := time.NewTicker(s.reconcileInterval)
	defer ticker.Stop()
	previewPrepared := make(map[string]struct{})
	closedPreviewEvents := make(map[string]struct{})
	modelRequestClosed := false

	reconcile := func() bool {
		for {
			events, err := s.store.EventsAfter(ctx, sessionID, cursor, 1000)
			if err != nil {
				return false
			}
			for _, event := range events {
				cursor = event.Sequence
				switch event.Type {
				case domain.EvSpanModelRequestStart:
					modelRequestClosed = false
				case domain.EvSpanModelRequestEnd:
					modelRequestClosed = true
				case domain.EvAgentMessage:
					closedPreviewEvents[event.ID] = struct{}{}
				}
				current := event
				select {
				case frames <- app.Frame{Event: &current}:
				case <-ctx.Done():
					return false
				default:
					return false
				}
			}
			if len(events) < 1000 {
				break
			}
		}
		exists, err := s.store.SessionExists(ctx, sessionID)
		if err != nil {
			return false
		}
		if exists {
			return true
		}
		now := s.clock.Now().UTC()
		deleted := domain.Event{
			ID:          s.ids.NewID(domain.PrefixEvent),
			SessionID:   sessionID,
			Type:        domain.EvSessionDeleted,
			Payload:     map[string]any{},
			CreatedAt:   now,
			ProcessedAt: &now,
		}
		select {
		case frames <- app.Frame{Event: &deleted}:
		case <-ctx.Done():
		}
		return false
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeups:
			if !reconcile() {
				return
			}
		case message := <-previews:
			if message == nil {
				continue
			}
			var envelope previewEnvelope
			if err := json.Unmarshal(message.Data, &envelope); err != nil {
				continue
			}
			if !deltaOptIn[envelope.EventType] {
				continue
			}
			if _, prepared := previewPrepared[envelope.EventID]; !prepared {
				// StartModelRequest commits before CallModel can publish. Catch the
				// authoritative cursor up before forwarding this event's first
				// best-effort frame, even if select observed the preview subject
				// before the persisted-event wakeup subject.
				if !reconcile() {
					return
				}
				previewPrepared[envelope.EventID] = struct{}{}
			}
			if _, closed := closedPreviewEvents[envelope.EventID]; closed {
				// A delayed NATS frame must not appear after its authoritative
				// buffered agent.message.
				continue
			}
			if modelRequestClosed {
				// Error and interrupt paths intentionally have no authoritative
				// agent.message. Once their durable span end has reached this
				// subscriber, it still closes any preview frames buffered on the
				// separate NATS subject.
				continue
			}
			preview := domain.PreviewFrame{
				Kind: envelope.Kind, EventID: envelope.EventID,
				EventType: envelope.EventType, Index: envelope.Index, Text: envelope.Text,
			}
			select {
			case frames <- app.Frame{Preview: &preview}:
			case <-ctx.Done():
				return
			default:
				return
			}
		case <-ticker.C:
			// Core NATS is intentionally at-most-once. Periodic reconciliation
			// repairs a wakeup lost during subscriber disconnect/reconnect.
			if !reconcile() {
				return
			}
		}
	}
}
