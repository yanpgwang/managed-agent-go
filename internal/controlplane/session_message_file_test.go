package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

type messageFileReaderFunc func(context.Context, string) (domain.FileMessageContent, error)

func (f messageFileReaderFunc) ReadMessageFile(
	ctx context.Context,
	id string,
) (domain.FileMessageContent, error) {
	return f(ctx, id)
}

func TestSessionServiceResolveMessageFilesSnapshotsWithoutMutatingInput(t *testing.T) {
	calls := 0
	service := &SessionService{messageFiles: messageFileReaderFunc(func(
		_ context.Context,
		id string,
	) (domain.FileMessageContent, error) {
		calls++
		return domain.FileMessageContent{
			FileID: id, Filename: "notes.md", MimeType: "text/markdown",
			Content: "# Notes\nresolved once",
		}, nil
	})}
	original := []domain.EventDraft{{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{
				"type":   "document",
				"source": map[string]any{"type": "file", "file_id": "file_notes"},
			},
			map[string]any{
				"type":   "document",
				"source": map[string]any{"type": "file", "file_id": "file_notes"},
			},
		}},
	}}
	prepared, err := service.resolveMessageFiles(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("File reads = %d, want one cached read", calls)
	}
	if _, present := original[0].Payload[domain.InternalFileMessageContents]; present {
		t.Fatal("resolver mutated caller payload")
	}
	snapshots := domain.FileMessageContents(prepared[0].Payload)
	if len(snapshots) != 2 || snapshots["0"].Content != "# Notes\nresolved once" ||
		snapshots["1"].FileID != "file_notes" {
		t.Fatalf("snapshots = %#v", snapshots)
	}
}

func TestSessionServiceResolveMessageFilesRequiresStorageAndPropagatesErrors(t *testing.T) {
	draft := domain.EventDraft{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{map[string]any{
			"type":   "document",
			"source": map[string]any{"type": "file", "file_id": "file_missing"},
		}}},
	}
	if _, err := (&SessionService{}).resolveMessageFiles(
		context.Background(), []domain.EventDraft{draft},
	); err == nil {
		t.Fatal("file message content succeeded without Files storage")
	}
	want := errors.New("object store unavailable")
	service := &SessionService{messageFiles: messageFileReaderFunc(func(
		context.Context,
		string,
	) (domain.FileMessageContent, error) {
		return domain.FileMessageContent{}, want
	})}
	if _, err := service.resolveMessageFiles(
		context.Background(), []domain.EventDraft{draft},
	); !errors.Is(err, want) {
		t.Fatalf("resolver error = %v, want %v", err, want)
	}
}

func TestSessionServiceResolveMessageFilesBoundsAggregateAdmissionContent(t *testing.T) {
	service := &SessionService{messageFiles: messageFileReaderFunc(func(
		_ context.Context,
		id string,
	) (domain.FileMessageContent, error) {
		return domain.FileMessageContent{
			FileID: id, Filename: id + ".txt", MimeType: "text/plain",
			Content: strings.Repeat("x", domain.MaxFileMessageCharacters/2+1),
		}, nil
	})}
	draft := domain.EventDraft{
		Type: domain.EvUserMessage,
		Payload: map[string]any{"content": []any{
			map[string]any{
				"type":   "document",
				"source": map[string]any{"type": "file", "file_id": "file_a"},
			},
			map[string]any{
				"type":   "document",
				"source": map[string]any{"type": "file", "file_id": "file_b"},
			},
		}},
	}
	if _, err := service.resolveMessageFiles(
		context.Background(), []domain.EventDraft{draft},
	); err == nil || !strings.Contains(err.Error(), "per admission") {
		t.Fatalf("aggregate File content error = %v", err)
	}
}
