package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// RunStore owns the small transactional boundary between sessions, public
// events, and internal work items. It intentionally implements a single-node
// queue only: there are no leases, attempts, or distributed-worker semantics.
type RunStore struct {
	db    *DB
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewRunStore(db *DB, ids domain.IDGenerator, clock domain.Clock) *RunStore {
	return &RunStore{db: db, ids: ids, clock: clock}
}

type Admission struct {
	Session domain.Session
	Events  []domain.Event
	Run     *domain.SessionRun
}

type RunClaim struct {
	Run           domain.SessionRun
	Triggers      []domain.Event
	Events        []domain.Event
	AgentSnapshot domain.Agent
}

type RunCompletion struct {
	Run     domain.SessionRun
	Session domain.Session
	Events  []domain.Event
}

// CreateSession atomically creates a session and, when initial events are
// present, admits the first durable run.
func (s *RunStore) CreateSession(
	ctx context.Context,
	session domain.Session,
	drafts []domain.EventDraft,
) (Admission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Admission{}, err
	}
	defer tx.Rollback()

	if err := insertSessionIfDependenciesActive(ctx, tx, session); err != nil {
		return Admission{}, err
	}
	admission, err := s.admitTx(ctx, tx, session, drafts)
	if err != nil {
		return Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admission{}, err
	}
	return admission, nil
}

// Admit atomically persists client events, projects the session to running when
// needed, emits session.status_running, and enqueues one durable run.
func (s *RunStore) Admit(
	ctx context.Context,
	sessionID string,
	drafts []domain.EventDraft,
) (Admission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Admission{}, err
	}
	defer tx.Rollback()

	session, err := getSessionTx(ctx, tx, sessionID)
	if err != nil {
		return Admission{}, err
	}
	if session.ArchivedAt != nil {
		return Admission{}, domain.Conflict("cannot send events to an archived session")
	}
	if session.Status == domain.StatusTerminated {
		return Admission{}, domain.Conflict("cannot send events to a terminated session")
	}
	admission, err := s.admitTx(ctx, tx, session, drafts)
	if err != nil {
		return Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admission{}, err
	}
	return admission, nil
}

func (s *RunStore) admitTx(
	ctx context.Context,
	tx *sql.Tx,
	session domain.Session,
	drafts []domain.EventDraft,
) (Admission, error) {
	events, err := appendEventsTx(ctx, tx, s.ids, s.clock, session.ID, drafts)
	if err != nil {
		return Admission{}, err
	}
	triggerIDs := make([]string, 0, len(events))
	for _, event := range events {
		if domain.IsClientSubmittable(event.Type) {
			triggerIDs = append(triggerIDs, event.ID)
		}
	}
	admission := Admission{Session: session, Events: events}
	if len(triggerIDs) == 0 {
		return admission, nil
	}

	if session.Status != domain.StatusRunning {
		session.Status = domain.StatusRunning
		session.UpdatedAt = s.clock.Now().UTC()
		if err := updateSessionTx(ctx, tx, session); err != nil {
			return Admission{}, err
		}
		statusEvents, err := appendEventsTx(ctx, tx, s.ids, s.clock, session.ID, []domain.EventDraft{{
			Type: domain.EvSessionStatusRunning, Payload: map[string]any{},
		}})
		if err != nil {
			return Admission{}, err
		}
		admission.Events = append(admission.Events, statusEvents...)
		admission.Session = session
	}

	run, err := s.insertRunTx(ctx, tx, session.ID, triggerIDs)
	if err != nil {
		return Admission{}, err
	}
	admission.Run = &run
	return admission, nil
}

func (s *RunStore) insertRunTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	triggerIDs []string,
) (domain.SessionRun, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(admission_seq), 0) + 1 FROM session_runs WHERE session_id=?`,
		sessionID).Scan(&sequence); err != nil {
		return domain.SessionRun{}, err
	}
	triggerJSON, err := json.Marshal(triggerIDs)
	if err != nil {
		return domain.SessionRun{}, err
	}
	now := s.clock.Now().UTC()
	run := domain.SessionRun{
		ID:              s.ids.NewID(domain.PrefixRun),
		SessionID:       sessionID,
		AdmissionSeq:    sequence,
		TriggerEventIDs: append([]string(nil), triggerIDs...),
		State:           domain.RunQueued,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO session_runs
  (id, session_id, admission_seq, trigger_event_ids, state, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, NULL, ?, ?)`,
		run.ID, run.SessionID, run.AdmissionSeq, string(triggerJSON), string(run.State),
		timeVal(run.CreatedAt), timeVal(run.UpdatedAt))
	return run, err
}

// ClaimNext transitions the oldest queued run for a session to running. A
// partial unique index guarantees at most one running item per session.
func (s *RunStore) ClaimNext(ctx context.Context, sessionID string) (RunClaim, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunClaim{}, false, err
	}
	defer tx.Rollback()

	run, err := selectNextQueuedRun(ctx, tx, sessionID)
	if err == sql.ErrNoRows {
		return RunClaim{}, false, nil
	}
	if err != nil {
		return RunClaim{}, false, err
	}
	now := s.clock.Now().UTC()
	result, err := tx.ExecContext(ctx, `
UPDATE session_runs
SET state=?, updated_at=?
WHERE id=? AND state=?
  AND NOT EXISTS (
    SELECT 1 FROM session_runs
    WHERE session_id=? AND state=?
  )`,
		string(domain.RunRunning), timeVal(now), run.ID, string(domain.RunQueued),
		sessionID, string(domain.RunRunning))
	if err != nil {
		return RunClaim{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return RunClaim{}, false, err
	}
	if affected != 1 {
		return RunClaim{}, false, nil
	}
	run.State = domain.RunRunning
	run.UpdatedAt = now

	triggers, err := loadEventsByIDTx(ctx, tx, sessionID, run.TriggerEventIDs)
	if err != nil {
		return RunClaim{}, false, err
	}
	session, err := getSessionTx(ctx, tx, sessionID)
	if err != nil {
		return RunClaim{}, false, err
	}
	var statusEvents []domain.Event
	if session.Status != domain.StatusRunning {
		session.Status = domain.StatusRunning
		session.UpdatedAt = now
		if err := updateSessionTx(ctx, tx, session); err != nil {
			return RunClaim{}, false, err
		}
		statusEvents, err = appendEventsTx(ctx, tx, s.ids, s.clock, sessionID, []domain.EventDraft{{
			Type: domain.EvSessionStatusRunning, Payload: map[string]any{},
		}})
		if err != nil {
			return RunClaim{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RunClaim{}, false, err
	}
	return RunClaim{Run: run, Triggers: triggers, Events: statusEvents, AgentSnapshot: session.AgentSnapshot}, true, nil
}

// Complete atomically appends buffered runtime output, marks trigger events
// processed, updates the session projection, and closes the durable run.
func (s *RunStore) Complete(
	ctx context.Context,
	runID string,
	drafts []domain.EventDraft,
	status domain.Status,
	runError *string,
) (RunCompletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunCompletion{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return RunCompletion{}, err
	}
	if run.State != domain.RunRunning {
		return RunCompletion{}, domain.Conflict("run is not running")
	}
	session, err := getSessionTx(ctx, tx, run.SessionID)
	if err != nil {
		return RunCompletion{}, err
	}
	events, err := appendEventsTx(ctx, tx, s.ids, s.clock, run.SessionID, drafts)
	if err != nil {
		return RunCompletion{}, err
	}

	processedAt := timeVal(s.clock.Now().UTC())
	for _, eventID := range run.TriggerEventIDs {
		result, err := tx.ExecContext(ctx, `
UPDATE events
SET processed_at=COALESCE(processed_at, ?)
WHERE session_id=? AND id=?`, processedAt, run.SessionID, eventID)
		if err != nil {
			return RunCompletion{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return RunCompletion{}, err
		}
		if affected != 1 {
			return RunCompletion{}, domain.NotFound("run trigger event not found")
		}
	}

	var hasQueued bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM session_runs
  WHERE session_id=? AND state=? AND id<>?
)`, run.SessionID, string(domain.RunQueued), run.ID).Scan(&hasQueued); err != nil {
		return RunCompletion{}, err
	}
	// A terminated session is final: even if later runs were queued before the
	// failure, we do not resurrect it to running. Only a non-terminal completion
	// with more queued work reopens the session to running.
	if hasQueued && status != domain.StatusRunning && status != domain.StatusTerminated {
		status = domain.StatusRunning
		statusEvents, err := appendEventsTx(ctx, tx, s.ids, s.clock, run.SessionID, []domain.EventDraft{{
			Type: domain.EvSessionStatusRunning, Payload: map[string]any{},
		}})
		if err != nil {
			return RunCompletion{}, err
		}
		events = append(events, statusEvents...)
	}
	session.Status = status
	session.UpdatedAt = s.clock.Now().UTC()
	if err := updateSessionTx(ctx, tx, session); err != nil {
		return RunCompletion{}, err
	}

	run.UpdatedAt = s.clock.Now().UTC()
	run.Error = runError
	run.State = domain.RunCompleted
	if runError != nil {
		run.State = domain.RunFailed
	}
	_, err = tx.ExecContext(ctx, `
UPDATE session_runs
SET state=?, error=?, updated_at=?
WHERE id=? AND state=?`,
		string(run.State), nullableString(run.Error), timeVal(run.UpdatedAt),
		run.ID, string(domain.RunRunning))
	if err != nil {
		return RunCompletion{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunCompletion{}, err
	}
	return RunCompletion{Run: run, Session: session, Events: events}, nil
}

// UpdateTitle keeps the session row and session.updated event in one commit.
func (s *RunStore) UpdateTitle(
	ctx context.Context,
	sessionID string,
	title string,
) (domain.Session, []domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Session{}, nil, err
	}
	defer tx.Rollback()

	session, err := getSessionTx(ctx, tx, sessionID)
	if err != nil {
		return domain.Session{}, nil, err
	}
	if session.Title == title {
		return session, nil, nil
	}
	session.Title = title
	session.UpdatedAt = s.clock.Now().UTC()
	if err := updateSessionTx(ctx, tx, session); err != nil {
		return domain.Session{}, nil, err
	}
	events, err := appendEventsTx(ctx, tx, s.ids, s.clock, sessionID, []domain.EventDraft{{
		Type: domain.EvSessionUpdated,
		Payload: map[string]any{
			"title": title,
		},
	}})
	if err != nil {
		return domain.Session{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Session{}, nil, err
	}
	return session, events, nil
}

// Recover resets work that was running when the single process stopped and
// returns every session with queued work.
func (s *RunStore) Recover(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := timeVal(s.clock.Now().UTC())
	if _, err := tx.ExecContext(ctx, `
UPDATE session_runs
SET state=?, updated_at=?
WHERE state=?`, string(domain.RunQueued), now, string(domain.RunRunning)); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT session_id
FROM session_runs
WHERE state=?
ORDER BY session_id`, string(domain.RunQueued))
	if err != nil {
		return nil, err
	}
	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return nil, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sessionIDs, nil
}

func (s *RunStore) Get(ctx context.Context, id string) (domain.SessionRun, error) {
	run, err := getRunTx(ctx, s.db, id)
	if err == sql.ErrNoRows {
		return domain.SessionRun{}, domain.NotFound("run not found")
	}
	return run, err
}

type runQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRunTx(ctx context.Context, q runQueryRower, id string) (domain.SessionRun, error) {
	var (
		run                     domain.SessionRun
		triggerJSON             string
		state, created, updated string
		runError                sql.NullString
	)
	err := q.QueryRowContext(ctx, `
SELECT id, session_id, admission_seq, trigger_event_ids, state, error, created_at, updated_at
FROM session_runs
WHERE id=?`, id).Scan(
		&run.ID, &run.SessionID, &run.AdmissionSeq, &triggerJSON, &state,
		&runError, &created, &updated)
	if err != nil {
		return domain.SessionRun{}, err
	}
	if err := json.Unmarshal([]byte(triggerJSON), &run.TriggerEventIDs); err != nil {
		return domain.SessionRun{}, fmt.Errorf("store: decode run triggers: %w", err)
	}
	run.State = domain.RunState(state)
	if runError.Valid {
		run.Error = &runError.String
	}
	run.CreatedAt, err = parseRFC3339(created)
	if err != nil {
		return domain.SessionRun{}, err
	}
	run.UpdatedAt, err = parseRFC3339(updated)
	return run, err
}

func selectNextQueuedRun(ctx context.Context, tx *sql.Tx, sessionID string) (domain.SessionRun, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
SELECT id
FROM session_runs
WHERE session_id=? AND state=?
  AND NOT EXISTS (
    SELECT 1 FROM session_runs
    WHERE session_id=? AND state=?
  )
ORDER BY admission_seq
LIMIT 1`,
		sessionID, string(domain.RunQueued),
		sessionID, string(domain.RunRunning)).Scan(&id)
	if err != nil {
		return domain.SessionRun{}, err
	}
	return getRunTx(ctx, tx, id)
}

func loadEventsByIDTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	ids []string,
) ([]domain.Event, error) {
	events := make([]domain.Event, 0, len(ids))
	for _, id := range ids {
		var (
			event              domain.Event
			payload, createdAt string
			processedAt        sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
SELECT id, seq, type, payload, created_at, processed_at
FROM events
WHERE session_id=? AND id=?`, sessionID, id).Scan(
			&event.ID, &event.Sequence, &event.Type, &payload, &createdAt, &processedAt)
		if err == sql.ErrNoRows {
			return nil, domain.NotFound("run trigger event not found")
		}
		if err != nil {
			return nil, err
		}
		event.SessionID = sessionID
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, err
		}
		event.CreatedAt, err = parseRFC3339(createdAt)
		if err != nil {
			return nil, err
		}
		if processedAt.Valid {
			processed, err := parseRFC3339(processedAt.String)
			if err != nil {
				return nil, err
			}
			event.ProcessedAt = &processed
		}
		events = append(events, event)
	}
	return events, nil
}

type sessionQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getSessionTx(ctx context.Context, q sessionQueryRower, id string) (domain.Session, error) {
	var body string
	err := q.QueryRowContext(ctx, `SELECT body FROM sessions WHERE id=?`, id).Scan(&body)
	if err == sql.ErrNoRows {
		return domain.Session{}, domain.NotFound("session not found")
	}
	if err != nil {
		return domain.Session{}, err
	}
	var session domain.Session
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func updateSessionTx(ctx context.Context, tx *sql.Tx, session domain.Session) error {
	body, err := json.Marshal(session)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE sessions
SET status=?, body=?, updated_at=?, archived_at=?
WHERE id=?`,
		string(session.Status), string(body), timeVal(session.UpdatedAt),
		nullableTime(session.ArchivedAt), session.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.NotFound("session not found")
	}
	return nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
