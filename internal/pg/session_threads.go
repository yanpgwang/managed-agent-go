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
)

// GetSessionThread returns a thread only when both path identifiers name the
// same resource. Today every thread is the primary thread and therefore reads
// its mutable execution projection directly from the parent Session.
func (s *Store) GetSessionThread(
	ctx context.Context,
	sessionID string,
	threadID string,
) (domain.SessionThread, error) {
	var (
		body          []byte
		storedCreated time.Time
		kind          string
		parentID      *string
	)
	err := s.pool.QueryRow(ctx, `
SELECT session.body, thread.created_at, thread.kind, thread.parent_thread_id
FROM session_threads AS thread
JOIN sessions AS session ON session.id = thread.session_id
WHERE thread.session_id = $1 AND thread.id = $2`,
		sessionID, threadID,
	).Scan(&body, &storedCreated, &kind, &parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionThread{}, domain.NotFound("session thread not found")
	}
	if err != nil {
		return domain.SessionThread{}, err
	}
	if kind != "primary" {
		return domain.SessionThread{}, domain.Unsupported(
			"child session-thread runtime is not implemented",
		)
	}
	return primaryThreadFromBody(threadID, storedCreated, parentID, body)
}

// ListSessionThreads returns threads in the documented order: primary first,
// then children in spawn order. The current runtime creates exactly one primary
// row, but the keyset and ordering contract is already safe for later children.
func (s *Store) ListSessionThreads(
	ctx context.Context,
	sessionID string,
	query app.SessionThreadListQuery,
) ([]domain.SessionThread, error) {
	if query.Limit <= 0 {
		query.Limit = app.DefaultSessionThreadListLimit
	}
	var sessionBody []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT body FROM sessions WHERE id = $1`, sessionID,
	).Scan(&sessionBody); errors.Is(err, pgx.ErrNoRows) {
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
SELECT thread.id, thread.created_at, thread.kind, thread.parent_thread_id
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
			id, kind  string
			createdAt time.Time
			parentID  *string
		)
		if err := rows.Scan(&id, &createdAt, &kind, &parentID); err != nil {
			return nil, err
		}
		if kind != "primary" {
			return nil, domain.Unsupported("child session-thread runtime is not implemented")
		}
		thread, err := primaryThreadFromBody(id, createdAt, parentID, sessionBody)
		if err != nil {
			return nil, err
		}
		result = append(result, thread)
	}
	return result, rows.Err()
}

func primaryThreadFromBody(
	threadID string,
	createdAt time.Time,
	parentID *string,
	body []byte,
) (domain.SessionThread, error) {
	var session domain.Session
	if err := json.Unmarshal(body, &session); err != nil {
		return domain.SessionThread{}, fmt.Errorf("pg: decode session thread projection: %w", err)
	}
	archivedAt := session.ArchivedAt
	status := session.Status
	terminatedAt := session.TerminatedAt
	updatedAt := session.UpdatedAt
	if archivedAt != nil {
		status = domain.StatusTerminated
		terminatedAt = archivedAt
		if updatedAt.Before(*archivedAt) {
			updatedAt = *archivedAt
		}
	}
	return domain.SessionThread{
		ID: threadID, SessionID: session.ID, ParentThreadID: parentID,
		Agent: session.AgentSnapshot, Status: status, Usage: session.Usage,
		ActiveSeconds: session.ActiveSeconds, RunningSince: session.RunningSince,
		TerminatedAt: terminatedAt, CreatedAt: createdAt.UTC(),
		UpdatedAt: updatedAt.UTC(), ArchivedAt: archivedAt,
	}, nil
}
