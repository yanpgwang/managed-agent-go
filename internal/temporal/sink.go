package temporal

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

var _ agentruntime.EventSink = (*activitySink)(nil)

// activitySink buffers the runtime's emitted drafts during one RunTurn Activity.
// It pre-assigns each draft's committed id from the shared generator so the id
// the runtime reads back (e.g. for tool_result correlation) matches the id
// PostgreSQL will persist. This legacy Activity sink buffers authoritative
// output; previews for the Workflow-owned model path are published separately.
type activitySink struct {
	ids    domain.IDGenerator
	drafts []domain.EventDraft
}

func newActivitySink(ids domain.IDGenerator) *activitySink {
	return &activitySink{ids: ids}
}

func (s *activitySink) Emit(_ context.Context, drafts []domain.EventDraft) ([]domain.Event, error) {
	out := make([]domain.Event, 0, len(drafts))
	for _, draft := range drafts {
		if draft.ID == "" {
			draft.ID = s.ids.NewID(domain.PrefixEvent)
		}
		s.drafts = append(s.drafts, draft)
		out = append(out, domain.Event{ID: draft.ID, Type: draft.Type, Payload: draft.Payload})
	}
	return out, nil
}

func (s *activitySink) Drafts() []domain.EventDraft {
	return append([]domain.EventDraft(nil), s.drafts...)
}
