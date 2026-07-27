package app

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// bufferedSink is both the run's persistent EventSink and its live
// PreviewEmitter: Emit buffers drafts for commit at run completion, while
// PreviewStart/PreviewDelta forward stream-only preview frames the hub delivers
// to opted-in subscribers.
var (
	_ agentruntime.EventSink      = (*bufferedSink)(nil)
	_ agentruntime.PreviewEmitter = (*bufferedSink)(nil)
)

// bufferedSink accumulates the runtime's emitted drafts for a single run and
// hands the runtime back committed event ids at emit time. It pre-assigns each
// draft's committed id from the shared IDGenerator (the same generator the
// store uses) and stamps it onto the draft, so the id the runtime reads back
// (for tool_result correlation, or to name parked events in a requires_action
// stop reason) is exactly the id the store will persist. Emission stays in
// memory; the drafts are committed together when the run completes.
//
// Independently of that persistent buffer, the sink is also a PreviewEmitter: it
// forwards live preview frames straight to the EventService (hub), so opted-in
// stream clients see an incremental assistant message before its full
// agent.message is committed. Previews are a real-time side channel — they never
// touch the drafts buffer, are never persisted, and never appear in history. The
// persistent buffer→commit path is unchanged by their presence.
type bufferedSink struct {
	ids       domain.IDGenerator
	drafts    []domain.EventDraft
	events    *EventService
	sessionID string
	// previewTypes remembers each previewed event's type from its PreviewStart so
	// the following PreviewDelta frames can carry it. The hub routes preview
	// frames by EventType (only opted-in subscribers receive them), and the core's
	// PreviewDelta callback does not repeat the type, so the sink threads it here.
	previewTypes map[string]string
}

func newBufferedSink(ids domain.IDGenerator, events *EventService, sessionID string) *bufferedSink {
	return &bufferedSink{ids: ids, events: events, sessionID: sessionID, previewTypes: map[string]string{}}
}

// PreviewStart announces a streamed assistant message. eventID is the committed
// id the persisted agent.message will carry, so the preview and the eventual
// event correlate by that shared id.
func (s *bufferedSink) PreviewStart(eventID, eventType string) {
	if s.events == nil {
		return
	}
	s.previewTypes[eventID] = eventType
	s.events.PublishPreview(s.sessionID, domain.PreviewFrame{
		Kind:      domain.PreviewEventStart,
		EventID:   eventID,
		EventType: eventType,
	})
}

// PreviewDelta forwards one incremental text chunk of the streamed message. The
// frame carries the previewed event's type (recorded at PreviewStart) so the hub
// delivers it to the same opted-in subscribers; the delta wire shape itself does
// not surface the type.
func (s *bufferedSink) PreviewDelta(eventID string, index int, text string) {
	if s.events == nil {
		return
	}
	s.events.PublishPreview(s.sessionID, domain.PreviewFrame{
		Kind:      domain.PreviewEventDelta,
		EventID:   eventID,
		EventType: s.previewTypes[eventID],
		Index:     index,
		Text:      text,
	})
}

func (s *bufferedSink) Emit(_ context.Context, drafts []domain.EventDraft) ([]domain.Event, error) {
	events := make([]domain.Event, 0, len(drafts))
	for _, draft := range drafts {
		if draft.ID == "" {
			draft.ID = s.ids.NewID(domain.PrefixEvent)
		}
		s.drafts = append(s.drafts, draft)
		events = append(events, domain.Event{ID: draft.ID, Type: draft.Type, Payload: draft.Payload})
	}
	return events, nil
}

func (s *bufferedSink) Drafts() []domain.EventDraft {
	return append([]domain.EventDraft(nil), s.drafts...)
}
