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

func (r *AgentRepository) Versions(ctx context.Context, id string) ([]domain.Agent, error) {
	rows, err := r.store.q.ListAgentVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []domain.Agent{}, nil
	}
	return agentsFromRows(rows)
}

func (r *AgentRepository) List(ctx context.Context) ([]domain.Agent, error) {
	rows, err := r.store.q.ListLatestAgents(ctx)
	if err != nil {
		return nil, err
	}
	return agentsFromRows(rows)
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

func (r *EnvironmentRepository) List(ctx context.Context) ([]domain.Environment, error) {
	rows, err := r.store.q.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, row := range rows {
		environment, err := environmentFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, environment)
	}
	return out, nil
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
