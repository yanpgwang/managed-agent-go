package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	DefaultFileListLimit       = 20
	MaxFileListLimit           = 1000
	MaxFileBytes         int64 = 500 << 20
	fileCleanupTimeout         = 10 * time.Second
)

// ErrBlobTooLarge lets a streaming blob implementation stop after one byte
// beyond MaxFileBytes without buffering the complete upload.
var ErrBlobTooLarge = errors.New("blob exceeds maximum size")

type FileUploadInput struct {
	Filename string
	MimeType string
	Body     io.Reader
}

type FileListQuery struct {
	AfterID  string
	BeforeID string
	ScopeID  string
	Limit    int
}

type FileListPage struct {
	Files   []domain.File
	HasMore bool
}

type BlobInfo struct {
	SizeBytes      int64
	ChecksumSHA256 string
}

type FileDownload struct {
	File domain.File
	Body io.ReadCloser
}

// FileRepository owns metadata and lifecycle intents. BeginUpload must commit
// before blob I/O; CompleteUpload is the only transition that makes a file
// visible. BeginDelete hides a file before object deletion begins.
type FileRepository interface {
	BeginUpload(context.Context, domain.File) error
	CompleteUpload(context.Context, string, BlobInfo) (domain.File, error)
	Get(context.Context, string) (domain.File, error)
	List(context.Context, FileListQuery) (FileListPage, error)
	BeginDelete(context.Context, string) (domain.File, error)
	RemoveIncomplete(context.Context, string) error
	ListIncomplete(context.Context) ([]domain.File, error)
}

// FileBlobStore is intentionally small so S3-compatible storage can be used
// without leaking provider types into the application or HTTP layers.
type FileBlobStore interface {
	Put(context.Context, string, string, io.Reader, int64) (BlobInfo, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type FileService struct {
	repo  FileRepository
	blobs FileBlobStore
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewFileService(
	repo FileRepository,
	blobs FileBlobStore,
	ids domain.IDGenerator,
	clock domain.Clock,
) *FileService {
	return &FileService{repo: repo, blobs: blobs, ids: ids, clock: clock}
}

func (s *FileService) Upload(ctx context.Context, input FileUploadInput) (domain.File, error) {
	filename, mimeType, err := validateFileUpload(input)
	if err != nil {
		return domain.File{}, err
	}
	now := s.clock.Now().UTC()
	id := s.ids.NewID(domain.PrefixFile)
	file := domain.File{
		ID: id, CreatedAt: now, UpdatedAt: now,
		Filename: filename, MimeType: mimeType,
		BlobKey: "files/" + id, State: domain.FileStateUploading,
	}
	if err := s.repo.BeginUpload(ctx, file); err != nil {
		return domain.File{}, err
	}

	info, err := s.blobs.Put(ctx, file.BlobKey, mimeType, input.Body, MaxFileBytes)
	if err != nil {
		s.cleanupIncomplete(ctx, file)
		if errors.Is(err, ErrBlobTooLarge) {
			return domain.File{}, domain.TooLarge("file exceeds 500 MB limit")
		}
		return domain.File{}, err
	}
	completed, err := s.repo.CompleteUpload(ctx, id, info)
	if err != nil {
		// The database update may have committed even when the client observes a
		// connection error. Deleting the blob here could leave a visible ready
		// row with no bytes. Preserve both sides: a committed row remains valid,
		// while an uncommitted uploading intent is removed by reconciliation.
		return domain.File{}, err
	}
	return completed, nil
}

func (s *FileService) Get(ctx context.Context, id string) (domain.File, error) {
	return s.repo.Get(ctx, id)
}

func (s *FileService) List(ctx context.Context, query FileListQuery) (FileListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultFileListLimit
	}
	if query.Limit < 1 || query.Limit > MaxFileListLimit {
		return FileListPage{}, domain.Validation("limit must be between 1 and 1000")
	}
	if query.AfterID != "" && query.BeforeID != "" {
		return FileListPage{}, domain.Validation("after_id and before_id cannot be combined")
	}
	return s.repo.List(ctx, query)
}

func (s *FileService) Download(ctx context.Context, id string) (FileDownload, error) {
	file, err := s.repo.Get(ctx, id)
	if err != nil {
		return FileDownload{}, err
	}
	if !file.Downloadable {
		return FileDownload{}, domain.Validation("file is not downloadable")
	}
	body, err := s.blobs.Open(ctx, file.BlobKey)
	if err != nil {
		return FileDownload{}, err
	}
	return FileDownload{File: file, Body: body}, nil
}

func (s *FileService) Delete(ctx context.Context, id string) (domain.File, error) {
	file, err := s.repo.BeginDelete(ctx, id)
	if err != nil {
		return domain.File{}, err
	}
	if err := s.blobs.Delete(ctx, file.BlobKey); err != nil {
		return domain.File{}, err
	}
	if err := s.repo.RemoveIncomplete(ctx, id); err != nil {
		return domain.File{}, err
	}
	return file, nil
}

// Reconcile removes objects and rows left by an API-process crash during an
// upload or delete. It runs before the HTTP server starts accepting requests.
func (s *FileService) Reconcile(ctx context.Context) error {
	files, err := s.repo.ListIncomplete(ctx)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := s.blobs.Delete(ctx, file.BlobKey); err != nil {
			return err
		}
		if err := s.repo.RemoveIncomplete(ctx, file.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileService) cleanupIncomplete(ctx context.Context, file domain.File) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fileCleanupTimeout)
	defer cancel()
	_ = s.blobs.Delete(cleanupCtx, file.BlobKey)
	_ = s.repo.RemoveIncomplete(cleanupCtx, file.ID)
}

func validateFileUpload(input FileUploadInput) (string, string, error) {
	name := input.Filename
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 {
		return "", "", domain.Validation("filename must contain between 1 and 255 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune("<>:\"|?*/\\", r) {
			return "", "", domain.Validation("filename contains a forbidden character")
		}
	}
	mediaType, _, err := mime.ParseMediaType(input.MimeType)
	if err != nil || mediaType == "" {
		return "", "", domain.Validation("file content type is invalid")
	}
	if input.Body == nil {
		return "", "", domain.Validation("file is required")
	}
	return name, mediaType, nil
}

// ComputeBlobInfo is shared by blob implementations and test doubles.
func ComputeBlobInfo(body []byte) BlobInfo {
	sum := sha256.Sum256(body)
	return BlobInfo{SizeBytes: int64(len(body)), ChecksumSHA256: hex.EncodeToString(sum[:])}
}
