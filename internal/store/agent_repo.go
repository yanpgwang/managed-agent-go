package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type AgentRepo struct{ db *DB }

func NewAgentRepo(db *DB) *AgentRepo { return &AgentRepo{db} }

func (r *AgentRepo) PutVersion(ctx context.Context, a domain.Agent) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO agents (id, version, name, body, created_at, updated_at, archived_at)
		 VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.Version, a.Name, string(body),
		a.CreatedAt.Format(rfc3339), a.UpdatedAt.Format(rfc3339), nullableTime(a.ArchivedAt))
	return err
}

// UpdateVersion atomically reads the current resource state, applies mutate,
// and conditionally appends one configuration version. The conditional insert
// protects against both a concurrent version append and a concurrent archive,
// including callers using another database connection.
func (r *AgentRepo) UpdateVersion(
	ctx context.Context,
	id string,
	mutate func(domain.Agent) (domain.Agent, bool, error),
) (domain.Agent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer tx.Rollback()

	current, err := latestAgent(ctx, tx, id)
	if err == sql.ErrNoRows {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	if err != nil {
		return domain.Agent{}, err
	}

	next, changed, err := mutate(current)
	if err != nil {
		return domain.Agent{}, err
	}
	if !changed {
		if err := tx.Commit(); err != nil {
			return domain.Agent{}, err
		}
		return current, nil
	}

	// Identity, lifecycle state, and version allocation belong to the
	// repository, not to a caller-provided mutation.
	next.ID = current.ID
	next.Version = current.Version + 1
	next.CreatedAt = current.CreatedAt
	next.ArchivedAt = nil
	body, err := json.Marshal(next)
	if err != nil {
		return domain.Agent{}, err
	}

	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO agents
  (id, version, name, body, created_at, updated_at, archived_at)
SELECT ?, ?, ?, ?, ?, ?, NULL
WHERE EXISTS (
  SELECT 1
  FROM agents AS current
  WHERE current.id = ?
    AND current.version = ?
    AND current.archived_at IS NULL
    AND current.version = (
      SELECT MAX(candidate.version)
      FROM agents AS candidate
      WHERE candidate.id = current.id
    )
)`,
		next.ID, next.Version, next.Name, string(body),
		next.CreatedAt.Format(rfc3339), next.UpdatedAt.Format(rfc3339),
		id, current.Version)
	if err != nil {
		if isAgentWriteRace(err) {
			return domain.Agent{}, domain.Conflict("agent version changed during update")
		}
		return domain.Agent{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Agent{}, err
	}
	if affected != 1 {
		return domain.Agent{}, domain.Conflict("agent version changed during update")
	}
	if err := tx.Commit(); err != nil {
		if isAgentWriteRace(err) {
			return domain.Agent{}, domain.Conflict("agent version changed during update")
		}
		return domain.Agent{}, err
	}
	return next, nil
}

// Archive records lifecycle state without creating a configuration version.
// Archival applies to the agent resource rather than one version, so the
// lifecycle timestamp is projected onto every stored version. This also
// prevents a caller from selecting a historical version to bypass archival.
func (r *AgentRepo) Archive(ctx context.Context, id string, archivedAt time.Time) (domain.Agent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, err
	}
	defer tx.Rollback()

	at := archivedAt.UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE agents
SET archived_at = COALESCE(
  (
    SELECT existing.archived_at
    FROM agents AS existing
    WHERE existing.id = ?
      AND existing.archived_at IS NOT NULL
    ORDER BY existing.version DESC
    LIMIT 1
  ),
  ?
)
WHERE id = ?`, id, at.Format(rfc3339), id); err != nil {
		return domain.Agent{}, err
	}
	latest, err := latestAgent(ctx, tx, id)
	if err == sql.ErrNoRows {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	if err != nil {
		return domain.Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, err
	}
	return latest, nil
}

func (r *AgentRepo) Latest(ctx context.Context, id string) (domain.Agent, error) {
	a, err := latestAgent(ctx, r.db, id)
	if err == sql.ErrNoRows {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	return a, err
}

// GetVersion returns a specific agent version. Configuration fields are
// immutable once written, so this yields a stable snapshot for session pinning;
// resource-level lifecycle metadata such as ArchivedAt may be projected later.
func (r *AgentRepo) GetVersion(ctx context.Context, id string, version int) (domain.Agent, error) {
	var body string
	var archivedAt sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT body, archived_at FROM agents WHERE id=? AND version=?`, id, version).
		Scan(&body, &archivedAt)
	if err == sql.ErrNoRows {
		return domain.Agent{}, domain.NotFound("agent version not found")
	}
	if err != nil {
		return domain.Agent{}, err
	}
	return decodeAgent(body, archivedAt)
}

func (r *AgentRepo) Versions(ctx context.Context, id string) ([]domain.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT body, archived_at FROM agents WHERE id=? ORDER BY version ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func (r *AgentRepo) List(ctx context.Context) ([]domain.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT body, archived_at FROM agents a WHERE version=(SELECT MAX(version) FROM agents b WHERE b.id=a.id)
		 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAgents(rows)
}

func scanAgents(rows *sql.Rows) ([]domain.Agent, error) {
	var out []domain.Agent
	for rows.Next() {
		var body string
		var archivedAt sql.NullString
		if err := rows.Scan(&body, &archivedAt); err != nil {
			return nil, err
		}
		a, err := decodeAgent(body, archivedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type agentQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func latestAgent(ctx context.Context, q agentQueryRower, id string) (domain.Agent, error) {
	var body string
	var archivedAt sql.NullString
	err := q.QueryRowContext(ctx,
		`SELECT body, archived_at FROM agents WHERE id=? ORDER BY version DESC LIMIT 1`, id).
		Scan(&body, &archivedAt)
	if err != nil {
		return domain.Agent{}, err
	}
	return decodeAgent(body, archivedAt)
}

func decodeAgent(body string, archivedAt sql.NullString) (domain.Agent, error) {
	var a domain.Agent
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		return domain.Agent{}, err
	}
	if !archivedAt.Valid {
		a.ArchivedAt = nil
		return a, nil
	}
	at, err := parseRFC3339(archivedAt.String)
	if err != nil {
		return domain.Agent{}, err
	}
	a.ArchivedAt = &at
	return a, nil
}

func isAgentWriteRace(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") ||
		strings.Contains(message, "busy") ||
		strings.Contains(message, "unique constraint")
}
