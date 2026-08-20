package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

type outcomeRubricReaderFunc func(context.Context, string) (string, error)

func (f outcomeRubricReaderFunc) ReadOutcomeRubric(
	ctx context.Context,
	fileID string,
) (string, error) {
	return f(ctx, fileID)
}

func TestSessionServiceResolveOutcomeRubricsSnapshotsWithoutMutatingInput(t *testing.T) {
	service := &SessionService{outcomeFiles: outcomeRubricReaderFunc(
		func(_ context.Context, fileID string) (string, error) {
			if fileID != "file_rubric" {
				t.Fatalf("file id = %q", fileID)
			}
			return "# Rubric\n- cites evidence", nil
		},
	)}
	original := []domain.EventDraft{{
		Type: domain.EvUserDefineOutcome,
		Payload: map[string]any{
			"description": "produce report",
			"rubric": map[string]any{
				"type": "file", "file_id": "file_rubric",
			},
		},
	}}
	prepared, err := service.resolveOutcomeRubrics(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := original[0].Payload[domain.InternalOutcomeRubricContent]; present {
		t.Fatal("resolver mutated caller payload")
	}
	if got, ok := domain.OutcomeRubricContent(prepared[0].Payload); !ok || got != "# Rubric\n- cites evidence" {
		t.Fatalf("snapshot = %q, %v", got, ok)
	}
}

func TestSessionServiceResolveOutcomeRubricsRequiresFilesAndPropagatesReadErrors(t *testing.T) {
	draft := domain.EventDraft{Type: domain.EvUserDefineOutcome, Payload: map[string]any{
		"rubric": map[string]any{"type": "file", "file_id": "file_missing"},
	}}
	if _, err := (&SessionService{}).resolveOutcomeRubrics(
		context.Background(), []domain.EventDraft{draft},
	); err == nil || !strings.Contains(err.Error(), "Files storage is not configured") {
		t.Fatalf("disabled resolver error = %v", err)
	}
	want := errors.New("object store unavailable")
	service := &SessionService{outcomeFiles: outcomeRubricReaderFunc(
		func(context.Context, string) (string, error) { return "", want },
	)}
	if _, err := service.resolveOutcomeRubrics(
		context.Background(), []domain.EventDraft{draft},
	); !errors.Is(err, want) {
		t.Fatalf("read error = %v, want %v", err, want)
	}
}
