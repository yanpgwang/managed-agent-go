package app

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

// runToolJournal binds runtime tool calls to one durable run attempt. Store
// writes use durableCtx rather than the runtime's cancelable context: after an
// interrupt reaches a tool executor, its returned result must still be
// recorded before the run can be classified and closed.
type runToolJournal struct {
	durableCtx context.Context
	runs       *store.RunStore
	attemptID  string
}

func (j runToolJournal) Prepare(
	_ context.Context,
	ordinal int,
	toolUseEventID string,
	toolName string,
	input map[string]any,
) (string, error) {
	step, err := j.runs.PrepareToolStep(
		j.durableCtx, j.attemptID, ordinal, toolUseEventID, toolName, input,
	)
	if err != nil {
		return "", err
	}
	return step.ID, nil
}

func (j runToolJournal) Start(_ context.Context, stepID string) error {
	_, err := j.runs.StartToolStep(j.durableCtx, stepID)
	return err
}

func (j runToolJournal) Complete(
	_ context.Context,
	stepID string,
	result domain.ToolStepResult,
) error {
	_, err := j.runs.CompleteToolStep(j.durableCtx, stepID, result)
	return err
}
