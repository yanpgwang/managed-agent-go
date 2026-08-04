package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestFileService_UploadDeleteAndReconcile(t *testing.T) {
	repo := newMemoryFileRepository()
	blobs := newMemoryBlobStore()
	service := NewFileService(repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{
		T: time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC),
	})

	created, err := service.Upload(context.Background(), FileUploadInput{
		Filename: "report.txt", MimeType: "text/plain; charset=utf-8",
		Body: bytes.NewBufferString("hello"),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if created.ID != "file_1" || created.SizeBytes != 5 || created.MimeType != "text/plain" {
		t.Fatalf("created = %+v", created)
	}
	if created.Downloadable || created.Scope != nil || created.State != domain.FileStateReady {
		t.Fatalf("uploaded visibility fields = %+v", created)
	}
	if _, err := service.Download(context.Background(), created.ID); err == nil {
		t.Fatal("uploaded file unexpectedly downloadable")
	}

	if _, err := service.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := service.Get(context.Background(), created.ID); err == nil {
		t.Fatal("deleted file remains visible")
	}
	if _, present := blobs.objects[created.BlobKey]; present {
		t.Fatal("deleted file bytes remain")
	}

	pending := domain.File{
		ID: "file_pending", Filename: "pending.txt", MimeType: "text/plain",
		BlobKey: "files/file_pending", State: domain.FileStateUploading,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := repo.BeginUpload(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	blobs.objects[pending.BlobKey] = []byte("orphan")
	if err := service.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, present := repo.files[pending.ID]; present {
		t.Fatal("pending metadata remains after reconciliation")
	}
	if _, present := blobs.objects[pending.BlobKey]; present {
		t.Fatal("pending blob remains after reconciliation")
	}
}

func TestFileService_ValidatesUploadAndMapsStreamingLimit(t *testing.T) {
	tests := []FileUploadInput{
		{Filename: "", MimeType: "text/plain", Body: bytes.NewReader(nil)},
		{Filename: "../secret", MimeType: "text/plain", Body: bytes.NewReader(nil)},
		{Filename: "ok.txt", MimeType: "not a mime", Body: bytes.NewReader(nil)},
		{Filename: "ok.txt", MimeType: "text/plain"},
	}
	for _, input := range tests {
		service := NewFileService(newMemoryFileRepository(), newMemoryBlobStore(),
			domain.NewSeqIDGen(), domain.FixedClock{})
		if _, err := service.Upload(context.Background(), input); err == nil {
			t.Errorf("Upload(%+v) succeeded", input)
		}
	}

	repo := newMemoryFileRepository()
	blobs := newMemoryBlobStore()
	blobs.putErr = ErrBlobTooLarge
	service := NewFileService(repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{})
	_, err := service.Upload(context.Background(), FileUploadInput{
		Filename: "large.bin", MimeType: "application/octet-stream", Body: bytes.NewReader(nil),
	})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindTooLarge {
		t.Fatalf("oversize error = %v", err)
	}
	if len(repo.files) != 0 {
		t.Fatalf("failed upload left rows: %+v", repo.files)
	}
}

func TestFileService_PreservesBlobWhenCompletionResultIsAmbiguous(t *testing.T) {
	repo := newMemoryFileRepository()
	repo.completeErrAfterCommit = errors.New("database connection lost after commit")
	blobs := newMemoryBlobStore()
	service := NewFileService(repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{})

	_, err := service.Upload(context.Background(), FileUploadInput{
		Filename: "committed.txt", MimeType: "text/plain", Body: bytes.NewBufferString("safe"),
	})
	if !errors.Is(err, repo.completeErrAfterCommit) {
		t.Fatalf("Upload error = %v", err)
	}
	file, err := repo.Get(context.Background(), "file_1")
	if err != nil || file.State != domain.FileStateReady {
		t.Fatalf("committed File = %+v, %v", file, err)
	}
	if got := string(blobs.objects[file.BlobKey]); got != "safe" {
		t.Fatalf("committed blob = %q, want safe", got)
	}
}

func TestFileService_RejectsDeletingSessionScopedFile(t *testing.T) {
	repo := newMemoryFileRepository()
	blobs := newMemoryBlobStore()
	service := NewFileService(repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{})
	file := domain.File{
		ID: "file_scoped", Filename: "resource.txt", MimeType: "text/plain",
		Downloadable: true,
		Scope:        &domain.FileScope{ID: "sesn_1", Type: "session"},
		BlobKey:      "files/file_scoped", State: domain.FileStateUploading,
	}
	if err := repo.BeginUpload(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteUpload(context.Background(), file.ID, ComputeBlobInfo([]byte("safe"))); err != nil {
		t.Fatal(err)
	}
	blobs.objects[file.BlobKey] = []byte("safe")
	repo.protected[file.ID] = true

	if _, err := service.Delete(context.Background(), file.ID); err == nil {
		t.Fatal("Delete accepted a Session Resource File")
	}
	if _, err := repo.Get(context.Background(), file.ID); err != nil {
		t.Fatalf("rejected delete changed File state: %v", err)
	}
	if got := string(blobs.objects[file.BlobKey]); got != "safe" {
		t.Fatalf("rejected delete changed blob = %q", got)
	}
}

func TestFileService_CleanupOutlivesCanceledRequest(t *testing.T) {
	repo := newMemoryFileRepository()
	repo.rejectCanceledCleanup = true
	blobs := newMemoryBlobStore()
	blobs.rejectCanceledCleanup = true
	service := NewFileService(repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{})
	pending := domain.File{
		ID: "file_pending_cancel", Filename: "pending.txt", MimeType: "text/plain",
		BlobKey: "files/file_pending_cancel", State: domain.FileStateUploading,
	}
	if err := repo.BeginUpload(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	blobs.objects[pending.BlobKey] = []byte("partial")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service.cleanupIncomplete(ctx, pending)
	if len(repo.files) != 0 || len(blobs.objects) != 0 {
		t.Fatalf("cleanup left row or blob: files=%+v objects=%+v", repo.files, blobs.objects)
	}
}

type memoryFileRepository struct {
	mu                     sync.Mutex
	files                  map[string]domain.File
	completeErrAfterCommit error
	rejectCanceledCleanup  bool
	protected              map[string]bool
}

func newMemoryFileRepository() *memoryFileRepository {
	return &memoryFileRepository{
		files: make(map[string]domain.File), protected: make(map[string]bool),
	}
}

func (r *memoryFileRepository) BeginUpload(_ context.Context, file domain.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[file.ID] = file
	return nil
}

func (r *memoryFileRepository) CompleteUpload(
	_ context.Context,
	id string,
	info BlobInfo,
) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, present := r.files[id]
	if !present || file.State != domain.FileStateUploading {
		return domain.File{}, domain.Conflict("not pending")
	}
	file.SizeBytes = info.SizeBytes
	file.ChecksumSHA256 = info.ChecksumSHA256
	file.State = domain.FileStateReady
	r.files[id] = file
	if r.completeErrAfterCommit != nil {
		return domain.File{}, r.completeErrAfterCommit
	}
	return file, nil
}

func (r *memoryFileRepository) Get(_ context.Context, id string) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, present := r.files[id]
	if !present || file.State != domain.FileStateReady {
		return domain.File{}, domain.NotFound("file not found")
	}
	return file, nil
}

func (r *memoryFileRepository) List(_ context.Context, query FileListQuery) (FileListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := make([]domain.File, 0, len(r.files))
	for _, file := range r.files {
		if file.State == domain.FileStateReady && (query.ScopeID == "" ||
			(file.Scope != nil && file.Scope.ID == query.ScopeID)) {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].CreatedAt.Equal(files[j].CreatedAt) {
			return files[i].ID > files[j].ID
		}
		return files[i].CreatedAt.After(files[j].CreatedAt)
	})
	if len(files) > query.Limit {
		return FileListPage{Files: files[:query.Limit], HasMore: true}, nil
	}
	return FileListPage{Files: files}, nil
}

func (r *memoryFileRepository) BeginDelete(_ context.Context, id string) (domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, present := r.files[id]
	if !present || file.State != domain.FileStateReady {
		return domain.File{}, domain.NotFound("file not found")
	}
	if r.protected[id] {
		return domain.File{}, domain.Conflict(
			"file is owned by a Session Resource; detach the resource first",
		)
	}
	file.State = domain.FileStateDeleting
	r.files[id] = file
	return file, nil
}

func (r *memoryFileRepository) RemoveIncomplete(ctx context.Context, id string) error {
	if r.rejectCanceledCleanup && ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if file, present := r.files[id]; present && file.State != domain.FileStateReady {
		delete(r.files, id)
	}
	return nil
}

func (r *memoryFileRepository) ListIncomplete(context.Context) ([]domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := make([]domain.File, 0)
	for _, file := range r.files {
		if file.State != domain.FileStateReady {
			files = append(files, file)
		}
	}
	return files, nil
}

type memoryBlobStore struct {
	objects               map[string][]byte
	putErr                error
	rejectCanceledCleanup bool
}

func newMemoryBlobStore() *memoryBlobStore {
	return &memoryBlobStore{objects: map[string][]byte{}}
}

func (s *memoryBlobStore) Put(
	_ context.Context,
	key string,
	_ string,
	body io.Reader,
	maxBytes int64,
) (BlobInfo, error) {
	if s.putErr != nil {
		return BlobInfo{}, s.putErr
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return BlobInfo{}, err
	}
	if int64(len(data)) > maxBytes {
		return BlobInfo{}, ErrBlobTooLarge
	}
	s.objects[key] = data
	return ComputeBlobInfo(data), nil
}

func (s *memoryBlobStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, present := s.objects[key]
	if !present {
		return nil, errors.New("missing blob")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *memoryBlobStore) Delete(ctx context.Context, key string) error {
	if s.rejectCanceledCleanup && ctx.Err() != nil {
		return ctx.Err()
	}
	delete(s.objects, key)
	return nil
}
