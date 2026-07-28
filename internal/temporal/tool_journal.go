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
//
// Every write runs on a context detached from the runtime's cancellation
// (durableCtx: context.WithoutCancel + a fresh bounded timeout). This mirrors
// the SQLite app's runToolJournal: once a tool has Started — its side effect may
// already have happened — a cancellation of the Activity context must not prevent
// recording Start or the subsequent Complete. Recording the fact is what keeps a
// crashed step recoverable as ambiguous instead of being silently replayed.
type activityToolJournal struct {
	store     JournalStore
	attemptID string
}

func (j activityToolJournal) Prepare(ctx context.Context, ordinal int, toolUseEventID, toolName string, input map[string]any) (string, error) {
	dctx, cancel := durableCtx(ctx)
	defer cancel()
	return j.store.PrepareToolStep(dctx, j.attemptID, ordinal, toolUseEventID, toolName, input)
}

func (j activityToolJournal) Start(ctx context.Context, stepID string) error {
	dctx, cancel := durableCtx(ctx)
	defer cancel()
	return j.store.StartToolStep(dctx, stepID)
}

func (j activityToolJournal) Complete(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	dctx, cancel := durableCtx(ctx)
	defer cancel()
	return j.store.CompleteToolStep(dctx, stepID, result)
}
