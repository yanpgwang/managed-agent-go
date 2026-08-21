package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

var _ app.FileRepository = (*FileRepository)(nil)
var _ app.SessionOutputRepository = (*FileRepository)(nil)

type FileRepository struct {
	store *Store
}

const insertFileStatement = `
INSERT INTO files (
    id, created_at, updated_at, filename, mime_type, size_bytes,
    downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state,
    workspace_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`

func NewFileRepository(store *Store) *FileRepository {
	return &FileRepository{store: store}
}

func (r *FileRepository) BeginUpload(ctx context.Context, file domain.File) error {
	workspaceID, err := r.store.workspaceForWrite(ctx)
	if err != nil {
		return err
	}
	file.CreatedAt = file.CreatedAt.UTC().Truncate(time.Microsecond)
	file.UpdatedAt = file.UpdatedAt.UTC().Truncate(time.Microsecond)
	var scopeID, scopeType *string
	if file.Scope != nil {
		scopeID, scopeType = &file.Scope.ID, &file.Scope.Type
	}
	if file.OutputPath != "" {
		return r.beginSessionOutputUpload(
			ctx, workspaceID, file, scopeID, scopeType,
		)
	}
	_, err = r.store.pool.Exec(ctx, insertFileStatement,
		file.ID, file.CreatedAt, file.UpdatedAt, file.Filename, file.MimeType,
		file.SizeBytes, file.Downloadable, file.Internal, scopeID, scopeType, file.BlobKey,
		file.ChecksumSHA256, nullableString(file.OutputPath), string(file.State),
		workspaceID,
	)
	if isUniqueViolation(err) {
		return domain.Conflict("file id already exists")
	}
	return err
}

func (r *FileRepository) beginSessionOutputUpload(
	ctx context.Context,
	workspaceID string,
	file domain.File,
	scopeID *string,
	scopeType *string,
) error {
	if file.Scope == nil || file.Scope.Type != "session" ||
		file.Scope.ID == "" || !file.Downloadable || file.Internal {
		return domain.Validation("invalid Session output File")
	}
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deletingAt *time.Time
	err = tx.QueryRow(ctx, `
SELECT deleting_at
FROM sessions
WHERE id = $1 AND ($2 = '' OR workspace_id = $2)
FOR UPDATE`, file.Scope.ID, workspaceID).Scan(&deletingAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound("session not found")
	}
	if err != nil {
		return err
	}
	if deletingAt != nil {
		return domain.Conflict("session deletion is in progress")
	}
	_, err = tx.Exec(ctx, insertFileStatement,
		file.ID, file.CreatedAt, file.UpdatedAt, file.Filename, file.MimeType,
		file.SizeBytes, file.Downloadable, file.Internal, scopeID, scopeType, file.BlobKey,
		file.ChecksumSHA256, nullableString(file.OutputPath), string(file.State),
		workspaceID,
	)
	if isUniqueViolation(err) {
		return domain.Conflict("file id already exists")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *FileRepository) CompleteUpload(
	ctx context.Context,
	id string,
	info app.BlobInfo,
) (domain.File, error) {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.File{}, err
	}
	row := r.store.pool.QueryRow(ctx, `
UPDATE files
SET size_bytes = $2,
    checksum_sha256 = $3,
    state = 'ready',
    updated_at = now()
WHERE id = $1 AND ($4 = '' OR workspace_id = $4) AND state = 'uploading'
RETURNING id, created_at, updated_at, filename, mime_type, size_bytes,
          downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state`,
		id, info.SizeBytes, info.ChecksumSHA256, workspaceID,
	)
	file, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.File{}, domain.Conflict("file upload is no longer pending")
	}
	return file, err
}

func (r *FileRepository) Get(ctx context.Context, id string) (domain.File, error) {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.File{}, err
	}
	row := r.store.pool.QueryRow(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE id = $1 AND ($2 = '' OR workspace_id = $2) AND state = 'ready'`, id, workspaceID)
	file, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.File{}, domain.NotFound("file not found")
	}
	return file, err
}

func (r *FileRepository) List(
	ctx context.Context,
	query app.FileListQuery,
) (app.FileListPage, error) {
	workspaceID, scoped, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return app.FileListPage{}, err
	}
	args := make([]any, 0, 5)
	where := []string{"state = 'ready'", "NOT internal_use"}
	if scoped {
		args = append(args, workspaceID)
		where = append(where, fmt.Sprintf("workspace_id = $%d", len(args)))
	}
	if query.ScopeID != "" {
		args = append(args, query.ScopeID)
		where = append(where, fmt.Sprintf("scope_id = $%d", len(args)))
	}

	before := query.BeforeID != ""
	cursorID := query.AfterID
	operator := "<"
	order := "created_at DESC, id DESC"
	if before {
		cursorID = query.BeforeID
		operator = ">"
		order = "created_at ASC, id ASC"
	}
	if cursorID != "" {
		boundary, err := r.Get(ctx, cursorID)
		if err != nil {
			var domainErr *domain.DomainError
			if errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound {
				return app.FileListPage{}, domain.Validation("file cursor not found")
			}
			return app.FileListPage{}, err
		}
		if boundary.Internal {
			return app.FileListPage{}, domain.Validation("file cursor not found")
		}
		args = append(args, boundary.CreatedAt, boundary.ID)
		where = append(where, fmt.Sprintf(
			"(created_at, id) %s ($%d, $%d)", operator, len(args)-1, len(args),
		))
	}
	args = append(args, query.Limit+1)
	statement := fmt.Sprintf(`
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE %s
ORDER BY %s
LIMIT $%d`, strings.Join(where, " AND "), order, len(args))
	rows, err := r.store.pool.Query(ctx, statement, args...)
	if err != nil {
		return app.FileListPage{}, err
	}
	defer rows.Close()
	files := make([]domain.File, 0, query.Limit+1)
	for rows.Next() {
		file, scanErr := scanFile(rows)
		if scanErr != nil {
			return app.FileListPage{}, scanErr
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return app.FileListPage{}, err
	}
	hasMore := len(files) > query.Limit
	if hasMore {
		files = files[:query.Limit]
	}
	if before {
		slices.Reverse(files)
	}
	return app.FileListPage{Files: files, HasMore: hasMore}, nil
}

func (r *FileRepository) BeginDelete(ctx context.Context, id string) (domain.File, error) {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return domain.File{}, err
	}
	row := r.store.pool.QueryRow(ctx, `
UPDATE files AS target
SET state = 'deleting', updated_at = now()
WHERE target.id = $1 AND ($2 = '' OR target.workspace_id = $2) AND target.state = 'ready'
	AND NOT target.internal_use
  AND NOT EXISTS (
      SELECT 1 FROM session_resources WHERE file_id = target.id
  )
RETURNING id, created_at, updated_at, filename, mime_type, size_bytes,
          downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state`, id, workspaceID)
	file, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		var protected bool
		if queryErr := r.store.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM session_resources AS resource
    JOIN sessions AS session ON session.id = resource.session_id
    JOIN files AS file ON file.id = resource.file_id
    WHERE resource.file_id = $1
      AND ($2 = '' OR session.workspace_id = $2)
      AND NOT file.internal_use
)`, id, workspaceID).Scan(&protected); queryErr != nil {
			return domain.File{}, queryErr
		}
		if protected {
			return domain.File{}, domain.Conflict(
				"file is owned by a Session Resource; detach the resource first",
			)
		}
		return domain.File{}, domain.NotFound("file not found")
	}
	return file, err
}

func (r *FileRepository) RemoveIncomplete(ctx context.Context, id string) error {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return err
	}
	_, err = r.store.pool.Exec(ctx, `
DELETE FROM files
WHERE id = $1 AND ($2 = '' OR workspace_id = $2) AND state <> 'ready'`, id, workspaceID)
	return err
}

func (r *FileRepository) ListIncomplete(ctx context.Context) ([]domain.File, error) {
	rows, err := r.store.pool.Query(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE state <> 'ready'
ORDER BY updated_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]domain.File, 0)
	for rows.Next() {
		file, scanErr := scanFile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

func (r *FileRepository) CompleteSessionOutput(
	ctx context.Context,
	id string,
	info app.BlobInfo,
) (app.SessionOutputCompletion, error) {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	pending, err := scanPendingSessionOutput(tx.QueryRow(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE id = $1 AND ($2 = '' OR workspace_id = $2) AND state = 'uploading'`,
		id, workspaceID,
	))
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}

	var deletingAt *time.Time
	err = tx.QueryRow(ctx, `
SELECT deleting_at
FROM sessions
WHERE id = $1 AND ($2 = '' OR workspace_id = $2)
FOR UPDATE`, pending.Scope.ID, workspaceID).Scan(&deletingAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.SessionOutputCompletion{}, domain.NotFound("session not found")
	}
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}
	if deletingAt != nil {
		return app.SessionOutputCompletion{}, domain.Conflict(
			"session deletion is in progress",
		)
	}
	// Lock the Session before its Files everywhere. Prepare/finalize deletion
	// use the same order, avoiding a Session/File lock inversion under a
	// concurrent idle publication and DELETE.
	pending, err = scanPendingSessionOutput(tx.QueryRow(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE id = $1 AND ($2 = '' OR workspace_id = $2) AND state = 'uploading'
FOR UPDATE`, id, workspaceID))
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}

	rows, err := tx.Query(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2 AND output_path = $3
  AND state IN ('ready', 'deleting')
FOR UPDATE`, workspaceID, pending.Scope.ID, pending.OutputPath)
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}
	var current *domain.File
	garbage := make([]domain.File, 0, 2)
	for rows.Next() {
		file, scanErr := scanFile(rows)
		if scanErr != nil {
			rows.Close()
			return app.SessionOutputCompletion{}, scanErr
		}
		if file.State == domain.FileStateReady {
			copy := file
			current = &copy
		} else {
			garbage = append(garbage, file)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return app.SessionOutputCompletion{}, err
	}
	rows.Close()

	if current != nil && current.ChecksumSHA256 == info.ChecksumSHA256 &&
		current.SizeBytes == info.SizeBytes {
		if err := tx.Commit(ctx); err != nil {
			return app.SessionOutputCompletion{}, err
		}
		return app.SessionOutputCompletion{
			File: *current, Garbage: garbage, Duplicate: true,
		}, nil
	}
	if current == nil {
		var readyCount int
		if err := tx.QueryRow(ctx, `
SELECT count(*)
FROM files
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2
  AND output_path IS NOT NULL AND state = 'ready'`,
			workspaceID, pending.Scope.ID,
		).Scan(&readyCount); err != nil {
			return app.SessionOutputCompletion{}, err
		}
		if readyCount >= app.MaxSessionOutputFiles {
			return app.SessionOutputCompletion{}, domain.TooLarge(
				"session outputs exceed 500 file limit",
			)
		}
	}
	if current != nil {
		if _, err := tx.Exec(ctx, `
UPDATE files SET state = 'deleting', updated_at = now()
WHERE id = $1 AND state = 'ready'`, current.ID); err != nil {
			return app.SessionOutputCompletion{}, err
		}
		current.State = domain.FileStateDeleting
		garbage = append(garbage, *current)
	}

	completed, err := scanFile(tx.QueryRow(ctx, `
UPDATE files
SET size_bytes = $2,
    checksum_sha256 = $3,
    state = 'ready',
    updated_at = now()
WHERE id = $1 AND state = 'uploading'
RETURNING id, created_at, updated_at, filename, mime_type, size_bytes,
          downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state`,
		id, info.SizeBytes, info.ChecksumSHA256,
	))
	if err != nil {
		return app.SessionOutputCompletion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.SessionOutputCompletion{}, err
	}
	return app.SessionOutputCompletion{File: completed, Garbage: garbage}, nil
}

// PrepareSessionOutputSnapshot makes the visible File set match the paths in a
// validated provider snapshot before new bytes are uploaded. The Session lock
// gives completion and deletion the same ordering and makes the 500-file limit
// apply across turns, not just within one tar stream.
func (r *FileRepository) PrepareSessionOutputSnapshot(
	ctx context.Context,
	sessionID string,
	outputPaths []string,
) (app.SessionOutputSnapshot, error) {
	if len(outputPaths) > app.MaxSessionOutputFiles {
		return app.SessionOutputSnapshot{}, domain.TooLarge("session outputs exceed 500 file limit")
	}
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return app.SessionOutputSnapshot{}, err
	}
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return app.SessionOutputSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deletingAt *time.Time
	if err := tx.QueryRow(ctx, `
SELECT deleting_at
FROM sessions
WHERE id = $1 AND ($2 = '' OR workspace_id = $2)
FOR UPDATE`, sessionID, workspaceID).Scan(&deletingAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.SessionOutputSnapshot{}, domain.NotFound("session not found")
		}
		return app.SessionOutputSnapshot{}, err
	}
	if deletingAt != nil {
		return app.SessionOutputSnapshot{}, domain.Conflict("session deletion is in progress")
	}
	if _, err := tx.Exec(ctx, `
UPDATE files
SET state = 'deleting', updated_at = now()
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2
  AND output_path IS NOT NULL AND state = 'ready'
  AND NOT (output_path = ANY($3::text[]))`,
		workspaceID, sessionID, outputPaths,
	); err != nil {
		return app.SessionOutputSnapshot{}, err
	}
	rows, err := tx.Query(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2
  AND output_path IS NOT NULL AND state IN ('ready', 'deleting')
ORDER BY id
FOR UPDATE`, workspaceID, sessionID)
	if err != nil {
		return app.SessionOutputSnapshot{}, err
	}
	current := make(map[string]domain.File)
	garbage := make([]domain.File, 0)
	for rows.Next() {
		file, scanErr := scanFile(rows)
		if scanErr != nil {
			rows.Close()
			return app.SessionOutputSnapshot{}, scanErr
		}
		if file.State == domain.FileStateReady {
			current[file.OutputPath] = file
		} else {
			garbage = append(garbage, file)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return app.SessionOutputSnapshot{}, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return app.SessionOutputSnapshot{}, err
	}
	return app.SessionOutputSnapshot{Current: current, Garbage: garbage}, nil
}

func (r *FileRepository) PrepareSessionOutputDeletion(
	ctx context.Context,
	sessionID string,
) ([]domain.File, error) {
	workspaceID, _, err := r.store.workspaceForRead(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `
SELECT true FROM sessions
WHERE id = $1 AND ($2 = '' OR workspace_id = $2)
FOR UPDATE`, sessionID, workspaceID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.NotFound("session not found")
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE files
SET state = 'deleting', updated_at = now()
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2
  AND output_path IS NOT NULL AND state = 'ready'`, workspaceID, sessionID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
SELECT id, created_at, updated_at, filename, mime_type, size_bytes,
       downloadable, internal_use, scope_id, scope_type, blob_key, checksum_sha256, output_path, state
FROM files
WHERE ($1 = '' OR workspace_id = $1)
  AND scope_type = 'session' AND scope_id = $2
  AND output_path IS NOT NULL AND state <> 'ready'
ORDER BY id
FOR UPDATE`, workspaceID, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]domain.File, 0)
	for rows.Next() {
		file, scanErr := scanFile(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return files, nil
}

func scanPendingSessionOutput(row fileScanner) (domain.File, error) {
	pending, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.File{}, domain.Conflict(
			"session output upload is no longer pending",
		)
	}
	if err != nil {
		return domain.File{}, err
	}
	if pending.Scope == nil || pending.Scope.Type != "session" ||
		pending.Scope.ID == "" || pending.OutputPath == "" || !pending.Downloadable {
		return domain.File{}, domain.Conflict(
			"file upload is not a Session output",
		)
	}
	return pending, nil
}

type fileScanner interface {
	Scan(...any) error
}

func scanFile(row fileScanner) (domain.File, error) {
	var file domain.File
	var scopeID, scopeType, outputPath *string
	err := row.Scan(
		&file.ID, &file.CreatedAt, &file.UpdatedAt, &file.Filename,
		&file.MimeType, &file.SizeBytes, &file.Downloadable,
		&file.Internal, &scopeID, &scopeType, &file.BlobKey, &file.ChecksumSHA256, &outputPath,
		&file.State,
	)
	if err != nil {
		return domain.File{}, err
	}
	file.CreatedAt = file.CreatedAt.UTC()
	file.UpdatedAt = file.UpdatedAt.UTC()
	if scopeID != nil && scopeType != nil {
		file.Scope = &domain.FileScope{ID: *scopeID, Type: *scopeType}
	}
	if outputPath != nil {
		file.OutputPath = *outputPath
	}
	return file, nil
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
