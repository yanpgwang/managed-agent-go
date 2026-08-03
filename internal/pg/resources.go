package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

var (
	_ app.AgentRepository       = (*AgentRepository)(nil)
	_ app.EnvironmentRepository = (*EnvironmentRepository)(nil)
)

// AgentRepository stores append-only Agent configuration versions in
// PostgreSQL. Lifecycle state is projected from the relational archived_at
// column so archiving a resource applies consistently to every historical
// version without rewriting its immutable JSON configuration.
type AgentRepository struct {
	store *Store
}

func NewAgentRepository(store *Store) *AgentRepository {
	return &AgentRepository{store: store}
}

func (r *AgentRepository) PutVersion(ctx context.Context, agent domain.Agent) error {
	params, err := agentInsertParams(agent)
	if err != nil {
		return err
	}
	return r.store.q.InsertAgentVersion(ctx, params)
}

func (r *AgentRepository) UpdateVersion(
	ctx context.Context,
	id string,
	mutate func(domain.Agent) (domain.Agent, bool, error),
) (domain.Agent, error) {
	var result domain.Agent
	err := r.store.withTx(ctx, func(q *pgstore.Queries) error {
		row, err := q.LockLatestAgent(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("agent not found")
		}
		if err != nil {
			return err
		}
		current, err := agentFromRow(row)
		if err != nil {
			return err
		}
		next, changed, err := mutate(current)
		if err != nil {
			return err
		}
		if !changed {
			result = current
			return nil
		}

		next.ID = current.ID
		next.Version = current.Version + 1
		next.CreatedAt = current.CreatedAt
		next.ArchivedAt = nil
		params, err := agentInsertParams(next)
		if err != nil {
			return err
		}
		if err := q.InsertAgentVersion(ctx, params); err != nil {
			if isUniqueViolation(err) {
				return domain.Conflict("agent version changed during update")
			}
			return err
		}
		result = next
		return nil
	})
	return result, err
}

func (r *AgentRepository) Archive(
	ctx context.Context,
	id string,
	archivedAt time.Time,
) (domain.Agent, error) {
	var result domain.Agent
	err := r.store.withTx(ctx, func(q *pgstore.Queries) error {
		affected, err := q.ArchiveAgent(ctx, pgstore.ArchiveAgentParams{
			ArchivedAt: tsUTC(archivedAt),
			ID:         id,
		})
		if err != nil {
			return err
		}
		if affected == 0 {
			return domain.NotFound("agent not found")
		}
		row, err := q.GetLatestAgent(ctx, id)
		if err != nil {
			return err
		}
		result, err = agentFromRow(row)
		return err
	})
	return result, err
}

func (r *AgentRepository) Latest(ctx context.Context, id string) (domain.Agent, error) {
	row, err := r.store.q.GetLatestAgent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Agent{}, domain.NotFound("agent not found")
	}
	if err != nil {
		return domain.Agent{}, err
	}
	return agentFromRow(row)
}

func (r *AgentRepository) GetVersion(
	ctx context.Context,
	id string,
	version int,
) (domain.Agent, error) {
	row, err := r.store.q.GetAgentVersion(ctx, pgstore.GetAgentVersionParams{
		ID: id, Version: int32(version),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Agent{}, domain.NotFound("agent version not found")
	}
	if err != nil {
		return domain.Agent{}, err
	}
	return agentFromRow(row)
}

func (r *AgentRepository) Versions(
	ctx context.Context,
	id string,
	query app.AgentVersionListQuery,
) (app.AgentVersionListPage, error) {
	if query.Limit <= 0 {
		query.Limit = app.DefaultAgentListLimit
	}
	rows, err := r.store.q.ListAgentVersions(ctx, pgstore.ListAgentVersionsParams{
		ID: id, AfterVersion: int32(query.AfterVersion), RowLimit: int32(query.Limit + 1),
	})
	if err != nil {
		return app.AgentVersionListPage{}, err
	}
	versions, err := agentsFromRows(rows)
	if err != nil {
		return app.AgentVersionListPage{}, err
	}
	page := app.AgentVersionListPage{Versions: versions}
	if len(versions) > query.Limit {
		page.Versions = versions[:query.Limit]
		page.HasNext = true
	}
	return page, nil
}

func (r *AgentRepository) ListLatest(
	ctx context.Context,
	query app.AgentListQuery,
) (app.AgentListPage, error) {
	return r.store.ListAgents(ctx, query)
}

func agentInsertParams(agent domain.Agent) (pgstore.InsertAgentVersionParams, error) {
	body, err := json.Marshal(agent)
	if err != nil {
		return pgstore.InsertAgentVersionParams{}, err
	}
	return pgstore.InsertAgentVersionParams{
		ID:         agent.ID,
		Version:    int32(agent.Version),
		Name:       agent.Name,
		Body:       body,
		CreatedAt:  tsUTC(agent.CreatedAt),
		UpdatedAt:  tsUTC(agent.UpdatedAt),
		ArchivedAt: tsPtr(agent.ArchivedAt),
	}, nil
}

func agentFromRow(row pgstore.Agent) (domain.Agent, error) {
	var agent domain.Agent
	if err := json.Unmarshal(row.Body, &agent); err != nil {
		return domain.Agent{}, fmt.Errorf("pg: decode agent %s version %d: %w", row.ID, row.Version, err)
	}
	agent.ArchivedAt = timePtr(row.ArchivedAt)
	return agent, nil
}

func agentsFromRows(rows []pgstore.Agent) ([]domain.Agent, error) {
	out := make([]domain.Agent, 0, len(rows))
	for _, row := range rows {
		agent, err := agentFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	return out, nil
}

// EnvironmentRepository stores Environment resources and makes
// delete-if-unused one PostgreSQL statement so a concurrent Session creation
// cannot commit an orphaned reference.
type EnvironmentRepository struct {
	store *Store
}

func NewEnvironmentRepository(store *Store) *EnvironmentRepository {
	return &EnvironmentRepository{store: store}
}

func (r *EnvironmentRepository) Put(ctx context.Context, environment domain.Environment) error {
	body, err := json.Marshal(environment)
	if err != nil {
		return err
	}
	return r.store.q.UpsertEnvironment(ctx, pgstore.UpsertEnvironmentParams{
		ID:         environment.ID,
		Name:       environment.Name,
		ConfigType: environment.ConfigType,
		Body:       body,
		CreatedAt:  tsUTC(environment.CreatedAt),
		UpdatedAt:  tsUTC(environment.UpdatedAt),
		ArchivedAt: tsPtr(environment.ArchivedAt),
	})
}

// Update replaces the mutable Environment projection only while the resource
// remains active. The archived_at predicate prevents a stale read followed by
// an update from reviving an Environment that was archived concurrently.
func (r *EnvironmentRepository) Update(
	ctx context.Context,
	environment domain.Environment,
) (domain.Environment, error) {
	body, err := json.Marshal(environment)
	if err != nil {
		return domain.Environment{}, err
	}
	row := r.store.pool.QueryRow(ctx, `
UPDATE environments
SET name = $2, config_type = $3, body = $4, updated_at = $5
WHERE id = $1 AND archived_at IS NULL
RETURNING id, name, config_type, body, created_at, updated_at, archived_at`,
		environment.ID,
		environment.Name,
		environment.ConfigType,
		body,
		tsUTC(environment.UpdatedAt),
	)
	var stored pgstore.Environment
	if err := row.Scan(
		&stored.ID,
		&stored.Name,
		&stored.ConfigType,
		&stored.Body,
		&stored.CreatedAt,
		&stored.UpdatedAt,
		&stored.ArchivedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		exists, existsErr := r.store.q.EnvironmentExists(ctx, environment.ID)
		if existsErr != nil {
			return domain.Environment{}, existsErr
		}
		if !exists {
			return domain.Environment{}, domain.NotFound("environment not found")
		}
		return domain.Environment{}, domain.Validation("archived environment is read-only")
	} else if err != nil {
		return domain.Environment{}, err
	}
	return environmentFromRow(stored)
}

// Archive is idempotent and updates lifecycle columns without rewriting the
// JSON resource body. It therefore serializes safely with Update instead of
// allowing a stale read/Put pair to discard a concurrent field change.
func (r *EnvironmentRepository) Archive(
	ctx context.Context,
	id string,
	archivedAt time.Time,
) (domain.Environment, error) {
	row := r.store.pool.QueryRow(ctx, `
UPDATE environments
SET archived_at = COALESCE(archived_at, $2),
    updated_at = CASE WHEN archived_at IS NULL THEN $2 ELSE updated_at END
WHERE id = $1
RETURNING id, name, config_type, body, created_at, updated_at, archived_at`,
		id,
		tsUTC(archivedAt),
	)
	var stored pgstore.Environment
	if err := row.Scan(
		&stored.ID,
		&stored.Name,
		&stored.ConfigType,
		&stored.Body,
		&stored.CreatedAt,
		&stored.UpdatedAt,
		&stored.ArchivedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return domain.Environment{}, domain.NotFound("environment not found")
	} else if err != nil {
		return domain.Environment{}, err
	}
	return environmentFromRow(stored)
}

func (r *EnvironmentRepository) Get(
	ctx context.Context,
	id string,
) (domain.Environment, error) {
	row, err := r.store.q.GetEnvironment(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Environment{}, domain.NotFound("environment not found")
	}
	if err != nil {
		return domain.Environment{}, err
	}
	return environmentFromRow(row)
}

func (r *EnvironmentRepository) List(
	ctx context.Context,
	query app.EnvironmentListQuery,
) (app.EnvironmentListPage, error) {
	return r.store.ListEnvironments(ctx, query)
}

func (r *EnvironmentRepository) DeleteIfUnreferenced(ctx context.Context, id string) error {
	return r.store.withTx(ctx, func(q *pgstore.Queries) error {
		affected, err := q.DeleteEnvironmentIfUnreferenced(ctx, id)
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		exists, err := q.EnvironmentExists(ctx, id)
		if err != nil {
			return err
		}
		if !exists {
			return domain.NotFound("environment not found")
		}
		return domain.Conflict("environment is referenced by a session")
	})
}

func environmentFromRow(row pgstore.Environment) (domain.Environment, error) {
	var environment domain.Environment
	if err := json.Unmarshal(row.Body, &environment); err != nil {
		return domain.Environment{}, fmt.Errorf("pg: decode environment %s: %w", row.ID, err)
	}
	environment.ID = row.ID
	environment.Name = row.Name
	environment.ConfigType = row.ConfigType
	environment.CreatedAt = row.CreatedAt.Time.UTC()
	environment.UpdatedAt = row.UpdatedAt.Time.UTC()
	environment.ArchivedAt = timePtr(row.ArchivedAt)
	return environment, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
