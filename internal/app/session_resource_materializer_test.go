package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/sandbox"
)

type changingSessionResourceRepository struct {
	resource domain.SessionResource
}

func (r changingSessionResourceRepository) SessionResourcesForReconcile(
	context.Context,
	string,
) ([]domain.SessionResource, error) {
	return []domain.SessionResource{r.resource}, nil
}

func (changingSessionResourceRepository) GetSessionResource(
	context.Context,
	string,
	string,
) (domain.SessionResource, error) {
	return domain.SessionResource{}, domain.NotFound("session resource not found")
}

func (changingSessionResourceRepository) FinalizeSessionResourceDeletion(
	context.Context,
	string,
	string,
) error {
	return nil
}

type trackingFileResourceSandbox struct {
	identity string
	data     []byte
	removed  []string
}

func (*trackingFileResourceSandbox) Root() string { return "/workspace" }
func (*trackingFileResourceSandbox) Exec(context.Context, sandbox.Command) (*sandbox.Result, error) {
	return &sandbox.Result{}, nil
}
func (*trackingFileResourceSandbox) ReadFile(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (*trackingFileResourceSandbox) WriteFile(context.Context, string, []byte) error { return nil }
func (*trackingFileResourceSandbox) Destroy(context.Context) error                   { return nil }
func (*trackingFileResourceSandbox) HasFileResource(
	context.Context,
	sandbox.FileResourceMount,
) (bool, error) {
	return false, nil
}
func (s *trackingFileResourceSandbox) ImportFileResource(
	_ context.Context,
	mount sandbox.FileResourceMount,
	body io.Reader,
) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.identity = mount.Identity
	s.data = data
	return nil
}
func (s *trackingFileResourceSandbox) RemoveFileResource(
	_ context.Context,
	_ string,
	identity string,
) error {
	s.removed = append(s.removed, identity)
	if s.identity == identity {
		s.identity = ""
		s.data = nil
	}
	return nil
}

func TestSessionResourceMaterializerRemovesLatePublishAfterDeletion(t *testing.T) {
	ctx := context.Background()
	files := newMemoryFileRepository()
	blobs := newMemoryBlobStore()
	file := domain.File{
		ID: "file_copy", Filename: "copy.txt", MimeType: "text/plain",
		BlobKey: "files/file_copy", State: domain.FileStateUploading,
	}
	if err := files.BeginUpload(ctx, file); err != nil {
		t.Fatal(err)
	}
	content := []byte("late content")
	if _, err := files.CompleteUpload(ctx, file.ID, ComputeBlobInfo(content)); err != nil {
		t.Fatal(err)
	}
	blobs.objects[file.BlobKey] = append([]byte(nil), content...)
	resource := domain.SessionResource{
		ID: "sesrsc_late", SessionID: "sesn_1", FileID: file.ID,
		MountPath: "/mnt/session/uploads/late.txt", State: domain.SessionResourceActive,
	}
	box := &trackingFileResourceSandbox{}
	materializer := NewSessionResourceMaterializer(
		changingSessionResourceRepository{resource: resource}, files, blobs,
	)

	err := materializer.Reconcile(ctx, resource.SessionID, box)
	if err == nil || !strings.Contains(err.Error(), "changed during materialization") {
		t.Fatalf("Reconcile error = %v, want retry signal", err)
	}
	if len(box.removed) != 1 || box.removed[0] != resource.ID || len(box.data) != 0 {
		t.Fatalf("late publish cleanup: removed=%v identity=%q data=%q", box.removed, box.identity, box.data)
	}
}

func TestSessionResourceMaterializerRejectsUnsupportedSandboxPermanently(t *testing.T) {
	resource := domain.SessionResource{
		ID: "sesrsc_unsupported", SessionID: "sesn_1", FileID: "file_1",
		MountPath: "/mnt/session/uploads/file_1", State: domain.SessionResourceActive,
	}
	repository := changingSessionResourceRepository{resource: resource}
	materializer := NewSessionResourceMaterializer(repository, newMemoryFileRepository(), newMemoryBlobStore())
	box := struct{ sandbox.Sandbox }{}

	err := materializer.Reconcile(context.Background(), resource.SessionID, box)
	if !sandbox.IsPermanent(err) {
		t.Fatalf("Reconcile error = %v, want permanent", err)
	}
}

func TestTrackingSandboxIdentityDoesNotRemoveReplacement(t *testing.T) {
	box := &trackingFileResourceSandbox{identity: "sesrsc_new", data: []byte("new")}
	if err := box.RemoveFileResource(
		context.Background(), "/mnt/session/uploads/same", "sesrsc_old",
	); err != nil {
		t.Fatal(err)
	}
	if box.identity != "sesrsc_new" || !bytes.Equal(box.data, []byte("new")) {
		t.Fatalf("stale identity removed replacement: identity=%q data=%q", box.identity, box.data)
	}
}
