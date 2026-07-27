package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type EnvironmentRepo struct{ db *DB }

func NewEnvironmentRepo(db *DB) *EnvironmentRepo { return &EnvironmentRepo{db} }

func (r *EnvironmentRepo) Put(ctx context.Context, e domain.Environment) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO environments (id, name, config_type, body, created_at, updated_at, archived_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, config_type=excluded.config_type,
		   body=excluded.body, updated_at=excluded.updated_at, archived_at=excluded.archived_at`,
		e.ID, e.Name, e.ConfigType, string(body),
		e.CreatedAt.Format(rfc3339), e.UpdatedAt.Format(rfc3339), nullableTime(e.ArchivedAt))
	return err
}

func (r *EnvironmentRepo) Get(ctx context.Context, id string) (domain.Environment, error) {
	var body string
	err := r.db.QueryRowContext(ctx, `SELECT body FROM environments WHERE id=?`, id).Scan(&body)
	if err == sql.ErrNoRows {
		return domain.Environment{}, domain.NotFound("environment not found")
	}
	if err != nil {
		return domain.Environment{}, err
	}
	var e domain.Environment
	return e, json.Unmarshal([]byte(body), &e)
}

func (r *EnvironmentRepo) List(ctx context.Context) ([]domain.Environment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT body FROM environments ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Environment
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e domain.Environment
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteIfUnreferenced removes an environment only if no session references it.
// The conditional DELETE and the missing-vs-referenced classification share one
// transaction, so a concurrent conditional session insert cannot leave an
// orphaned environment reference.
func (r *EnvironmentRepo) DeleteIfUnreferenced(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
DELETE FROM environments
WHERE id = ?
  AND NOT EXISTS (
    SELECT 1
    FROM sessions
    WHERE environment_id = ?
  )`, id, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return tx.Commit()
	}

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM environments WHERE id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return domain.NotFound("environment not found")
	}
	return domain.Conflict("environment is referenced by a session")
}
