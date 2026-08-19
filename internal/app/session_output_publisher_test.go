package app

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/sandbox"
)

func TestSessionOutputPublisher_PublishesAndIdempotentlyReplaces(t *testing.T) {
	repo := newMemoryFileRepository()
	blobs := newMemoryBlobStore()
	publisher := NewSessionOutputPublisher(
		repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(10, 0)},
	)
	box := outputArchiveSandbox{archive: outputArchive(t, []outputArchiveEntry{
		{name: "reports", typeflag: tar.TypeDir},
		{name: "reports/final.txt", body: "first"},
		{name: "data.json", body: "{}"},
	})}
	if err := publisher.Publish(context.Background(), "sesn_1", box); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	assertReadySessionOutput(t, repo, blobs, "sesn_1", "reports/final.txt", "first")
	assertReadySessionOutput(t, repo, blobs, "sesn_1", "data.json", "{}")
	if len(repo.files) != 2 || len(blobs.objects) != 2 {
		t.Fatalf("initial publication rows=%d blobs=%d", len(repo.files), len(blobs.objects))
	}

	if err := publisher.Publish(context.Background(), "sesn_1", box); err != nil {
		t.Fatalf("idempotent Publish: %v", err)
	}
	if len(repo.files) != 2 || len(blobs.objects) != 2 {
		t.Fatalf("retry rows=%d blobs=%d, want 2/2", len(repo.files), len(blobs.objects))
	}

	box.archive = outputArchive(t, []outputArchiveEntry{
		{name: "reports/final.txt", body: "second"},
		{name: "data.json", body: "{}"},
	})
	if err := publisher.Publish(context.Background(), "sesn_1", box); err != nil {
		t.Fatalf("replacement Publish: %v", err)
	}
	assertReadySessionOutput(t, repo, blobs, "sesn_1", "reports/final.txt", "second")
	if len(repo.files) != 2 || len(blobs.objects) != 2 {
		t.Fatalf("replacement rows=%d blobs=%d, want 2/2", len(repo.files), len(blobs.objects))
	}

	if err := publisher.CleanupSession(context.Background(), "sesn_1"); err != nil {
		t.Fatalf("CleanupSession: %v", err)
	}
	if len(repo.files) != 0 || len(blobs.objects) != 0 {
		t.Fatalf("cleanup rows=%d blobs=%d, want 0/0", len(repo.files), len(blobs.objects))
	}
}

func TestSessionOutputPublisher_RejectsUnsafeArchiveEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry outputArchiveEntry
	}{
		{name: "traversal", entry: outputArchiveEntry{name: "../secret", body: "no"}},
		{name: "absolute", entry: outputArchiveEntry{name: "/etc/passwd", body: "no"}},
		{name: "symlink", entry: outputArchiveEntry{
			name: "link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd",
		}},
		{name: "hardlink", entry: outputArchiveEntry{
			name: "hard", typeflag: tar.TypeLink, linkname: "target",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newMemoryFileRepository()
			blobs := newMemoryBlobStore()
			publisher := NewSessionOutputPublisher(
				repo, blobs, domain.NewSeqIDGen(), domain.FixedClock{},
			)
			err := publisher.Publish(context.Background(), "sesn_1", outputArchiveSandbox{
				archive: outputArchive(t, []outputArchiveEntry{test.entry}),
			})
			var domainErr *domain.DomainError
			if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindValidation {
				t.Fatalf("Publish error = %v, want validation", err)
			}
			if len(repo.files) != 0 || len(blobs.objects) != 0 {
				t.Fatalf("unsafe entry left rows=%d blobs=%d", len(repo.files), len(blobs.objects))
			}
		})
	}
}

func TestSessionOutputPublisher_EnforcesFileCount(t *testing.T) {
	entries := make([]outputArchiveEntry, MaxSessionOutputFiles+1)
	for index := range entries {
		entries[index] = outputArchiveEntry{name: "file-" + strconv.Itoa(index), body: ""}
	}
	publisher := NewSessionOutputPublisher(
		newMemoryFileRepository(), newMemoryBlobStore(),
		domain.NewSeqIDGen(), domain.FixedClock{},
	)
	err := publisher.Publish(context.Background(), "sesn_1", outputArchiveSandbox{
		archive: outputArchive(t, entries),
	})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindTooLarge {
		t.Fatalf("Publish error = %v, want too large", err)
	}
}

func assertReadySessionOutput(
	t *testing.T,
	repo *memoryFileRepository,
	blobs *memoryBlobStore,
	sessionID string,
	outputPath string,
	want string,
) {
	t.Helper()
	for _, file := range repo.files {
		if file.State != domain.FileStateReady || file.OutputPath != outputPath {
			continue
		}
		if file.Scope == nil || file.Scope.ID != sessionID || file.Scope.Type != "session" ||
			!file.Downloadable {
			t.Fatalf("output metadata = %+v", file)
		}
		if got := string(blobs.objects[file.BlobKey]); got != want {
			t.Fatalf("output %q bytes = %q, want %q", outputPath, got, want)
		}
		return
	}
	t.Fatalf("ready output %q not found in %+v", outputPath, repo.files)
}

type outputArchiveEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func outputArchive(t *testing.T, entries []outputArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(entry.body))
		if typeflag != tar.TypeReg {
			size = 0
		}
		if err := writer.WriteHeader(&tar.Header{
			Name: entry.name, Typeflag: typeflag, Size: size,
			Mode: 0o644, Linkname: entry.linkname,
		}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type outputArchiveSandbox struct {
	archive []byte
}

func (s outputArchiveSandbox) Exec(context.Context, sandbox.Command) (*sandbox.Result, error) {
	return nil, errors.New("not implemented")
}
func (s outputArchiveSandbox) ReadFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (s outputArchiveSandbox) WriteFile(context.Context, string, []byte) error {
	return errors.New("not implemented")
}
func (s outputArchiveSandbox) Root() string                  { return "/workspace" }
func (s outputArchiveSandbox) Destroy(context.Context) error { return nil }
func (s outputArchiveSandbox) OpenSessionOutputs(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.archive)), nil
}
