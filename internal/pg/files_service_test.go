package pg

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/blob"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/httpapi"
	"github.com/yanpgwang/mango/internal/sandbox"
)

func TestFileService_PostgresS3RestartReconciliation(t *testing.T) {
	endpoint := os.Getenv("MANGO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MANGO_TEST_S3_ENDPOINT not set; skipping Files service conformance")
	}
	store := testStore(t)
	repo := NewFileRepository(store)
	blobs, err := blob.NewS3Store(context.Background(), blob.S3Config{
		Endpoint: endpoint, Region: "us-east-1",
		Bucket:       os.Getenv("MANGO_TEST_S3_BUCKET"),
		AccessKey:    os.Getenv("MANGO_TEST_S3_ACCESS_KEY"),
		SecretKey:    os.Getenv("MANGO_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, CreateBucket: true, UploadTempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	ctx := context.Background()
	service := app.NewFileService(repo, blobs, domain.NewSeqIDGen(), fixedClock{})

	created, err := service.Upload(ctx, app.FileUploadInput{
		Filename: "input.txt", MimeType: "text/plain", Body: bytes.NewBufferString("input"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if created.SizeBytes != 5 || created.Downloadable {
		t.Fatalf("created = %+v", created)
	}

	// Seed a downloadable Session-scoped fixture so reconciliation exercises
	// both public and internal File intents. Runtime output publication is
	// covered end to end by TestFileHTTP_PostgresS3SDKLifecycle below.
	output := domain.File{
		ID: "file_output_service", Filename: "output.txt", MimeType: "text/plain",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Downloadable: true, Scope: &domain.FileScope{ID: "sesn_test", Type: "session"},
		BlobKey: "files/file_output_service", State: domain.FileStateUploading,
	}
	if err := repo.BeginUpload(ctx, output); err != nil {
		t.Fatal(err)
	}
	info, err := blobs.Put(ctx, output.BlobKey, output.MimeType, bytes.NewBufferString("output"), app.MaxFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteUpload(ctx, output.ID, info); err != nil {
		t.Fatal(err)
	}
	download, err := service.Download(ctx, output.ID)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, readErr := io.ReadAll(download.Body)
	if closeErr := download.Body.Close(); readErr == nil {
		readErr = closeErr
	}
	if readErr != nil || string(data) != "output" {
		t.Fatalf("download = %q, %v", data, readErr)
	}

	pending := domain.File{
		ID: "file_pending_service", Filename: "pending.txt", MimeType: "text/plain",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		BlobKey: "files/file_pending_service", State: domain.FileStateUploading,
	}
	if err := repo.BeginUpload(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Put(ctx, pending.BlobKey, pending.MimeType,
		bytes.NewBufferString("orphan"), app.MaxFileBytes); err != nil {
		t.Fatal(err)
	}

	deleting, err := repo.BeginDelete(ctx, created.ID)
	if err != nil || deleting.State != domain.FileStateDeleting {
		t.Fatalf("BeginDelete = %+v, %v", deleting, err)
	}
	restarted := app.NewFileService(repo, blobs, domain.NewSeqIDGen(), fixedClock{})
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, file := range []domain.File{pending, deleting} {
		if _, err := blobs.Open(ctx, file.BlobKey); err == nil {
			t.Errorf("incomplete blob %s remains", file.BlobKey)
		}
	}
	if incomplete, err := repo.ListIncomplete(ctx); err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete rows = %+v, %v", incomplete, err)
	}

	_, _ = service.Delete(ctx, output.ID)
}

func TestFileHTTP_PostgresS3SDKLifecycle(t *testing.T) {
	endpoint := os.Getenv("MANGO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MANGO_TEST_S3_ENDPOINT not set; skipping Files HTTP service conformance")
	}
	store := testStore(t)
	blobs, err := blob.NewS3Store(context.Background(), blob.S3Config{
		Endpoint: endpoint, Region: "us-east-1",
		Bucket:       os.Getenv("MANGO_TEST_S3_BUCKET"),
		AccessKey:    os.Getenv("MANGO_TEST_S3_ACCESS_KEY"),
		SecretKey:    os.Getenv("MANGO_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, CreateBucket: true, UploadTempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	repo := NewFileRepository(store)
	ids := domain.NewSeqIDGen()
	service := app.NewFileService(repo, blobs, ids, fixedClock{})
	server := httptest.NewServer(httpapi.NewServer(httpapi.Deps{Files: service}, httpapi.Config{
		RequireBeta: true, RequireAuth: true, RequireVersion: true, RequireContentType: true,
	}).Handler())
	defer server.Close()
	client := anthropic.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("sk-test"))
	ctx := context.Background()

	uploaded, err := client.Beta.Files.Upload(ctx, anthropic.BetaFileUploadParams{
		File: &serviceNamedReader{Reader: bytes.NewReader([]byte("service"))},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if uploaded.SizeBytes != 7 || uploaded.Downloadable || uploaded.Filename != "service.txt" {
		t.Fatalf("uploaded = %s", uploaded.RawJSON())
	}
	metadata, err := client.Beta.Files.GetMetadata(ctx, uploaded.ID, anthropic.BetaFileGetMetadataParams{})
	if err != nil || metadata.ID != uploaded.ID {
		t.Fatalf("GetMetadata = %+v, %v", metadata, err)
	}
	page, err := client.Beta.Files.List(ctx, anthropic.BetaFileListParams{})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != uploaded.ID {
		t.Fatalf("List = %+v, %v", page, err)
	}
	if _, err := client.Beta.Files.Download(ctx, uploaded.ID, anthropic.BetaFileDownloadParams{}); err == nil {
		t.Fatal("ordinary upload unexpectedly downloadable")
	} else {
		var apiErr *anthropic.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
			t.Fatalf("Download error = %T %v", err, err)
		}
	}
	session := newSession("sesn_http")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	publisher := app.NewSessionOutputPublisher(repo, blobs, ids, fixedClock{})
	if err := publisher.Publish(ctx, session.ID, pgOutputSandbox{
		archive: oneFileOutputArchive(t, "output.txt", "sdk-output"),
	}); err != nil {
		t.Fatalf("Publish Session output: %v", err)
	}
	rawRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet, server.URL+"/v1/files?scope_id="+session.ID, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	rawRequest.Header.Set("x-api-key", "sk-test")
	rawRequest.Header.Set("anthropic-version", "2023-06-01")
	rawRequest.Header.Set("anthropic-beta", "files-api-2025-04-14")
	rawResponse, err := server.Client().Do(rawRequest)
	if err != nil {
		t.Fatalf("raw list Session outputs: %v", err)
	}
	defer func() { _ = rawResponse.Body.Close() }()
	if rawResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(rawResponse.Body)
		t.Fatalf("raw list Session outputs = %d: %s", rawResponse.StatusCode, body)
	}
	var listed struct {
		Data []struct {
			ID           string `json:"id"`
			Filename     string `json:"filename"`
			Downloadable bool   `json:"downloadable"`
			Scope        struct {
				ID string `json:"id"`
			} `json:"scope"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rawResponse.Body).Decode(&listed); err != nil {
		t.Fatalf("decode raw output list: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Filename != "output.txt" ||
		!listed.Data[0].Downloadable || listed.Data[0].Scope.ID != session.ID {
		t.Fatalf("raw output list = %+v", listed.Data)
	}
	outputID := listed.Data[0].ID
	response, err := client.Beta.Files.Download(ctx, outputID, anthropic.BetaFileDownloadParams{})
	if err != nil {
		t.Fatalf("Download output: %v", err)
	}
	content, readErr := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); readErr == nil {
		readErr = closeErr
	}
	if readErr != nil || string(content) != "sdk-output" {
		t.Fatalf("Download output body = %q, %v", content, readErr)
	}
	deleted, err := client.Beta.Files.Delete(ctx, uploaded.ID, anthropic.BetaFileDeleteParams{})
	if err != nil || deleted.ID != uploaded.ID {
		t.Fatalf("Delete = %+v, %v", deleted, err)
	}
	if _, err := client.Beta.Files.GetMetadata(ctx, uploaded.ID, anthropic.BetaFileGetMetadataParams{}); err == nil {
		t.Fatal("deleted File remains visible")
	}
	if _, err := service.Delete(ctx, outputID); err != nil {
		t.Fatalf("Delete output: %v", err)
	}
}

func TestFileService_PostgresS3ConcurrentLifecycle(t *testing.T) {
	endpoint := os.Getenv("MANGO_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MANGO_TEST_S3_ENDPOINT not set; skipping concurrent Files conformance")
	}
	store := testStore(t)
	blobs, err := blob.NewS3Store(context.Background(), blob.S3Config{
		Endpoint: endpoint, Region: "us-east-1",
		Bucket:       os.Getenv("MANGO_TEST_S3_BUCKET"),
		AccessKey:    os.Getenv("MANGO_TEST_S3_ACCESS_KEY"),
		SecretKey:    os.Getenv("MANGO_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, CreateBucket: true, UploadTempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	service := app.NewFileService(
		NewFileRepository(store), blobs, domain.NewSeqIDGen(), fixedClock{},
	)
	ctx := context.Background()
	const count = 8
	files := make(chan domain.File, count)
	errs := make(chan error, count)
	var uploads sync.WaitGroup
	for index := 0; index < count; index++ {
		uploads.Add(1)
		go func(index int) {
			defer uploads.Done()
			created, uploadErr := service.Upload(ctx, app.FileUploadInput{
				Filename: fmt.Sprintf("concurrent-%d.bin", index),
				MimeType: "application/octet-stream",
				Body:     bytes.NewReader(bytes.Repeat([]byte{byte('a' + index)}, 32<<10)),
			})
			if uploadErr != nil {
				errs <- uploadErr
				return
			}
			files <- created
		}(index)
	}
	uploads.Wait()
	close(files)
	close(errs)
	for uploadErr := range errs {
		t.Errorf("concurrent Upload: %v", uploadErr)
	}
	created := make([]domain.File, 0, count)
	for file := range files {
		created = append(created, file)
	}
	if len(created) != count {
		t.Fatalf("created %d Files, want %d", len(created), count)
	}
	t.Cleanup(func() {
		for _, file := range created {
			_, _ = service.Delete(context.Background(), file.ID)
		}
	})
	page, err := service.List(ctx, app.FileListQuery{Limit: 100})
	if err != nil || len(page.Files) != count {
		t.Fatalf("List after concurrent upload = %d Files, %v", len(page.Files), err)
	}

	errs = make(chan error, count)
	var deletes sync.WaitGroup
	for _, file := range created {
		deletes.Add(1)
		go func(file domain.File) {
			defer deletes.Done()
			if _, deleteErr := service.Delete(ctx, file.ID); deleteErr != nil {
				errs <- deleteErr
			}
		}(file)
	}
	deletes.Wait()
	close(errs)
	for deleteErr := range errs {
		t.Errorf("concurrent Delete: %v", deleteErr)
	}
	page, err = service.List(ctx, app.FileListQuery{Limit: 100})
	if err != nil || len(page.Files) != 0 {
		t.Fatalf("List after concurrent delete = %d Files, %v", len(page.Files), err)
	}
}

type serviceNamedReader struct {
	*bytes.Reader
}

func (*serviceNamedReader) Name() string        { return "service.txt" }
func (*serviceNamedReader) ContentType() string { return "text/plain" }

type pgOutputSandbox struct {
	archive []byte
}

func (pgOutputSandbox) Exec(context.Context, sandbox.Command) (*sandbox.Result, error) {
	return nil, errors.New("not implemented")
}
func (pgOutputSandbox) ReadFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (pgOutputSandbox) WriteFile(context.Context, string, []byte) error {
	return errors.New("not implemented")
}
func (pgOutputSandbox) Root() string                  { return "/workspace" }
func (pgOutputSandbox) Destroy(context.Context) error { return nil }
func (s pgOutputSandbox) OpenSessionOutputs(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.archive)), nil
}

func oneFileOutputArchive(t *testing.T, name string, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{
		Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
