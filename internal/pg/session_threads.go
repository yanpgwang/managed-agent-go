package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// GetSessionThread returns a Thread only when both path identifiers name the
// same resource. The Thread row is the execution projection source of truth;
// the parent Session is not decoded on this read path.
func (s *Store) GetSessionThread(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.SessionThread, error) {
	var (
		id, storedSessionID string
		parentID            *string
		status              string
		body                []byte
		createdAt           time.Time
		updatedAt           time.Time
		archivedAt          *time.Time
	)
	err := s.pool.QueryRow(ctx, `
SELECT id, session_id, parent_thread_id, status, body,
       created_at, updated_at, archived_at
FROM session_threads
WHERE session_id = $1 AND id = $2`,
		sessionID, threadID,
	).Scan(
		&id, &storedSessionID, &parentID, &status, &body,
		&createdAt, &updatedAt, &archivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionThread{}, domain.NotFound("session thread not found")
	}
	if err != nil {
		return domain.SessionThread{}, err
	}
	return sessionThreadFromRow(
		id, storedSessionID, parentID, domain.Status(status), body,
		createdAt, updatedAt, archivedAt,
	)
}

// ListSessionThreads returns threads in the documented order: primary first,
// then children in spawn order. Each result is decoded from its own projection.
func (s *Store) ListSessionThreads(
	ctx context.Context,
	sessionID string,
	query app.SessionThreadListQuery,
) ([]domain.SessionThread, error) {
	if query.Limit <= 0 {
		query.Limit = app.DefaultSessionThreadListLimit
	}
	var exists int
	if err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM sessions WHERE id = $1`, sessionID,
	).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound("session not found")
	} else if err != nil {
		return nil, err
	}

	args := []any{sessionID}
	boundary := ""
	if query.Boundary != nil {
		args = append(args, query.Boundary.CreatedAt, query.Boundary.ID)
		boundary = fmt.Sprintf(
			` AND (thread.created_at > $%d OR (thread.created_at = $%d AND thread.id > $%d))`,
			len(args)-1, len(args)-1, len(args),
		)
	}
	args = append(args, query.Limit)
	rows, err := s.pool.Query(ctx, `
SELECT thread.id, thread.session_id, thread.parent_thread_id,
       thread.status, thread.body, thread.created_at,
       thread.updated_at, thread.archived_at
FROM session_threads AS thread
WHERE thread.session_id = $1`+boundary+`
ORDER BY CASE WHEN thread.kind = 'primary' THEN 0 ELSE 1 END,
         thread.created_at, thread.id
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.SessionThread, 0, query.Limit)
	for rows.Next() {
		var (
			id, storedSessionID string
			parentID            *string
			status              string
			body                []byte
			createdAt           time.Time
			updatedAt           time.Time
			archivedAt          *time.Time
		)
		if err := rows.Scan(
			&id, &storedSessionID, &parentID, &status, &body,
			&createdAt, &updatedAt, &archivedAt,
		); err != nil {
			return nil, err
		}
		thread, err := sessionThreadFromRow(
			id, storedSessionID, parentID, domain.Status(status), body,
			createdAt, updatedAt, archivedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, thread)
	}
	return result, rows.Err()
}

func sessionThreadFromRow(
	threadID string,
	sessionID string,
	parentID *string,
	status domain.Status,
	body []byte,
	createdAt time.Time,
	updatedAt time.Time,
	archivedAt *time.Time,
) (domain.SessionThread, error) {
	var thread domain.SessionThread
	if err := json.Unmarshal(body, &thread); err != nil {
		return domain.SessionThread{}, fmt.Errorf("pg: decode session thread projection: %w", err)
	}
	thread.ID = threadID
	thread.SessionID = sessionID
	thread.ParentThreadID = parentID
	thread.Status = status
	thread.CreatedAt = createdAt.UTC()
	thread.UpdatedAt = updatedAt.UTC()
	thread.ArchivedAt = utcTimePtr(archivedAt)
	return thread, nil
}

// putPrimarySessionThreadProjection synchronizes the current single-Thread
// execution into the independent primary projection. Callers already hold the
// Session row lock, which is the serialization fence for every Thread mutation
// in that Session.
func (s *Store) putPrimarySessionThreadProjection(
	ctx context.Context,
	q *pgstore.Queries,
	session domain.Session,
) error {
	body, err := q.GetPrimarySessionThreadProjection(ctx, session.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pg: primary session thread is missing for %s", session.ID)
	}
	if err != nil {
		return err
	}
	var thread domain.SessionThread
	if err := json.Unmarshal(body, &thread); err != nil {
		return fmt.Errorf("pg: decode primary session thread projection: %w", err)
	}
	thread.ApplyPrimarySessionProjection(session)
	body, err = json.Marshal(thread)
	if err != nil {
		return err
	}
	return q.UpdatePrimarySessionThreadProjection(
		ctx,
		pgstore.UpdatePrimarySessionThreadProjectionParams{
			Status: string(thread.Status), Body: body,
			UpdatedAt: tsUTC(thread.UpdatedAt), ArchivedAt: tsPtr(thread.ArchivedAt),
			SessionID: session.ID,
		},
	)
}
