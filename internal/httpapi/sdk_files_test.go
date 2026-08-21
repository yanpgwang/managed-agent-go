package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestSDK_FilesLifecycleAndBidirectionalPaging(t *testing.T) {
	service := newTestFileService()
	handler := NewServer(Deps{Files: service}, Config{
		RequireAuth: true,
	}).Handler()
	server := httptest.NewServer(handler)
	defer server.Close()
	client := anthropic.NewClient(option.WithBaseURL(server.URL), option.WithAuthToken("sk-test"))
	ctx := context.Background()

	created := make([]anthropic.FileMetadata, 0, 5)
	for index := 0; index < 5; index++ {
		file, err := client.Beta.Files.Upload(ctx, anthropic.BetaFileUploadParams{
			File: &namedFileReader{
				Reader: bytes.NewReader([]byte{byte('a' + index)}),
				name:   "file-" + string(rune('a'+index)) + ".txt", mimeType: "text/plain",
			},
		})
		if err != nil {
			t.Fatalf("Upload %d: %v", index, err)
		}
		assertRawObjectHasFields(t, file.RawJSON(),
			"id", "created_at", "filename", "mime_type", "size_bytes",
			"type", "downloadable", "scope")
		if file.Downloadable || file.Scope.JSON.ID.Valid() {
			t.Fatalf("uploaded file visibility = %s", file.RawJSON())
		}
		created = append(created, *file)
	}

	pager := client.Beta.Files.ListAutoPaging(ctx, anthropic.BetaFileListParams{
		Limit: anthropic.Int(2),
	})
	seen := map[string]bool{}
	for pager.Next() {
		seen[pager.Current().ID] = true
	}
	if err := pager.Err(); err != nil {
		t.Fatalf("ListAutoPaging: %v", err)
	}
	if len(seen) != len(created) {
		t.Fatalf("forward auto-pager saw %d files, want %d", len(seen), len(created))
	}

	beforePager := client.Beta.Files.ListAutoPaging(ctx, anthropic.BetaFileListParams{
		BeforeID: anthropic.String(created[0].ID), Limit: anthropic.Int(2),
	})
	beforeSeen := map[string]bool{}
	for beforePager.Next() {
		beforeSeen[beforePager.Current().ID] = true
	}
	if err := beforePager.Err(); err != nil {
		t.Fatalf("before ListAutoPaging: %v", err)
	}
	if len(beforeSeen) != len(created)-1 {
		t.Fatalf("backward auto-pager saw %d files, want %d", len(beforeSeen), len(created)-1)
	}

	metadata, err := client.Beta.Files.GetMetadata(ctx, created[2].ID, anthropic.BetaFileGetMetadataParams{})
	if err != nil || metadata.ID != created[2].ID {
		t.Fatalf("GetMetadata = %+v, %v", metadata, err)
	}
	if _, err := client.Beta.Files.Download(ctx, created[2].ID, anthropic.BetaFileDownloadParams{}); err == nil {
		t.Fatal("ordinary uploaded file unexpectedly downloaded")
	} else {
		assertAPIStatus(t, err, 400)
	}

	output := service.seedDownloadable("result.txt", "text/plain", []byte("result"))
	response, err := client.Beta.Files.Download(ctx, output.ID, anthropic.BetaFileDownloadParams{})
	if err != nil {
		t.Fatalf("Download output: %v", err)
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("download X-Content-Type-Options = %q", got)
	}
	if got := response.Header.Get("Content-Disposition"); got != `attachment; filename=result.txt` {
		t.Fatalf("download Content-Disposition = %q", got)
	}
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(body) != "result" {
		t.Fatalf("download body = %q, %v", body, err)
	}

	deleted, err := client.Beta.Files.Delete(ctx, created[1].ID, anthropic.BetaFileDeleteParams{})
	if err != nil || deleted.ID != created[1].ID || deleted.Type != anthropic.DeletedFileTypeFileDeleted {
		t.Fatalf("Delete = %+v, %v", deleted, err)
	}
	_, err = client.Beta.Files.GetMetadata(ctx, created[1].ID, anthropic.BetaFileGetMetadataParams{})
	assertAPIStatus(t, err, 404)
}

type namedFileReader struct {
	*bytes.Reader
	name     string
	mimeType string
}

func (r *namedFileReader) Name() string        { return r.name }
func (r *namedFileReader) ContentType() string { return r.mimeType }
