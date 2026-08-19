package app

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/sandbox"
	"github.com/yanpgwang/mango/internal/workspace"
)

const (
	MaxSessionOutputFiles      = 500
	MaxSessionOutputPathRunes  = 1024
	sessionOutputHashRunes     = 16
	sessionOutputFilenameRunes = 255
)

// SessionOutputPublisher snapshots regular files from the runtime-owned
// deliverable directory into Mango's Files object store. The relative output
// path is the logical identity: unchanged retries reuse the visible File,
// while changed content atomically replaces its previous ready version.
type SessionOutputPublisher struct {
	repo  SessionOutputRepository
	blobs FileBlobStore
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewSessionOutputPublisher(
	repo SessionOutputRepository,
	blobs FileBlobStore,
	ids domain.IDGenerator,
	clock domain.Clock,
) *SessionOutputPublisher {
	return &SessionOutputPublisher{repo: repo, blobs: blobs, ids: ids, clock: clock}
}

func (p *SessionOutputPublisher) Publish(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if sessionID == "" {
		return domain.Validation("session id is required")
	}
	exporter, ok := box.(sandbox.SessionOutputSandbox)
	if !ok {
		return sandbox.Permanent(errors.New(
			"sandbox: provider does not expose Session outputs",
		))
	}
	archive, err := exporter.OpenSessionOutputs(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()

	reader := tar.NewReader(archive)
	count := 0
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("session outputs: read archive: %w", nextErr)
		}
		relativePath, pathErr := normalizeSessionOutputPath(header.Name)
		if pathErr != nil {
			return pathErr
		}
		if relativePath == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return domain.Validation("session output archive root is not a directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, 0:
			// Continue below.
		default:
			return domain.Validation(fmt.Sprintf(
				"session output %q is not a regular file", relativePath,
			))
		}
		count++
		if count > MaxSessionOutputFiles {
			return domain.TooLarge("session outputs exceed 500 file limit")
		}
		if header.Size < 0 || header.Size > MaxFileBytes {
			return domain.TooLarge(fmt.Sprintf(
				"session output %q exceeds 500 MB limit", relativePath,
			))
		}
		if err := p.publishOne(ctx, sessionID, relativePath, header.Size, reader); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionOutputPublisher) publishOne(
	ctx context.Context,
	sessionID string,
	outputPath string,
	size int64,
	body io.Reader,
) error {
	mimeType := mime.TypeByExtension(path.Ext(outputPath))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	} else if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}
	now := p.clock.Now().UTC()
	id := p.ids.NewID(domain.PrefixFile)
	file := domain.File{
		ID: id, CreatedAt: now, UpdatedAt: now,
		Filename:     sessionOutputFilename(outputPath),
		MimeType:     mimeType,
		Downloadable: true,
		Scope:        &domain.FileScope{ID: sessionID, Type: "session"},
		BlobKey:      workspace.BlobKey(ctx, "files/"+id),
		OutputPath:   outputPath,
		State:        domain.FileStateUploading,
	}
	if err := p.repo.BeginUpload(ctx, file); err != nil {
		return err
	}
	info, err := p.blobs.Put(ctx, file.BlobKey, mimeType, io.LimitReader(body, size), MaxFileBytes)
	if err != nil {
		p.cleanupIncomplete(ctx, file)
		if errors.Is(err, ErrBlobTooLarge) {
			return domain.TooLarge(fmt.Sprintf(
				"session output %q exceeds 500 MB limit", outputPath,
			))
		}
		return err
	}
	if info.SizeBytes != size {
		p.cleanupIncomplete(ctx, file)
		return fmt.Errorf(
			"session outputs: %q archive size changed: read %d bytes, expected %d",
			outputPath, info.SizeBytes, size,
		)
	}
	completion, err := p.repo.CompleteSessionOutput(ctx, id, info)
	if err != nil {
		// Completion may have committed before its acknowledgement was lost.
		// Reconciliation determines whether this intent is ready or incomplete;
		// eagerly deleting the blob could corrupt a visible File.
		return err
	}
	garbage := completion.Garbage
	if completion.Duplicate {
		garbage = append(garbage, file)
	}
	return p.cleanupFiles(ctx, garbage)
}

// CleanupSession hides all runtime-produced Files before deleting their blobs
// and metadata. It is idempotent so a Session deletion Activity can retry.
func (p *SessionOutputPublisher) CleanupSession(ctx context.Context, sessionID string) error {
	files, err := p.repo.PrepareSessionOutputDeletion(ctx, sessionID)
	if err != nil {
		return err
	}
	return p.cleanupFiles(ctx, files)
}

func (p *SessionOutputPublisher) cleanupFiles(ctx context.Context, files []domain.File) error {
	for _, file := range files {
		if err := p.blobs.Delete(ctx, file.BlobKey); err != nil {
			return err
		}
		if err := p.repo.RemoveIncomplete(ctx, file.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p *SessionOutputPublisher) cleanupIncomplete(ctx context.Context, file domain.File) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fileCleanupTimeout)
	defer cancel()
	_ = p.blobs.Delete(cleanupCtx, file.BlobKey)
	_ = p.repo.RemoveIncomplete(cleanupCtx, file.ID)
}

func normalizeSessionOutputPath(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", domain.Validation("session output path must be valid UTF-8")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", domain.Validation("session output path contains a control character")
		}
	}
	trimmed := value
	for strings.HasPrefix(trimmed, "./") {
		trimmed = strings.TrimPrefix(trimmed, "./")
	}
	if trimmed == "" {
		trimmed = "."
	}
	if path.IsAbs(trimmed) {
		return "", domain.Validation("session output path must be relative")
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == ".." {
			return "", domain.Validation("session output path escapes the outputs directory")
		}
		if utf8.RuneCountInString(component) > 255 {
			return "", domain.Validation("session output path component exceeds 255 characters")
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", domain.Validation("session output path escapes the outputs directory")
	}
	if cleaned != "." && utf8.RuneCountInString(cleaned) > MaxSessionOutputPathRunes {
		return "", domain.Validation("session output path exceeds 1024 characters")
	}
	return cleaned, nil
}

func sessionOutputFilename(outputPath string) string {
	var builder strings.Builder
	for _, r := range outputPath {
		switch {
		case r == '/':
			builder.WriteString("__")
		case unicode.IsControl(r) || strings.ContainsRune("<>:\"|?*\\", r):
			builder.WriteRune('_')
		default:
			builder.WriteRune(r)
		}
	}
	name := builder.String()
	if utf8.RuneCountInString(name) <= sessionOutputFilenameRunes {
		return name
	}
	sum := sha256.Sum256([]byte(outputPath))
	suffix := "-" + hex.EncodeToString(sum[:])[:sessionOutputHashRunes]
	keep := sessionOutputFilenameRunes - utf8.RuneCountInString(suffix)
	runes := []rune(name)
	return string(runes[:keep]) + suffix
}
