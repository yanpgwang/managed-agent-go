package domain

const (
	PreviewEventStart = "event_start"
	PreviewEventDelta = "event_delta"
)

// PreviewFrame is a stream-only preview of an event being generated. Preview
// frames are NEVER persisted and never appear in the event history; they are
// delivered only to stream subscribers that opted in via event_deltas[].
type PreviewFrame struct {
	ThreadID            string // empty for the primary stream; child Thread otherwise
	Kind                string // PreviewEventStart | PreviewEventDelta
	EventID             string // the id of the event being previewed (== the eventual persisted event id)
	EventType           string // event_start: the previewed event's type (e.g. "agent.message")
	ModelRequestStartID string // internal correlation fence; deliberately omitted from WireJSON
	Index               int    // event_delta: content block index
	Text                string // event_delta: incremental text
}

func (f PreviewFrame) WireJSON() map[string]any {
	if f.Kind == PreviewEventStart {
		return map[string]any{
			"type":  "event_start",
			"event": map[string]any{"type": f.EventType, "id": f.EventID},
		}
	}
	return map[string]any{
		"type":     "event_delta",
		"event_id": f.EventID,
		"delta": map[string]any{
			"type":    "content_delta",
			"index":   f.Index,
			"content": map[string]any{"type": "text", "text": f.Text},
		},
	}
}
