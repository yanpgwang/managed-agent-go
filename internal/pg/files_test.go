package pg

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

func TestFileRepository_LifecycleAndBidirectionalPaging(t *testing.T) {
	store := testStore(t)
	repo := NewFileRepository(store)
	ctx := context.Background()
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

	created := make([]domain.File, 0, 5)
	for index := 1; index <= 5; index++ {
		file := domain.File{
			ID:        "file_" + itoa(int64(index)),
			CreatedAt: base.Add(time.Duration(index) * time.Second),
			UpdatedAt: base.Add(time.Duration(index) * time.Second),
			Filename:  "file.txt", MimeType: "text/plain",
			BlobKey: "files/file_" + itoa(int64(index)), State: domain.FileStateUploading,
		}
		if index == 3 {
			file.Scope = &domain.FileScope{ID: "sesn_scope", Type: "session"}
			file.Downloadable = true
		}
		if err := repo.BeginUpload(ctx, file); err != nil {
			t.Fatalf("BeginUpload %d: %v", index, err)
		}
		ready, err := repo.CompleteUpload(ctx, file.ID, app.BlobInfo{
			SizeBytes: int64(index), ChecksumSHA256: "sum",
		})
		if err != nil {
			t.Fatalf("CompleteUpload %d: %v", index, err)
		}
		created = append(created, ready)
	}

	first, err := repo.List(ctx, app.FileListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertFileIDs(t, first.Files, "file_5", "file_4")
	if !first.HasMore {
		t.Fatal("first page missing has_more")
	}
	second, err := repo.List(ctx, app.FileListQuery{AfterID: "file_4", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertFileIDs(t, second.Files, "file_3", "file_2")
	if !second.HasMore {
		t.Fatal("second page missing has_more")
	}
	last, err := repo.List(ctx, app.FileListQuery{AfterID: "file_2", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertFileIDs(t, last.Files, "file_1")
	if last.HasMore {
		t.Fatal("last page has_more = true")
	}

	previous, err := repo.List(ctx, app.FileListQuery{BeforeID: "file_2", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertFileIDs(t, previous.Files, "file_4", "file_3")
	if !previous.HasMore {
		t.Fatal("backward page missing has_more")
	}
	newest, err := repo.List(ctx, app.FileListQuery{BeforeID: "file_4", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertFileIDs(t, newest.Files, "file_5")
	if newest.HasMore {
		t.Fatal("newest backward page has_more = true")
	}

	scoped, err := repo.List(ctx, app.FileListQuery{ScopeID: "sesn_scope", Limit: 20})
	if err != nil {
		t.Fatalf("scope filter = %+v, %v", scoped, err)
	}
	assertFileIDs(t, scoped.Files, "file_3")
	missingScope, err := repo.List(ctx, app.FileListQuery{ScopeID: "sesn_missing", Limit: 20})
	if err != nil || len(missingScope.Files) != 0 {
		t.Fatalf("missing scope filter = %+v, %v", missingScope, err)
	}
	if _, err := repo.List(ctx, app.FileListQuery{AfterID: "file_missing", Limit: 2}); err == nil {
		t.Fatal("missing cursor accepted")
	}

	deleting, err := repo.BeginDelete(ctx, created[2].ID)
	if err != nil || deleting.State != domain.FileStateDeleting {
		t.Fatalf("BeginDelete = %+v, %v", deleting, err)
	}
	if _, err := repo.Get(ctx, deleting.ID); err == nil {
		t.Fatal("deleting file remains visible")
	}
	incomplete, err := repo.ListIncomplete(ctx)
	if err != nil || len(incomplete) != 1 || incomplete[0].ID != deleting.ID {
		t.Fatalf("incomplete = %+v, %v", incomplete, err)
	}
	if err := repo.RemoveIncomplete(ctx, deleting.ID); err != nil {
		t.Fatal(err)
	}
	if incomplete, err = repo.ListIncomplete(ctx); err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete after remove = %+v, %v", incomplete, err)
	}
}

func TestFileRepository_SessionOutputReplacementAndCleanup(t *testing.T) {
	store := testStore(t)
	repo := NewFileRepository(store)
	ctx := context.Background()
	session := newSession("sesn_outputs")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	begin := func(id string) {
		t.Helper()
		now := time.Now().UTC()
		if err := repo.BeginUpload(ctx, domain.File{
			ID: id, CreatedAt: now, UpdatedAt: now,
			Filename: "reports__final.txt", MimeType: "text/plain",
			Downloadable: true,
			Scope:        &domain.FileScope{ID: session.ID, Type: "session"},
			BlobKey:      "files/" + id,
			OutputPath:   "reports/final.txt",
			State:        domain.FileStateUploading,
		}); err != nil {
			t.Fatalf("BeginUpload(%s): %v", id, err)
		}
	}

	firstInfo := app.ComputeBlobInfo([]byte("first"))
	begin("file_output_1")
	first, err := repo.CompleteSessionOutput(ctx, "file_output_1", firstInfo)
	if err != nil || first.Duplicate || first.File.State != domain.FileStateReady {
		t.Fatalf("first completion = %+v, %v", first, err)
	}
	if exists, err := store.SessionOutputFilesExist(ctx, session.ID); err != nil || !exists {
		t.Fatalf("SessionOutputFilesExist = %t, %v", exists, err)
	}

	begin("file_output_retry")
	retry, err := repo.CompleteSessionOutput(ctx, "file_output_retry", firstInfo)
	if err != nil || !retry.Duplicate || retry.File.ID != "file_output_1" {
		t.Fatalf("retry completion = %+v, %v", retry, err)
	}
	if err := repo.RemoveIncomplete(ctx, "file_output_retry"); err != nil {
		t.Fatal(err)
	}

	begin("file_output_2")
	second, err := repo.CompleteSessionOutput(
		ctx, "file_output_2", app.ComputeBlobInfo([]byte("second")),
	)
	if err != nil || second.Duplicate || second.File.ID != "file_output_2" ||
		len(second.Garbage) != 1 || second.Garbage[0].ID != "file_output_1" {
		t.Fatalf("replacement completion = %+v, %v", second, err)
	}
	page, err := repo.List(ctx, app.FileListQuery{ScopeID: session.ID, Limit: 20})
	if err != nil || len(page.Files) != 1 || page.Files[0].ID != "file_output_2" {
		t.Fatalf("visible outputs = %+v, %v", page, err)
	}
	if err := repo.RemoveIncomplete(ctx, "file_output_1"); err != nil {
		t.Fatal(err)
	}

	if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repo.BeginUpload(ctx, domain.File{
		ID: "file_after_fence", CreatedAt: now, UpdatedAt: now,
		Filename: "after.txt", MimeType: "text/plain", Downloadable: true,
		Scope:      &domain.FileScope{ID: session.ID, Type: "session"},
		BlobKey:    "files/file_after_fence",
		OutputPath: "after.txt",
		State:      domain.FileStateUploading,
	}); err == nil {
		t.Fatal("BeginUpload accepted a Session output after the deletion fence")
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err == nil {
		t.Fatal("FinalizeSessionDeletion removed a Session before output cleanup")
	}
	cleanup, err := repo.PrepareSessionOutputDeletion(ctx, session.ID)
	if err != nil || len(cleanup) != 1 || cleanup[0].ID != "file_output_2" ||
		cleanup[0].State != domain.FileStateDeleting {
		t.Fatalf("PrepareSessionOutputDeletion = %+v, %v", cleanup, err)
	}
	if err := repo.RemoveIncomplete(ctx, "file_output_2"); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.SessionOutputFilesExist(ctx, session.ID); err != nil || exists {
		t.Fatalf("SessionOutputFilesExist after cleanup = %t, %v", exists, err)
	}
	if incomplete, err := repo.ListIncomplete(ctx); err != nil || len(incomplete) != 0 {
		t.Fatalf("incomplete after cleanup = %+v, %v", incomplete, err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("FinalizeSessionDeletion after output cleanup: %v", err)
	}
}

func assertFileIDs(t *testing.T, files []domain.File, expected ...string) {
	t.Helper()
	if len(files) != len(expected) {
		t.Fatalf("file count = %d, want %d: %+v", len(files), len(expected), files)
	}
	for index, id := range expected {
		if files[index].ID != id {
			t.Fatalf("file[%d] = %s, want %s", index, files[index].ID, id)
		}
	}
}
