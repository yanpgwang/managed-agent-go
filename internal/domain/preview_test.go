package domain

import (
	"reflect"
	"testing"
)

func TestPreviewFrame_WireJSON_Start(t *testing.T) {
	f := PreviewFrame{
		Kind: PreviewEventStart, EventID: "sevt_1", EventType: "agent.message",
		ModelRequestStartID: "sevt_model_start",
	}
	got := f.WireJSON()
	want := map[string]any{
		"type":  "event_start",
		"event": map[string]any{"type": "agent.message", "id": "sevt_1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("start wire = %#v, want %#v", got, want)
	}
}

func TestPreviewFrame_WireJSON_Delta(t *testing.T) {
	f := PreviewFrame{
		Kind: PreviewEventDelta, EventID: "sevt_1", Index: 0, Text: "Hi",
		ModelRequestStartID: "sevt_model_start",
	}
	got := f.WireJSON()
	want := map[string]any{
		"type":     "event_delta",
		"event_id": "sevt_1",
		"delta": map[string]any{
			"type":    "content_delta",
			"index":   0,
			"content": map[string]any{"type": "text", "text": "Hi"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delta wire = %#v, want %#v", got, want)
	}
}
