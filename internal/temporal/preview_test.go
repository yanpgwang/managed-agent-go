package temporal

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
)

func TestCallModelPublishesCorrelatedPreviewFrames(t *testing.T) {
	publisher := &previewRecorder{}
	activities := NewActivities(
		nil,
		model.NewFake(),
		nil,
		nil,
		nil,
		domain.NewSeqIDGen(),
		publisher,
	)
	result, err := activities.CallModel(context.Background(), CallModelInput{
		SessionID: "sesn_1",
		Request: model.Request{Messages: []domain.Message{{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Type: "text", Text: "hello",
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frames := publisher.snapshot()
	if len(frames) < 3 {
		t.Fatalf("preview frames = %d, want start plus at least two deltas", len(frames))
	}
	if frames[0].sessionID != "sesn_1" ||
		frames[0].frame.Kind != domain.PreviewEventStart ||
		frames[0].frame.EventID != result.MessageEventID {
		t.Fatalf("start frame = %+v, result id=%s", frames[0], result.MessageEventID)
	}
	var text strings.Builder
	for _, frame := range frames[1:] {
		if frame.frame.Kind != domain.PreviewEventDelta {
			t.Fatalf("non-delta frame after start: %+v", frame)
		}
		if frame.frame.EventID != result.MessageEventID {
			t.Fatalf("delta id = %s, want %s", frame.frame.EventID, result.MessageEventID)
		}
		text.WriteString(frame.frame.Text)
	}
	if got, want := text.String(), "echo: hello"; got != want {
		t.Fatalf("preview text = %q, want %q", got, want)
	}
}

type recordedPreview struct {
	sessionID string
	frame     domain.PreviewFrame
}

type previewRecorder struct {
	mu     sync.Mutex
	frames []recordedPreview
}

func (p *previewRecorder) PublishPreview(
	_ context.Context,
	sessionID string,
	frame domain.PreviewFrame,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = append(p.frames, recordedPreview{sessionID: sessionID, frame: frame})
	return nil
}

func (p *previewRecorder) snapshot() []recordedPreview {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedPreview(nil), p.frames...)
}
