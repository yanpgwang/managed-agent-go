package app

import (
	"sync"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// Frame is the unit delivered to a stream subscriber. Exactly one of Event or
// Preview is non-nil. Event carries a persisted event (delivered to every
// subscriber); Preview carries a stream-only preview frame (delivered only to
// subscribers that opted in for its event type). Preview frames are never
// persisted and never appear in event history.
//
// The Event/Preview pointees MUST be treated as read-only by consumers: a
// single Publish/PublishPreview sends the same Frame (and thus the same inner
// pointer) to every matching subscriber, so mutating through the pointer would
// race and corrupt other subscribers' view. Copy before mutating if ever needed.
type Frame struct {
	Event   *domain.Event
	Preview *domain.PreviewFrame
}

type subscriber struct {
	ch         chan Frame
	closed     bool
	deltaOptIn map[string]bool
}

type Hub struct {
	mu      sync.Mutex
	bufSize int
	subs    map[string]map[*subscriber]struct{}
}

func NewHub(bufferSize int) *Hub {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	return &Hub{
		bufSize: bufferSize,
		subs:    map[string]map[*subscriber]struct{}{},
	}
}

// Subscribe registers a subscriber for a session. deltaOptIn selects which
// event types this subscriber wants preview frames for; a nil/empty map means
// preview frames are never delivered to it. Persisted events are always
// delivered regardless of deltaOptIn.
func (h *Hub) Subscribe(sessionID string, deltaOptIn map[string]bool) (<-chan Frame, func()) {
	s := &subscriber{ch: make(chan Frame, h.bufSize), deltaOptIn: deltaOptIn}
	h.mu.Lock()
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = map[*subscriber]struct{}{}
	}
	h.subs[sessionID][s] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			h.removeLocked(sessionID, s)
			h.mu.Unlock()
		})
	}
	return s.ch, cancel
}

func (h *Hub) removeLocked(sessionID string, s *subscriber) {
	if set, ok := h.subs[sessionID]; ok {
		if _, present := set[s]; present {
			delete(set, s)
			if !s.closed {
				s.closed = true
				close(s.ch)
			}
		}
		if len(set) == 0 {
			delete(h.subs, sessionID)
		}
	}
}

func (h *Hub) Publish(sessionID string, e domain.Event) {
	ev := e
	f := Frame{Event: &ev}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[sessionID] {
		select {
		case s.ch <- f:
		default:
			// slow consumer: drop the subscriber, never block, never lose the
			// event for healthy subscribers. Client reconnects and reconciles.
			h.removeLocked(sessionID, s)
		}
	}
}

// PublishPreview delivers a stream-only preview frame to subscribers that opted
// in for its event type. Preview frames are never persisted. Subscribers that
// did not opt in for f.EventType are skipped entirely (they see no frame). The
// slow-consumer drop policy still applies to opted-in subscribers.
func (h *Hub) PublishPreview(sessionID string, pf domain.PreviewFrame) {
	preview := pf
	f := Frame{Preview: &preview}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[sessionID] {
		if !s.deltaOptIn[pf.EventType] {
			continue
		}
		select {
		case s.ch <- f:
		default:
			h.removeLocked(sessionID, s)
		}
	}
}

// CloseSession publishes a final event and closes every subscriber atomically.
func (h *Hub) CloseSession(sessionID string, terminal domain.Event) {
	ev := terminal
	f := Frame{Event: &ev}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[sessionID] {
		if s.closed {
			continue
		}
		select {
		case s.ch <- f:
		default:
			// The normal slow-consumer policy still applies: do not block a
			// successful delete on a subscriber that stopped reading.
		}
		s.closed = true
		close(s.ch)
	}
	delete(h.subs, sessionID)
}
