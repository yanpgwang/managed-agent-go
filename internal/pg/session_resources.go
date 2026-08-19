package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/pg/pgstore"
)

func insertPreparedSessionResources(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
	sessionID string,
	prepared []app.PreparedSessionResource,
) error {
	for _, item := range prepared {
		resource := item.Resource
		if resource.Type() == domain.SessionResourceTypeMemoryStore {
			if resource.SessionID != sessionID || resource.MemoryStoreID == "" ||
				resource.FileID != "" || resource.SourceFileID != "" ||
				resource.State != domain.SessionResourceActive ||
				(resource.MemoryAccess != domain.MemoryAccessReadWrite &&
					resource.MemoryAccess != domain.MemoryAccessReadOnly) {
				return errors.New("pg: invalid prepared Memory Store Resource ownership")
			}
			var active int
			if err := tx.QueryRow(ctx, `
SELECT 1 FROM memory_stores
WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL
FOR SHARE`, resource.MemoryStoreID, workspaceID).Scan(&active); errors.Is(err, pgx.ErrNoRows) {
				return domain.Validation("memory store is missing or archived")
			} else if err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
INSERT INTO session_resources (
    id, session_id, resource_type, memory_store_id, memory_access,
    memory_instructions, memory_store_name, memory_store_description,
    mount_path, state, created_at, updated_at
) VALUES ($1, $2, 'memory_store', $3, $4, $5, $6, $7, $8, 'active', $9, $10)`,
				resource.ID,
				resource.SessionID,
				resource.MemoryStoreID,
				resource.MemoryAccess,
				resource.MemoryInstructions,
				resource.MemoryStoreName,
				resource.MemoryStoreDescription,
				resource.MountPath,
				resource.CreatedAt.UTC(),
				resource.UpdatedAt.UTC(),
			)
			if isUniqueViolation(err) {
				return domain.Conflict("a Session Resource already uses this Memory Store or mount_path")
			}
			if err != nil {
				return err
			}
			continue
		}
		file := item.File
		if resource.Type() != domain.SessionResourceTypeFile ||
			resource.SessionID != sessionID || resource.FileID != file.ID ||
			resource.State != domain.SessionResourceActive ||
			file.Scope == nil || file.Scope.ID != sessionID || file.Scope.Type != "session" {
			return errors.New("pg: invalid prepared Session Resource ownership")
		}
		fileTag, err := tx.Exec(ctx, `
UPDATE files
SET size_bytes = $2,
    checksum_sha256 = $3,
    state = 'ready',
    updated_at = $4
WHERE id = $1 AND workspace_id = $5 AND state = 'uploading'`,
			file.ID,
			item.Blob.SizeBytes,
			item.Blob.ChecksumSHA256,
			resource.UpdatedAt.UTC(),
			workspaceID,
		)
		if err != nil {
			return err
		}
		if fileTag.RowsAffected() != 1 {
			return domain.Conflict("prepared session File is no longer pending")
		}
		// Detach remains a recovery path if an older deployment or operator
		// already removed the scoped File row. The tombstone still carries the
		// deterministic object key and mount identity needed for cleanup.
		_, err = tx.Exec(ctx, `
INSERT INTO session_resources (
    id, session_id, resource_type, source_file_id, file_id, mount_path,
    state, created_at, updated_at
) VALUES ($1, $2, 'file', $3, $4, $5, 'active', $6, $7)`,
			resource.ID,
			resource.SessionID,
			resource.SourceFileID,
			resource.FileID,
			resource.MountPath,
			resource.CreatedAt.UTC(),
			resource.UpdatedAt.UTC(),
		)
		if isUniqueViolation(err) {
			return domain.Conflict("a Session Resource already uses this mount_path")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddSessionResource(
	ctx context.Context,
	prepared app.PreparedSessionResource,
	maxResources int,
	maxBytes int64,
) (domain.SessionResource, error) {
	resource := prepared.Resource
	if resource.Type() != domain.SessionResourceTypeFile {
		return domain.SessionResource{}, domain.Unsupported(
			"Memory Stores can be attached only while creating a Session",
		)
	}
	resource.CreatedAt = resource.CreatedAt.UTC().Truncate(time.Microsecond)
	resource.UpdatedAt = resource.UpdatedAt.UTC().Truncate(time.Microsecond)
	prepared.Resource = resource
	err := s.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, resource.SessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		if row.DeletingAt.Valid {
			return domain.Conflict("session deletion is in progress")
		}
		session, err := sessionFromLockRow(row)
		if err != nil {
			return err
		}
		workspaceID, scoped, err := s.workspaceForRead(ctx)
		if err != nil {
			return err
		}
		if scoped && session.WorkspaceID != workspaceID {
			return domain.NotFound("session not found")
		}
		if session.ArchivedAt != nil {
			return domain.Conflict("cannot add a resource to an archived session")
		}
		if session.Status == domain.StatusTerminated {
			return domain.Conflict("cannot add a resource to a terminated session")
		}
		rows, err := tx.Query(ctx, `
SELECT mount_path FROM session_resources
WHERE session_id = $1 AND state = 'active'`, resource.SessionID)
		if err != nil {
			return err
		}
		mountPaths := make([]string, 0, maxResources)
		for rows.Next() {
			var mountPath string
			if err := rows.Scan(&mountPath); err != nil {
				rows.Close()
				return err
			}
			mountPaths = append(mountPaths, mountPath)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return rowsErr
		}
		if len(mountPaths) >= maxResources {
			return domain.Conflict("session already has 500 resources")
		}
		var activeBytes int64
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(SUM(f.size_bytes), 0)
FROM session_resources AS sr
JOIN files AS f ON f.id = sr.file_id
WHERE sr.session_id = $1 AND sr.state = 'active'`, resource.SessionID).Scan(&activeBytes); err != nil {
			return err
		}
		if prepared.Blob.SizeBytes > maxBytes-activeBytes {
			return domain.TooLarge(
				"Session File Resources exceed the 500 MB aggregate limit",
			)
		}
		for _, mountPath := range mountPaths {
			if domain.SessionFileMountPathsConflict(mountPath, resource.MountPath) {
				return domain.Conflict(
					"session resource mount_path values cannot overlap",
				)
			}
		}
		if err := insertPreparedSessionResources(
			ctx, tx, session.WorkspaceID, resource.SessionID, []app.PreparedSessionResource{prepared},
		); err != nil {
			return err
		}
		session.Resources = append(session.Resources, resource)
		session.UpdatedAt = s.clock.Now().UTC()
		return s.updateAPIProjection(ctx, q, session)
	})
	if err != nil {
		return domain.SessionResource{}, err
	}
	s.notifySession(ctx, resource.SessionID)
	return resource, nil
}

func (s *Store) ActiveSessionResourceBytes(
	ctx context.Context,
	sessionID string,
) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(f.size_bytes), 0)
FROM session_resources AS sr
JOIN files AS f ON f.id = sr.file_id
WHERE sr.session_id = $1 AND sr.state = 'active'`, sessionID).Scan(&total)
	return total, err
}

func (s *Store) GetSessionResource(
	ctx context.Context,
	sessionID string,
	resourceID string,
) (domain.SessionResource, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, session_id, resource_type, source_file_id, file_id,
       memory_store_id, memory_access, memory_instructions,
       memory_store_name, memory_store_description, mount_path, state,
       created_at, updated_at
FROM session_resources
WHERE session_id = $1 AND id = $2 AND state = 'active'`, sessionID, resourceID)
	resource, err := scanSessionResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionResource{}, domain.NotFound("session resource not found")
	}
	return resource, err
}

func (s *Store) ListSessionResources(
	ctx context.Context,
	sessionID string,
	query app.SessionResourceListQuery,
) (app.SessionResourceListPage, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return app.SessionResourceListPage{}, err
	}
	args := []any{sessionID}
	statement := `
SELECT id, session_id, resource_type, source_file_id, file_id,
       memory_store_id, memory_access, memory_instructions,
       memory_store_name, memory_store_description, mount_path, state,
       created_at, updated_at
FROM session_resources
WHERE session_id = $1 AND state = 'active'`
	if query.Boundary != nil {
		args = append(args, query.Boundary.CreatedAt.UTC(), query.Boundary.ID)
		statement += fmt.Sprintf(
			" AND (created_at, id) > ($%d, $%d)", len(args)-1, len(args),
		)
	}
	args = append(args, query.Limit+1)
	statement += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return app.SessionResourceListPage{}, err
	}
	defer rows.Close()
	resources := make([]domain.SessionResource, 0, query.Limit+1)
	for rows.Next() {
		resource, scanErr := scanSessionResource(rows)
		if scanErr != nil {
			return app.SessionResourceListPage{}, scanErr
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return app.SessionResourceListPage{}, err
	}
	page := app.SessionResourceListPage{Resources: resources}
	if len(resources) > query.Limit {
		page.Resources = resources[:query.Limit]
		page.HasMore = true
	}
	return page, nil
}

func (s *Store) BeginSessionResourceDeletion(
	ctx context.Context,
	sessionID string,
	resourceID string,
) (domain.SessionResource, error) {
	var deleted domain.SessionResource
	err := s.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		if row.DeletingAt.Valid {
			return domain.Conflict("session deletion is in progress")
		}
		session, err := sessionFromLockRow(row)
		if err != nil {
			return err
		}
		resourceRow := tx.QueryRow(ctx, `
SELECT resource.id, resource.session_id, resource.resource_type, resource.source_file_id, resource.file_id,
       memory_store_id, memory_access, memory_instructions,
       memory_store_name, memory_store_description, mount_path, resource.state,
       resource.created_at, resource.updated_at, file.blob_key
FROM session_resources AS resource
LEFT JOIN files AS file ON file.id = resource.file_id
WHERE resource.session_id = $1 AND resource.id = $2 AND resource.state = 'active'
FOR UPDATE OF resource`, sessionID, resourceID)
		deleted, err = scanSessionResourceWithBlob(resourceRow)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session resource not found")
		}
		if err != nil {
			return err
		}
		if deleted.Type() == domain.SessionResourceTypeMemoryStore {
			return domain.Unsupported(
				"Memory Stores cannot be detached from a running Session",
			)
		}
		now := s.clock.Now().UTC()
		if _, err := tx.Exec(ctx, `
UPDATE session_resources SET state = 'deleting', updated_at = $3
WHERE session_id = $1 AND id = $2`, sessionID, resourceID, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
		UPDATE files SET state = 'deleting', updated_at = $2
		WHERE id = $1 AND workspace_id = $3 AND state = 'ready'`,
			deleted.FileID, now, session.WorkspaceID)
		if err != nil {
			return err
		}
		visible := make([]domain.SessionResource, 0, len(session.Resources))
		for _, resource := range session.Resources {
			if resource.ID != resourceID {
				visible = append(visible, resource)
			}
		}
		session.Resources = visible
		session.UpdatedAt = now
		return s.updateAPIProjection(ctx, q, session)
	})
	if err != nil {
		return domain.SessionResource{}, err
	}
	s.notifySession(ctx, sessionID)
	return deleted, nil
}

// SessionResourcesForReconcile returns both desired mounts and deletion
// tombstones. The worker reconciles this set after every sandbox acquisition.
func (s *Store) SessionResourcesForReconcile(
	ctx context.Context,
	sessionID string,
) ([]domain.SessionResource, error) {
	rows, err := s.pool.Query(ctx, `
	SELECT resource.id, resource.session_id, resource.resource_type,
	       resource.source_file_id, resource.file_id,
       memory_store_id, memory_access, memory_instructions,
       memory_store_name, memory_store_description, mount_path, resource.state,
	       resource.created_at, resource.updated_at, file.blob_key
	FROM session_resources AS resource
	LEFT JOIN files AS file ON file.id = resource.file_id
	WHERE resource.session_id = $1
	ORDER BY CASE resource.state WHEN 'deleting' THEN 0 ELSE 1 END,
	         resource.created_at, resource.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resources := make([]domain.SessionResource, 0)
	for rows.Next() {
		resource, scanErr := scanSessionResourceWithBlob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

func (s *Store) FinalizeSessionResourceDeletion(
	ctx context.Context,
	sessionID string,
	resourceID string,
) error {
	_, err := s.pool.Exec(ctx, `
DELETE FROM session_resources
WHERE session_id = $1 AND id = $2 AND state = 'deleting'`, sessionID, resourceID)
	return err
}

func (s *Store) FinalizeSessionMemoryResources(
	ctx context.Context,
	sessionID string,
) error {
	_, err := s.pool.Exec(ctx, `
DELETE FROM session_resources
WHERE session_id = $1 AND resource_type = 'memory_store' AND state = 'deleting'`,
		sessionID,
	)
	return err
}

func scanSessionResource(row interface{ Scan(...any) error }) (domain.SessionResource, error) {
	return scanSessionResourceFields(row, false)
}

func scanSessionResourceWithBlob(row interface{ Scan(...any) error }) (domain.SessionResource, error) {
	return scanSessionResourceFields(row, true)
}

func scanSessionResourceFields(
	row interface{ Scan(...any) error },
	includeBlob bool,
) (domain.SessionResource, error) {
	var resource domain.SessionResource
	var sourceFileID, fileID, memoryStoreID, memoryAccess, memoryInstructions *string
	var memoryStoreName, memoryStoreDescription *string
	var blobKey *string
	destinations := []any{
		&resource.ID,
		&resource.SessionID,
		&resource.ResourceType,
		&sourceFileID,
		&fileID,
		&memoryStoreID,
		&memoryAccess,
		&memoryInstructions,
		&memoryStoreName,
		&memoryStoreDescription,
		&resource.MountPath,
		&resource.State,
		&resource.CreatedAt,
		&resource.UpdatedAt,
	}
	if includeBlob {
		destinations = append(destinations, &blobKey)
	}
	err := row.Scan(destinations...)
	if sourceFileID != nil {
		resource.SourceFileID = *sourceFileID
	}
	if fileID != nil {
		resource.FileID = *fileID
	}
	if memoryStoreID != nil {
		resource.MemoryStoreID = *memoryStoreID
	}
	if memoryAccess != nil {
		resource.MemoryAccess = *memoryAccess
	}
	if memoryInstructions != nil {
		resource.MemoryInstructions = *memoryInstructions
	}
	if memoryStoreName != nil {
		resource.MemoryStoreName = *memoryStoreName
	}
	if memoryStoreDescription != nil {
		resource.MemoryStoreDescription = *memoryStoreDescription
	}
	if blobKey != nil {
		resource.BlobKey = *blobKey
	}
	resource.CreatedAt = resource.CreatedAt.UTC()
	resource.UpdatedAt = resource.UpdatedAt.UTC()
	return resource, err
}
