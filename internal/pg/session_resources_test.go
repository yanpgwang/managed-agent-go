package pg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestSessionResources_LimitPagingAndConcurrentCollision(t *testing.T) {
	store := testStore(t)
	repository := NewFileRepository(store)
	ctx := context.Background()
	session := newSession("sesn_resources")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	first := prepareSessionResource(t, repository, session.ID, "first", "/mnt/session/uploads/first")
	if _, err := store.AddSessionResource(ctx, first, 1, app.MaxSessionResourceBytes); err != nil {
		t.Fatalf("add first resource: %v", err)
	}
	limitCandidate := prepareSessionResource(t, repository, session.ID, "limit", "/mnt/session/uploads/limit")
	if _, err := store.AddSessionResource(
		ctx, limitCandidate, 1, app.MaxSessionResourceBytes,
	); !isConflict(err) {
		t.Fatalf("add beyond configured resource cap = %v, want conflict", err)
	}
	byteCandidate := prepareSessionResource(
		t, repository, session.ID, "bytes", "/mnt/session/uploads/bytes",
	)
	if _, err := store.AddSessionResource(
		ctx, byteCandidate, 500, first.Blob.SizeBytes,
	); !isTooLarge(err) {
		t.Fatalf("add beyond configured byte cap = %v, want too-large error", err)
	}

	second := prepareSessionResource(t, repository, session.ID, "second", "/mnt/session/uploads/second")
	if _, err := store.AddSessionResource(ctx, second, 500, app.MaxSessionResourceBytes); err != nil {
		t.Fatalf("add second resource: %v", err)
	}
	page, err := store.ListSessionResources(ctx, session.ID, app.SessionResourceListQuery{Limit: 1})
	if err != nil || len(page.Resources) != 1 || !page.HasMore {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := store.ListSessionResources(ctx, session.ID, app.SessionResourceListQuery{
		Limit: 1,
		Boundary: &app.SessionResourcePageBoundary{
			CreatedAt: page.Resources[0].CreatedAt,
			ID:        page.Resources[0].ID,
		},
	})
	if err != nil || len(next.Resources) != 1 || next.HasMore ||
		next.Resources[0].ID == page.Resources[0].ID {
		t.Fatalf("second page = %+v, %v", next, err)
	}

	left := prepareSessionResource(t, repository, session.ID, "left", "/mnt/session/uploads/collision")
	right := prepareSessionResource(t, repository, session.ID, "right", "/mnt/session/uploads/collision/child")
	var wait sync.WaitGroup
	wait.Add(2)
	errorsOut := make(chan error, 2)
	for _, prepared := range []app.PreparedSessionResource{left, right} {
		prepared := prepared
		go func() {
			defer wait.Done()
			_, err := store.AddSessionResource(ctx, prepared, 500, app.MaxSessionResourceBytes)
			errorsOut <- err
		}()
	}
	wait.Wait()
	close(errorsOut)
	var successes, conflicts int
	for err := range errorsOut {
		switch {
		case err == nil:
			successes++
		case isConflict(err):
			conflicts++
		default:
			t.Fatalf("concurrent add: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent add successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}

	addition := prepareSessionResource(t, repository, session.ID, "addition", "/mnt/session/uploads/addition")
	if _, err := store.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, first.File.ID); err != nil {
		t.Fatalf("simulate missing scoped File: %v", err)
	}
	operations := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := store.AddSessionResource(ctx, addition, 500, app.MaxSessionResourceBytes)
		operations <- err
	}()
	go func() {
		defer wait.Done()
		_, err := store.BeginSessionResourceDeletion(ctx, session.ID, first.Resource.ID)
		operations <- err
	}()
	wait.Wait()
	close(operations)
	for err := range operations {
		if err != nil {
			t.Fatalf("concurrent add/delete: %v", err)
		}
	}
	active, err := store.ListSessionResources(ctx, session.ID, app.SessionResourceListQuery{Limit: 500})
	if err != nil || len(active.Resources) != 3 {
		t.Fatalf("resources after concurrent add/delete = %+v, %v; want 3 active", active, err)
	}
}

func TestCreateSessionWithResources_RollsBackProjectionAndFilePublication(t *testing.T) {
	store := testStore(t)
	repository := NewFileRepository(store)
	ctx := context.Background()
	session := newSession("sesn_resource_rollback")
	first := prepareSessionResource(t, repository, session.ID, "rollback_a", "/mnt/session/uploads/duplicate")
	second := prepareSessionResource(t, repository, session.ID, "rollback_b", "/mnt/session/uploads/duplicate")

	if _, err := store.createSession(
		ctx,
		session,
		[]domain.EventDraft{{Type: domain.EvUserMessage, Payload: map[string]any{"content": "hello"}}},
		false,
		[]app.PreparedSessionResource{first, second},
	); !isConflict(err) {
		t.Fatalf("create with duplicate resource mount = %v, want conflict", err)
	}
	if _, err := store.GetSession(ctx, session.ID); err == nil {
		t.Fatal("rolled-back Session remains visible")
	}
	if _, err := repository.Get(ctx, first.File.ID); err == nil {
		t.Fatal("first prepared File was published despite transaction rollback")
	}
	if _, err := repository.Get(ctx, second.File.ID); err == nil {
		t.Fatal("second prepared File was published despite transaction rollback")
	}
	incomplete, err := repository.ListIncomplete(ctx)
	if err != nil || len(incomplete) != 2 {
		t.Fatalf("prepared upload intents after rollback = %+v, %v; want 2", incomplete, err)
	}
}

func prepareSessionResource(
	t *testing.T,
	repository *FileRepository,
	sessionID string,
	suffix string,
	mountPath string,
) app.PreparedSessionResource {
	t.Helper()
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(suffix)) * time.Second)
	file := domain.File{
		ID: "file_" + suffix, CreatedAt: now, UpdatedAt: now,
		Filename: suffix + ".txt", MimeType: "text/plain", Downloadable: true,
		Scope:   &domain.FileScope{ID: sessionID, Type: "session"},
		BlobKey: "files/file_" + suffix, State: domain.FileStateUploading,
	}
	if err := repository.BeginUpload(context.Background(), file); err != nil {
		t.Fatalf("prepare File %s: %v", suffix, err)
	}
	return app.PreparedSessionResource{
		Resource: domain.SessionResource{
			ID: "sesrsc_" + suffix, SessionID: sessionID,
			SourceFileID: "file_source_" + suffix,
			FileID:       file.ID,
			MountPath:    mountPath,
			CreatedAt:    now,
			UpdatedAt:    now,
			State:        domain.SessionResourceActive,
		},
		File: file,
		Blob: app.BlobInfo{SizeBytes: int64(len(suffix)), ChecksumSHA256: "checksum_" + suffix},
	}
}

func isConflict(err error) bool {
	var domainErr *domain.DomainError
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindConflict
}

func isTooLarge(err error) bool {
	var domainErr *domain.DomainError
	return errors.As(err, &domainErr) && domainErr.Kind == domain.KindTooLarge
}
