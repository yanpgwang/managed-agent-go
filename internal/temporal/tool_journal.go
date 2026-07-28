package temporal

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

var _ agentruntime.ToolExecutionJournal = activityToolJournal{}

// activityToolJournal binds the RunTurn Activity's durable journal store to one
// attempt, implementing the runtime's narrow ToolExecutionJournal interface. The
// runtime calls Prepare/Start/Complete around each built-in execution; each call
// is a durable PostgreSQL write, so the prepared/started/completed boundary
// survives an Activity crash and a retry can classify a started-but-uncompleted
// step as ambiguous rather than replaying it.
type activityToolJournal struct {
	store     JournalStore
	attemptID string
}

func (j activityToolJournal) Prepare(ctx context.Context, ordinal int, toolUseEventID, toolName string, input map[string]any) (string, error) {
	return j.store.PrepareToolStep(ctx, j.attemptID, ordinal, toolUseEventID, toolName, input)
}

func (j activityToolJournal) Start(ctx context.Context, stepID string) error {
	return j.store.StartToolStep(ctx, stepID)
}

func (j activityToolJournal) Complete(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	return j.store.CompleteToolStep(ctx, stepID, result)
}
