package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// ListSessions applies the public session filters and bidirectional keyset
// pagination directly to the PostgreSQL projection.
func (s *Store) ListSessions(
	ctx context.Context,
	query app.ListPage,
) (app.SessionListPage, error) {
	if query.Limit <= 0 {
		query.Limit = 100
	}
	clauses, args := sessionListClauses(query)
	pageClauses := append([]string(nil), clauses...)
	pageArgs := append([]any(nil), args...)

	displayOrder := "ASC"
	if query.Desc {
		displayOrder = "DESC"
	}
	fetchOrder := displayOrder
	if query.Boundary != nil {
		relationAfter := !query.Boundary.Backward
		if query.Boundary.Backward {
			fetchOrder = oppositeSQLOrder(displayOrder)
		}
		predicate, values := sessionBoundaryPredicate(
			relationAfter,
			query.Desc,
			query.Boundary.CreatedAt,
			query.Boundary.ID,
			len(pageArgs)+1,
		)
		pageClauses = append(pageClauses, predicate)
		pageArgs = append(pageArgs, values...)
	}

	statement := `SELECT body, created_at FROM sessions`
	if len(pageClauses) > 0 {
		statement += ` WHERE ` + strings.Join(pageClauses, ` AND `)
	}
	pageArgs = append(pageArgs, query.Limit)
	statement += fmt.Sprintf(
		` ORDER BY created_at %s, id %s LIMIT $%d`,
		fetchOrder,
		fetchOrder,
		len(pageArgs),
	)
	rows, err := s.pool.Query(ctx, statement, pageArgs...)
	if err != nil {
		return app.SessionListPage{}, err
	}
	sessions, err := scanSessionBodies(rows)
	rows.Close()
	if err != nil {
		return app.SessionListPage{}, err
	}
	if query.Boundary != nil && query.Boundary.Backward {
		reverseDomainSessions(sessions)
	}

	result := app.SessionListPage{Sessions: sessions}
	if len(sessions) == 0 {
		return result, nil
	}
	result.HasPrev, err = s.sessionExistsRelative(
		ctx, clauses, args, false, query.Desc, sessions[0],
	)
	if err != nil {
		return app.SessionListPage{}, err
	}
	result.HasNext, err = s.sessionExistsRelative(
		ctx, clauses, args, true, query.Desc, sessions[len(sessions)-1],
	)
	if err != nil {
		return app.SessionListPage{}, err
	}
	return result, nil
}

func sessionListClauses(query app.ListPage) ([]string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if !query.IncludeArchived {
		clauses = append(clauses, `archived_at IS NULL`)
	}
	if query.AgentID != "" {
		add(`agent_id = $%d`, query.AgentID)
	}
	if query.AgentVersion != nil {
		add(`agent_version = $%d`, *query.AgentVersion)
	}
	for _, bound := range []struct {
		value *time.Time
		op    string
	}{
		{query.CreatedAtGt, `>`},
		{query.CreatedAtGte, `>=`},
		{query.CreatedAtLt, `<`},
		{query.CreatedAtLte, `<=`},
	} {
		if bound.value != nil {
			add(`created_at `+bound.op+` $%d`, bound.value)
		}
	}
	if len(query.Statuses) > 0 {
		statuses := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			statuses[i] = string(status)
		}
		add(`status = ANY($%d::text[])`, statuses)
	}
	if query.DeploymentID != nil || query.MemoryStoreID != nil {
		clauses = append(clauses, `FALSE`)
	}
	return clauses, args
}

func sessionBoundaryPredicate(
	relationAfter bool,
	desc bool,
	createdAt any,
	id string,
	firstPlaceholder int,
) (string, []any) {
	operator := ">"
	if !relationAfter {
		operator = "<"
	}
	if desc {
		if operator == ">" {
			operator = "<"
		} else {
			operator = ">"
		}
	}
	return fmt.Sprintf(
		`(created_at %s $%d OR (created_at = $%d AND id %s $%d))`,
		operator,
		firstPlaceholder,
		firstPlaceholder,
		operator,
		firstPlaceholder+1,
	), []any{createdAt, id}
}

func (s *Store) sessionExistsRelative(
	ctx context.Context,
	clauses []string,
	args []any,
	relationAfter bool,
	desc bool,
	session domain.Session,
) (bool, error) {
	predicate, values := sessionBoundaryPredicate(
		relationAfter, desc, session.CreatedAt, session.ID, len(args)+1,
	)
	allClauses := append(append([]string(nil), clauses...), predicate)
	allArgs := append(append([]any(nil), args...), values...)
	statement := `SELECT EXISTS(SELECT 1 FROM sessions WHERE ` +
		strings.Join(allClauses, ` AND `) + `)`
	var exists bool
	err := s.pool.QueryRow(ctx, statement, allArgs...).Scan(&exists)
	return exists, err
}

func scanSessionBodies(rows pgx.Rows) ([]domain.Session, error) {
	var sessions []domain.Session
	for rows.Next() {
		var body []byte
		var createdAt time.Time
		if err := rows.Scan(&body, &createdAt); err != nil {
			return nil, err
		}
		var session domain.Session
		if err := json.Unmarshal(body, &session); err != nil {
			return nil, fmt.Errorf("pg: decode session projection: %w", err)
		}
		// The relational timestamp is the pagination key and PostgreSQL's exact
		// microsecond value. Return that same value in the cursor-bearing object,
		// including for rows written before create-time normalization existed.
		session.CreatedAt = createdAt.UTC()
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func reverseDomainSessions(sessions []domain.Session) {
	for left, right := 0, len(sessions)-1; left < right; left, right = left+1, right-1 {
		sessions[left], sessions[right] = sessions[right], sessions[left]
	}
}

func oppositeSQLOrder(order string) string {
	if order == "ASC" {
		return "DESC"
	}
	return "ASC"
}

// UpdateSessionTitle keeps the projection and session.updated event in one
// transaction under the per-session admission lock.
func (s *Store) UpdateSessionTitle(
	ctx context.Context,
	sessionID string,
	title string,
) (domain.Session, error) {
	var result domain.Session
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
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
		if session.Title == title {
			result = session
			return nil
		}
		session.Title = title
		session.UpdatedAt = s.clock.Now().UTC()
		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		if _, _, err := s.appendDrafts(ctx, q, sessionID, []domain.EventDraft{{
			Type: domain.EvSessionUpdated,
			Payload: map[string]any{
				"title": title,
			},
		}}, maxSeq, nil); err != nil {
			return err
		}
		if err := s.updateAPIProjection(ctx, q, session); err != nil {
			return err
		}
		result = session
		return nil
	})
	if err == nil {
		s.notifySession(ctx, sessionID)
	}
	return result, err
}

func (s *Store) ArchiveSession(ctx context.Context, sessionID string) (domain.Session, error) {
	var result domain.Session
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
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
		if session.Status == domain.StatusRunning {
			return domain.Conflict("cannot archive a running session; interrupt first")
		}
		if session.ArchivedAt == nil {
			now := s.clock.Now().UTC()
			session.ArchivedAt = &now
			session.UpdatedAt = now
			if err := s.updateAPIProjection(ctx, q, session); err != nil {
				return err
			}
		}
		result = session
		return nil
	})
	return result, err
}

// PrepareSessionDeletion closes admission before the control plane stops the
// Temporal Workflow and releases its sandbox. This prevents a concurrent
// user.message from turning the projection running during external cleanup.
func (s *Store) PrepareSessionDeletion(ctx context.Context, sessionID string) error {
	return s.withTx(ctx, func(q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		session, err := sessionFromLockRow(row)
		if err != nil {
			return err
		}
		if session.Status == domain.StatusRunning {
			return domain.Conflict("cannot delete a running session; interrupt first")
		}
		if row.DeletingAt.Valid {
			return nil
		}
		return q.MarkSessionDeleting(ctx, pgstore.MarkSessionDeletingParams{
			DeletingAt: tsUTC(s.clock.Now()),
			ID:         sessionID,
		})
	})
}

// FinalizeSessionDeletion physically deletes a previously fenced session.
func (s *Store) FinalizeSessionDeletion(ctx context.Context, sessionID string) error {
	affected, err := s.q.DeleteMarkedSession(ctx, sessionID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return domain.Conflict("session sandbox cleanup is incomplete")
		}
		return err
	}
	if affected != 1 {
		exists, existsErr := s.SessionExists(ctx, sessionID)
		if existsErr != nil {
			return existsErr
		}
		if exists {
			return domain.Conflict("session deletion was not prepared")
		}
		// A concurrent retry already completed the same prepared deletion.
		return nil
	}
	s.notifySession(ctx, sessionID)
	return nil
}

// ListDeletingSessionIDs returns fenced sessions in stable oldest-first order
// for worker-side lifecycle reconciliation.
func (s *Store) ListDeletingSessionIDs(
	ctx context.Context,
	limit int,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	return s.q.ListDeletingSessionIDs(ctx, int32(limit))
}

// DeleteSession is the storage-only convenience used by repository tests. The
// HTTP control plane uses the explicit prepare/terminate/finalize sequence.
func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	if err := s.PrepareSessionDeletion(ctx, sessionID); err != nil {
		return err
	}
	return s.FinalizeSessionDeletion(ctx, sessionID)
}

func (s *Store) updateAPIProjection(
	ctx context.Context,
	q *pgstore.Queries,
	session domain.Session,
) error {
	body, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return q.UpdateSessionProjection(ctx, pgstore.UpdateSessionProjectionParams{
		Status:     string(session.Status),
		Body:       body,
		UpdatedAt:  tsUTC(session.UpdatedAt),
		ArchivedAt: tsPtr(session.ArchivedAt),
		ID:         session.ID,
	})
}

// QueryEvents applies the public event filters to the durable ledger.
func (s *Store) QueryEvents(
	ctx context.Context,
	sessionID string,
	query app.EventQuery,
) ([]domain.Event, error) {
	if query.Limit <= 0 {
		query.Limit = 1000
	}
	clauses := []string{`session_id = $1`}
	args := []any{sessionID}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if query.AfterSeq > 0 {
		add(`seq > $%d`, query.AfterSeq)
	}
	if query.BeforeSeq > 0 {
		add(`seq < $%d`, query.BeforeSeq)
	}
	if len(query.Types) > 0 {
		add(`type = ANY($%d::text[])`, query.Types)
	}
	for _, bound := range []struct {
		value *time.Time
		op    string
	}{
		{query.CreatedAtGt, `>`},
		{query.CreatedAtGte, `>=`},
		{query.CreatedAtLt, `<`},
		{query.CreatedAtLte, `<=`},
	} {
		if bound.value != nil {
			add(`processed_at `+bound.op+` $%d`, bound.value)
		}
	}
	order := "ASC"
	if query.Desc {
		order = "DESC"
	}
	args = append(args, query.Limit)
	statement := `SELECT id, session_id, seq, type, payload, turn_event_id, created_at, processed_at
FROM events
WHERE ` + strings.Join(clauses, ` AND `) +
		fmt.Sprintf(` ORDER BY seq %s LIMIT $%d`, order, len(args))
	rows, err := s.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Event
	for rows.Next() {
		var row pgstore.Event
		if err := rows.Scan(
			&row.ID,
			&row.SessionID,
			&row.Seq,
			&row.Type,
			&row.Payload,
			&row.TurnEventID,
			&row.CreatedAt,
			&row.ProcessedAt,
		); err != nil {
			return nil, err
		}
		event, err := eventFromRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) LatestEventSequence(ctx context.Context, sessionID string) (int64, error) {
	var sequence int64
	err := s.pool.QueryRow(
		ctx,
		`SELECT COALESCE(MAX(seq), 0)::bigint FROM events WHERE session_id = $1`,
		sessionID,
	).Scan(&sequence)
	return sequence, err
}

// SessionExists reports whether the projection remains present. It lets the
// polling stream distinguish a quiet session from a concurrently deleted one.
func (s *Store) SessionExists(ctx context.Context, sessionID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1)`,
		sessionID,
	).Scan(&exists)
	return exists, err
}
