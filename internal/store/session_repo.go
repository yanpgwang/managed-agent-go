package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type SessionRepo struct{ db *DB }

func NewSessionRepo(db *DB) *SessionRepo { return &SessionRepo{db} }

// sessionCreatedKeySQL normalizes the created_at column to a fixed-width
// nanosecond representation for correct ordering and boundary comparisons.
var sessionCreatedKeySQL = nanoKeySQL("created_at")

const sessionCreatedKeyFormat = "2006-01-02T15:04:05.000000000Z"

type SessionListBoundary struct {
	CreatedAt time.Time
	ID        string
	// Backward asks for the page immediately before this boundary in the
	// caller's requested order. False asks for the page immediately after it.
	Backward bool
}

type SessionListQuery struct {
	AgentID         string
	AgentVersion    *int
	CreatedAtGt     *time.Time
	CreatedAtGte    *time.Time
	CreatedAtLt     *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	Statuses        []domain.Status
	// Sessions do not yet persist deployments or memory-store resources.
	// These flags make an explicit filter match no rows instead of being
	// silently ignored.
	HasDeploymentFilter  bool
	HasMemoryStoreFilter bool
	Boundary             *SessionListBoundary
	Limit                int
	Desc                 bool
}

type SessionListResult struct {
	Sessions []domain.Session
	// HasBefore and HasAfter are relative to the requested display order.
	HasBefore bool
	HasAfter  bool
}

func (r *SessionRepo) Put(ctx context.Context, s domain.Session) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, agent_version, environment_id, status, body, created_at, updated_at, archived_at)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET status=excluded.status, body=excluded.body,
		   updated_at=excluded.updated_at, archived_at=excluded.archived_at`,
		s.ID, s.AgentID, s.AgentVersion, s.EnvironmentID, string(s.Status), string(body),
		timeVal(s.CreatedAt), timeVal(s.UpdatedAt), nullableTime(s.ArchivedAt))
	return err
}

// CreateIfDependenciesActive inserts a new session only while its exact agent
// version and environment still exist and remain unarchived. Keeping both
// dependency checks in the INSERT makes session creation linearizable with
// concurrent agent/environment archival and environment deletion.
func (r *SessionRepo) CreateIfDependenciesActive(ctx context.Context, s domain.Session) error {
	return insertSessionIfDependenciesActive(ctx, r.db, s)
}

type sessionExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertSessionIfDependenciesActive(
	ctx context.Context,
	exec sessionExecer,
	s domain.Session,
) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	result, err := exec.ExecContext(ctx, `
INSERT INTO sessions
  (id, agent_id, agent_version, environment_id, status, body, created_at, updated_at, archived_at)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS (
  SELECT 1
  FROM agents
  WHERE id = ? AND version = ? AND archived_at IS NULL
)
AND EXISTS (
  SELECT 1
  FROM environments
  WHERE id = ? AND archived_at IS NULL
)`,
		s.ID, s.AgentID, s.AgentVersion, s.EnvironmentID, string(s.Status), string(body),
		timeVal(s.CreatedAt), timeVal(s.UpdatedAt), nullableTime(s.ArchivedAt),
		s.AgentID, s.AgentVersion, s.EnvironmentID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.Validation("agent or environment is missing or archived")
	}
	return nil
}

func (r *SessionRepo) Get(ctx context.Context, id string) (domain.Session, error) {
	var body string
	err := r.db.QueryRowContext(ctx, `SELECT body FROM sessions WHERE id=?`, id).Scan(&body)
	if err == sql.ErrNoRows {
		return domain.Session{}, domain.NotFound("session not found")
	}
	if err != nil {
		return domain.Session{}, err
	}
	var s domain.Session
	return s, json.Unmarshal([]byte(body), &s)
}

func (r *SessionRepo) List(ctx context.Context, query SessionListQuery) (SessionListResult, error) {
	if query.Limit <= 0 {
		query.Limit = 100
	}

	clauses, baseArgs := sessionListClauses(query)
	pageClauses := append([]string(nil), clauses...)
	pageArgs := append([]any(nil), baseArgs...)

	displayOrder := "ASC"
	if query.Desc {
		displayOrder = "DESC"
	}
	fetchOrder := displayOrder
	if query.Boundary != nil {
		relation := sessionRelationAfter
		if query.Boundary.Backward {
			relation = sessionRelationBefore
			fetchOrder = oppositeOrder(displayOrder)
		}
		predicate, args := sessionKeyPredicate(relation, query.Desc, *query.Boundary)
		pageClauses = append(pageClauses, predicate)
		pageArgs = append(pageArgs, args...)
	}

	statement := `SELECT body FROM sessions`
	if len(pageClauses) > 0 {
		statement += ` WHERE ` + strings.Join(pageClauses, ` AND `)
	}
	statement += ` ORDER BY ` + sessionCreatedKeySQL + ` ` + fetchOrder + `, id ` + fetchOrder + ` LIMIT ?`
	pageArgs = append(pageArgs, query.Limit)

	rows, err := r.db.QueryContext(ctx, statement, pageArgs...)
	if err != nil {
		return SessionListResult{}, err
	}
	sessions, err := scanSessions(rows)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return SessionListResult{}, err
	}
	if query.Boundary != nil && query.Boundary.Backward {
		reverseSessions(sessions)
	}

	result := SessionListResult{Sessions: sessions}
	if len(sessions) == 0 {
		return result, nil
	}
	result.HasBefore, err = r.sessionExistsRelative(
		ctx, clauses, baseArgs, sessionRelationBefore, query.Desc, sessions[0],
	)
	if err != nil {
		return SessionListResult{}, err
	}
	result.HasAfter, err = r.sessionExistsRelative(
		ctx, clauses, baseArgs, sessionRelationAfter, query.Desc, sessions[len(sessions)-1],
	)
	if err != nil {
		return SessionListResult{}, err
	}
	return result, nil
}

type sessionRelation int

const (
	sessionRelationBefore sessionRelation = iota
	sessionRelationAfter
)

func sessionListClauses(query SessionListQuery) ([]string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 12)
	if !query.IncludeArchived {
		clauses = append(clauses, `archived_at IS NULL`)
	}
	if query.AgentID != "" {
		clauses = append(clauses, `agent_id = ?`)
		args = append(args, query.AgentID)
	}
	if query.AgentVersion != nil {
		clauses = append(clauses, `agent_version = ?`)
		args = append(args, *query.AgentVersion)
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
			clauses = append(clauses, sessionCreatedKeySQL+` `+bound.op+` ?`)
			args = append(args, sessionCreatedKey(*bound.value))
		}
	}
	if len(query.Statuses) > 0 {
		placeholders := make([]string, len(query.Statuses))
		for i, status := range query.Statuses {
			placeholders[i] = `?`
			args = append(args, string(status))
		}
		clauses = append(clauses, `status IN (`+strings.Join(placeholders, `,`)+`)`)
	}
	if query.HasDeploymentFilter || query.HasMemoryStoreFilter {
		clauses = append(clauses, `0 = 1`)
	}
	return clauses, args
}

func sessionKeyPredicate(
	relation sessionRelation,
	desc bool,
	boundary SessionListBoundary,
) (string, []any) {
	cmp := `>`
	if relation == sessionRelationBefore {
		cmp = `<`
	}
	if desc {
		if cmp == `>` {
			cmp = `<`
		} else {
			cmp = `>`
		}
	}
	return `(` + sessionCreatedKeySQL + ` ` + cmp + ` ? OR (` +
			sessionCreatedKeySQL + ` = ? AND id ` + cmp + ` ?))`,
		[]any{sessionCreatedKey(boundary.CreatedAt), sessionCreatedKey(boundary.CreatedAt), boundary.ID}
}

func sessionCreatedKey(value time.Time) string {
	return value.UTC().Format(sessionCreatedKeyFormat)
}

func (r *SessionRepo) sessionExistsRelative(
	ctx context.Context,
	clauses []string,
	args []any,
	relation sessionRelation,
	desc bool,
	session domain.Session,
) (bool, error) {
	predicate, boundaryArgs := sessionKeyPredicate(relation, desc, SessionListBoundary{
		CreatedAt: session.CreatedAt,
		ID:        session.ID,
	})
	allClauses := append(append([]string(nil), clauses...), predicate)
	allArgs := append(append([]any(nil), args...), boundaryArgs...)
	statement := `SELECT EXISTS(SELECT 1 FROM sessions WHERE ` +
		strings.Join(allClauses, ` AND `) + ` LIMIT 1)`
	var exists bool
	if err := r.db.QueryRowContext(ctx, statement, allArgs...).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func oppositeOrder(order string) string {
	if order == "ASC" {
		return "DESC"
	}
	return "ASC"
}

func reverseSessions(sessions []domain.Session) {
	for left, right := 0, len(sessions)-1; left < right; left, right = left+1, right-1 {
		sessions[left], sessions[right] = sessions[right], sessions[left]
	}
}

func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_runs WHERE session_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE session_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func scanSessions(rows *sql.Rows) ([]domain.Session, error) {
	var out []domain.Session
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var s domain.Session
		if err := json.Unmarshal([]byte(body), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
