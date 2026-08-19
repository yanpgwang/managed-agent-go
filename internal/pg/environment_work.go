package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/pg/pgstore"
)

const environmentWorkColumns = `
id, environment_id, session_id, state, metadata, created_at,
acknowledged_at, started_at, latest_heartbeat_at, ttl_seconds,
stop_requested_at, stopped_at`

const environmentWorkTargetColumns = `
work.id, work.environment_id, work.session_id, work.state, work.metadata, work.created_at,
work.acknowledged_at, work.started_at, work.latest_heartbeat_at, work.ttl_seconds,
work.stop_requested_at, work.stopped_at`

type EnvironmentWorkRepository struct{ store *Store }

func NewEnvironmentWorkRepository(store *Store) *EnvironmentWorkRepository {
	return &EnvironmentWorkRepository{store: store}
}

func (r *EnvironmentWorkRepository) authorizeEnvironment(ctx context.Context, id string) error {
	_, err := NewEnvironmentRepository(r.store).Get(ctx, id)
	return err
}

type workScanner interface{ Scan(...any) error }

func scanEnvironmentWork(row workScanner) (domain.EnvironmentWork, error) {
	var (
		work       domain.EnvironmentWork
		state      string
		metadata   []byte
		ack, start *time.Time
		heartbeat  *time.Time
		requested  *time.Time
		stopped    *time.Time
	)
	err := row.Scan(
		&work.ID, &work.EnvironmentID, &work.SessionID, &state, &metadata,
		&work.CreatedAt, &ack, &start, &heartbeat, &work.TTLSeconds,
		&requested, &stopped,
	)
	if err != nil {
		return domain.EnvironmentWork{}, err
	}
	if err := json.Unmarshal(metadata, &work.Metadata); err != nil {
		return domain.EnvironmentWork{}, err
	}
	work.State = domain.EnvironmentWorkState(state)
	work.CreatedAt = work.CreatedAt.UTC()
	work.AcknowledgedAt = utcTimePtr(ack)
	work.StartedAt = utcTimePtr(start)
	work.LatestHeartbeatAt = utcTimePtr(heartbeat)
	work.StopRequestedAt = utcTimePtr(requested)
	work.StoppedAt = utcTimePtr(stopped)
	return work, nil
}

func (r *EnvironmentWorkRepository) GetWork(
	ctx context.Context,
	environmentID, workID string,
) (domain.EnvironmentWork, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return domain.EnvironmentWork{}, err
	}
	work, err := scanEnvironmentWork(r.store.pool.QueryRow(ctx,
		`SELECT `+environmentWorkColumns+` FROM environment_work
WHERE environment_id = $1 AND id = $2`, environmentID, workID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EnvironmentWork{}, domain.NotFound("work item not found")
	}
	return work, err
}

func (r *EnvironmentWorkRepository) UpdateWorkMetadata(
	ctx context.Context,
	environmentID, workID string,
	patch map[string]*string,
) (domain.EnvironmentWork, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return domain.EnvironmentWork{}, err
	}
	var result domain.EnvironmentWork
	err := r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		current, err := scanEnvironmentWork(tx.QueryRow(ctx,
			`SELECT `+environmentWorkColumns+` FROM environment_work
WHERE environment_id = $1 AND id = $2 FOR UPDATE`, environmentID, workID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("work item not found")
		}
		if err != nil {
			return err
		}
		if current.Metadata == nil {
			current.Metadata = map[string]string{}
		}
		for key, value := range patch {
			if value == nil {
				delete(current.Metadata, key)
			} else {
				current.Metadata[key] = *value
			}
		}
		body, err := json.Marshal(current.Metadata)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE environment_work SET metadata = $3
WHERE environment_id = $1 AND id = $2`, environmentID, workID, body); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}

func (r *EnvironmentWorkRepository) ListWork(
	ctx context.Context,
	environmentID string,
	query app.EnvironmentWorkListQuery,
) (app.EnvironmentWorkListPage, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return app.EnvironmentWorkListPage{}, err
	}
	clauses := []string{"environment_id = $1"}
	args := []any{environmentID}
	if query.After != nil {
		args = append(args, query.After.CreatedAt, query.After.ID)
		clauses = append(clauses, fmt.Sprintf(
			`(created_at < $%d OR (created_at = $%d AND id < $%d))`,
			len(args)-1, len(args)-1, len(args),
		))
	}
	args = append(args, query.Limit+1)
	rows, err := r.store.pool.Query(ctx,
		`SELECT `+environmentWorkColumns+` FROM environment_work WHERE `+
			strings.Join(clauses, " AND ")+fmt.Sprintf(
			` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return app.EnvironmentWorkListPage{}, err
	}
	defer rows.Close()
	items := make([]domain.EnvironmentWork, 0, query.Limit+1)
	for rows.Next() {
		item, err := scanEnvironmentWork(rows)
		if err != nil {
			return app.EnvironmentWorkListPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.EnvironmentWorkListPage{}, err
	}
	page := app.EnvironmentWorkListPage{Work: items}
	if len(items) > query.Limit {
		page.HasNext = true
		page.Work = items[:query.Limit]
	}
	return page, nil
}

func (r *EnvironmentWorkRepository) PollWork(
	ctx context.Context,
	environmentID string,
	input app.EnvironmentWorkPollInput,
) (*domain.EnvironmentWork, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return nil, err
	}
	var result *domain.EnvironmentWork
	now := r.store.clock.Now().UTC().Truncate(time.Microsecond)
	err := r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		if input.WorkerID != "" {
			if _, err := tx.Exec(ctx, `
INSERT INTO environment_work_pollers (environment_id, worker_id, polled_at)
VALUES ($1, $2, $3)
ON CONFLICT (environment_id, worker_id) DO UPDATE SET polled_at = EXCLUDED.polled_at`,
				environmentID, input.WorkerID, now); err != nil {
				return err
			}
		}
		// Recover workers that disappeared after Ack or after their last lease
		// heartbeat. Reusing the Work ID preserves the control-plane audit item.
		if _, err := tx.Exec(ctx, `
UPDATE environment_work
SET state = 'stopped', stopped_at = COALESCE(stopped_at, $2)
WHERE environment_id = $1 AND state = 'stopping'
  AND stop_requested_at < $2::timestamptz - make_interval(secs => ttl_seconds)`,
			environmentID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
UPDATE environment_work
SET state = 'queued', acknowledged_at = NULL, started_at = NULL,
    latest_heartbeat_at = NULL, polled_at = NULL, poll_worker_id = NULL
WHERE environment_id = $1 AND (
    (state = 'starting' AND acknowledged_at < $2::timestamptz - make_interval(secs => ttl_seconds)) OR
    (state = 'active' AND latest_heartbeat_at < $2::timestamptz - make_interval(secs => ttl_seconds))
)`, environmentID, now); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
WITH candidate AS (
    SELECT id
    FROM environment_work
    WHERE environment_id = $1
      AND state = 'queued'
      AND (polled_at IS NULL OR polled_at <= $2)
      AND NOT EXISTS (
          SELECT 1 FROM environment_work AS predecessor
          WHERE predecessor.session_id = environment_work.session_id
            AND predecessor.id <> environment_work.id
            AND predecessor.state IN ('starting', 'active', 'stopping')
      )
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE environment_work AS work
SET polled_at = $3, poll_worker_id = NULLIF($4, '')
FROM candidate
WHERE work.id = candidate.id
RETURNING `+environmentWorkTargetColumns,
			environmentID, now.Add(-input.ReclaimAge), now, input.WorkerID)
		work, err := scanEnvironmentWork(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result = &work
		return nil
	})
	return result, err
}

func (r *EnvironmentWorkRepository) AckWork(
	ctx context.Context,
	environmentID, workID string,
) (domain.EnvironmentWork, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return domain.EnvironmentWork{}, err
	}
	now := r.store.clock.Now().UTC().Truncate(time.Microsecond)
	work, err := scanEnvironmentWork(r.store.pool.QueryRow(ctx, `
UPDATE environment_work
SET state = 'starting', acknowledged_at = $3
WHERE environment_id = $1 AND id = $2 AND state = 'queued' AND polled_at IS NOT NULL
RETURNING `+environmentWorkColumns, environmentID, workID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := r.GetWork(ctx, environmentID, workID); getErr != nil {
			return domain.EnvironmentWork{}, getErr
		}
		return domain.EnvironmentWork{}, domain.Conflict("work item is not awaiting acknowledgement")
	}
	return work, err
}

func (r *EnvironmentWorkRepository) HeartbeatWork(
	ctx context.Context,
	environmentID, workID string,
	expected *string,
	desiredTTL *int64,
) (domain.EnvironmentWorkHeartbeat, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return domain.EnvironmentWorkHeartbeat{}, err
	}
	var response domain.EnvironmentWorkHeartbeat
	now := r.store.clock.Now().UTC().Truncate(time.Microsecond)
	err := r.store.withPGXTx(ctx, func(tx pgx.Tx, _ *pgstore.Queries) error {
		work, err := scanEnvironmentWork(tx.QueryRow(ctx,
			`SELECT `+environmentWorkColumns+` FROM environment_work
WHERE environment_id = $1 AND id = $2 FOR UPDATE`, environmentID, workID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("work item not found")
		}
		if err != nil {
			return err
		}
		if expected != nil {
			matches := *expected == "NO_HEARTBEAT" && work.LatestHeartbeatAt == nil
			if work.LatestHeartbeatAt != nil {
				matches = *expected == work.LatestHeartbeatAt.Format(time.RFC3339Nano)
			}
			if !matches {
				return domain.Precondition("work heartbeat precondition failed")
			}
		}
		if work.State != domain.EnvironmentWorkStarting &&
			work.State != domain.EnvironmentWorkActive {
			last := now
			if work.LatestHeartbeatAt != nil {
				last = *work.LatestHeartbeatAt
			}
			response = domain.EnvironmentWorkHeartbeat{
				LastHeartbeat: last, LeaseExtended: false,
				State: work.State, TTLSeconds: work.TTLSeconds,
			}
			return nil
		}
		ttl := work.TTLSeconds
		if desiredTTL != nil {
			ttl = *desiredTTL
		}
		started := work.StartedAt
		if started == nil {
			started = &now
		}
		if _, err := tx.Exec(ctx, `
UPDATE environment_work
SET state = 'active', started_at = $3, latest_heartbeat_at = $4, ttl_seconds = $5
WHERE environment_id = $1 AND id = $2`,
			environmentID, workID, started, now, ttl); err != nil {
			return err
		}
		response = domain.EnvironmentWorkHeartbeat{
			LastHeartbeat: now, LeaseExtended: true,
			State: domain.EnvironmentWorkActive, TTLSeconds: ttl,
		}
		return nil
	})
	return response, err
}

func (r *EnvironmentWorkRepository) StopWork(
	ctx context.Context,
	environmentID, workID string,
	force bool,
) error {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return err
	}
	now := r.store.clock.Now().UTC().Truncate(time.Microsecond)
	return r.store.withPGXTx(ctx, func(tx pgx.Tx, q *pgstore.Queries) error {
		var (
			state, sessionID string
			activationSeq    int64
		)
		err := tx.QueryRow(ctx, `SELECT state, session_id, activation_seq FROM environment_work
WHERE environment_id = $1 AND id = $2 FOR UPDATE`, environmentID, workID).Scan(
			&state, &sessionID, &activationSeq,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("work item not found")
		}
		if err != nil {
			return err
		}
		if state == string(domain.EnvironmentWorkStopped) {
			return domain.Conflict("work item is already stopped")
		}
		next := domain.EnvironmentWorkStopping
		stoppedAt := (*time.Time)(nil)
		if force || state == string(domain.EnvironmentWorkQueued) ||
			state == string(domain.EnvironmentWorkStarting) {
			next = domain.EnvironmentWorkStopped
			stoppedAt = &now
		}
		_, err = tx.Exec(ctx, `
UPDATE environment_work
SET state = $3, stop_requested_at = COALESCE(stop_requested_at, $4), stopped_at = $5
WHERE environment_id = $1 AND id = $2`, environmentID, workID, next, now, stoppedAt)
		if err != nil {
			return err
		}
		// Runnable input can race a worker's shutdown. Queue a successor while
		// retaining the stopping item, and let Poll serialize the handoff. This
		// closes the admission-before-Stop race without allowing two workers to
		// execute the same Session concurrently.
		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
INSERT INTO environment_work (
    id, environment_id, session_id, activation_seq, state, metadata, created_at
)
SELECT $1, $2, $3, $4, 'queued', '{}'::jsonb, $5
WHERE EXISTS (
    SELECT 1 FROM events
    WHERE session_id = $3 AND seq > $6 AND processed_at IS NULL
      AND type IN (
          'user.message', 'user.define_outcome', 'user.custom_tool_result',
          'user.tool_confirmation', 'user.tool_result'
      )
)
ON CONFLICT (session_id) WHERE state IN ('queued', 'starting', 'active') DO NOTHING`,
			r.store.ids.NewID(domain.PrefixEnvironmentWork), environmentID, sessionID,
			maxSeq, now, activationSeq)
		return err
	})
}

func (r *EnvironmentWorkRepository) WorkStats(
	ctx context.Context,
	environmentID string,
) (domain.EnvironmentWorkQueueStats, error) {
	if err := r.authorizeEnvironment(ctx, environmentID); err != nil {
		return domain.EnvironmentWorkQueueStats{}, err
	}
	var (
		stats  domain.EnvironmentWorkQueueStats
		oldest *time.Time
	)
	err := r.store.pool.QueryRow(ctx, `
SELECT
    COUNT(*) FILTER (WHERE state = 'queued' AND polled_at IS NULL)::bigint,
    COUNT(*) FILTER (WHERE state = 'queued' AND polled_at IS NOT NULL)::bigint,
    MIN(created_at) FILTER (WHERE state = 'queued'),
    (SELECT COUNT(*)::bigint FROM environment_work_pollers
     WHERE environment_id = $1 AND polled_at >= $2)
FROM environment_work
WHERE environment_id = $1`, environmentID, r.store.clock.Now().UTC().Add(-30*time.Second)).Scan(
		&stats.Depth, &stats.Pending, &oldest, &stats.WorkersPolling,
	)
	stats.OldestQueuedAt = utcTimePtr(oldest)
	return stats, err
}
