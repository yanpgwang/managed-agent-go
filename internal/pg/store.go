package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// Store is the PostgreSQL platform-spine store. It owns the event-admission
// transaction, cursor reads for the SessionWorkflow, idempotent turn
// completion, and the coalescible orchestration outbox.
//
// It is a NEW path used only behind the feature gate; the SQLite store remains
// the default. PostgreSQL — not Temporal — is the source of truth for public
// events and the session projection.
type Store struct {
	pool  *pgxpool.Pool
	q     *pgstore.Queries
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewStore(pool *pgxpool.Pool, ids domain.IDGenerator, clock domain.Clock) *Store {
	return &Store{pool: pool, q: pgstore.New(pool), ids: ids, clock: clock}
}

// Admission is the result of an event-admission transaction: the committed
// public events (including any synthetic session.status_running), the resulting
// session projection, the highest receipt sequence after the batch, and whether
// a coalescible orchestration wakeup was written.
type Admission struct {
	Session  domain.Session
	Events   []domain.Event
	MaxSeq   int64
	Enqueued bool
}

// withTx runs fn inside a transaction bound to a tx-scoped Queries. It commits
// on success and rolls back on any error or panic.
func (s *Store) withTx(ctx context.Context, fn func(q *pgstore.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateSession inserts a session projection and, when initial events are
// present, admits them in one transaction: the events get receipt sequences, the
// session flips to running, and a coalescible orchestration wakeup is written.
func (s *Store) CreateSession(
	ctx context.Context,
	session domain.Session,
	drafts []domain.EventDraft,
) (Admission, error) {
	body, err := json.Marshal(session)
	if err != nil {
		return Admission{}, err
	}
	var admission Admission
	err = s.withTx(ctx, func(q *pgstore.Queries) error {
		if err := q.InsertSession(ctx, pgstore.InsertSessionParams{
			ID:        session.ID,
			Status:    string(session.Status),
			Body:      body,
			CreatedAt: tsUTC(session.CreatedAt),
			UpdatedAt: tsUTC(session.UpdatedAt),
		}); err != nil {
			return err
		}
		var innerErr error
		admission, innerErr = s.admitLocked(ctx, q, session, drafts)
		return innerErr
	})
	if err != nil {
		return Admission{}, err
	}
	return admission, nil
}

// AdmitEvents is the PostgreSQL event-admission transaction for the Temporal
// path. It locks the session, validates and admits an ordered event batch,
// assigns durable per-session receipt sequences, appends the public events and
// projection changes, and writes a coalescible orchestration outbox wakeup — all
// atomically. The outbox row is a wakeup carrying the highest known sequence, not
// a run queue: a second admission before delivery coalesces into the same row.
func (s *Store) AdmitEvents(
	ctx context.Context,
	sessionID string,
	drafts []domain.EventDraft,
) (Admission, error) {
	if len(drafts) == 0 {
		return Admission{}, domain.Validation("no events to admit")
	}
	var admission Admission
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		session, err := sessionFromRow(row)
		if err != nil {
			return err
		}
		if session.Status == domain.StatusTerminated {
			return domain.Conflict("cannot send events to a terminated session")
		}
		var innerErr error
		admission, innerErr = s.admitLocked(ctx, q, session, drafts)
		return innerErr
	})
	if err != nil {
		return Admission{}, err
	}
	return admission, nil
}

// admitLocked appends the batch under an already-taken session lock (or a
// freshly inserted session in CreateSession). It assigns receipt sequences,
// reopens the session to running when a client trigger arrives, and writes the
// coalescible wakeup.
func (s *Store) admitLocked(
	ctx context.Context,
	q *pgstore.Queries,
	session domain.Session,
	drafts []domain.EventDraft,
) (Admission, error) {
	for _, d := range drafts {
		if !domain.IsClientSubmittable(d.Type) {
			return Admission{}, domain.Validation("event type is not client-submittable: " + d.Type)
		}
	}

	maxSeq, err := q.MaxEventSeq(ctx, session.ID)
	if err != nil {
		return Admission{}, err
	}

	events, maxSeq, err := s.appendDrafts(ctx, q, session.ID, drafts, maxSeq, nil)
	if err != nil {
		return Admission{}, err
	}

	hasTrigger := false
	for _, d := range drafts {
		if domain.IsClientSubmittable(d.Type) {
			hasTrigger = true
			break
		}
	}

	admission := Admission{Session: session, Events: events, MaxSeq: maxSeq}
	if !hasTrigger {
		if err := s.putProjection(ctx, q, session); err != nil {
			return Admission{}, err
		}
		return admission, nil
	}

	// Reopen to running when a trigger arrives and the session is not already
	// running. Emit a synthetic session.status_running so the public event order
	// mirrors the SQLite path.
	if session.Status != domain.StatusRunning {
		session.Status = domain.StatusRunning
		session.UpdatedAt = s.clock.Now().UTC()
		statusEvents, newMax, err := s.appendDrafts(ctx, q, session.ID,
			[]domain.EventDraft{{Type: domain.EvSessionStatusRunning, Payload: map[string]any{}}},
			maxSeq, nil)
		if err != nil {
			return Admission{}, err
		}
		maxSeq = newMax
		events = append(events, statusEvents...)
		admission.Events = events
		admission.Session = session
		admission.MaxSeq = maxSeq
	}
	if err := s.putProjection(ctx, q, session); err != nil {
		return Admission{}, err
	}

	// The coalescible orchestration wakeup. One pending row per session; a burst
	// of admissions coalesces and raises max_event_seq to the newest receipt
	// sequence. This is the durable signal the relay delivers to Temporal.
	if err := q.UpsertOutbox(ctx, pgstore.UpsertOutboxParams{
		SessionID:   session.ID,
		MaxEventSeq: maxSeq,
		EnqueuedAt:  tsUTC(s.clock.Now().UTC()),
	}); err != nil {
		return Admission{}, err
	}
	admission.Enqueued = true
	return admission, nil
}

// appendDrafts inserts a slice of drafts starting after startSeq, returning the
// committed events and the new max sequence. turnEventID, when non-nil, tags
// every appended event so a completed turn's output can be replayed
// idempotently by trigger id.
func (s *Store) appendDrafts(
	ctx context.Context,
	q *pgstore.Queries,
	sessionID string,
	drafts []domain.EventDraft,
	startSeq int64,
	turnEventID *string,
) ([]domain.Event, int64, error) {
	seq := startSeq
	out := make([]domain.Event, 0, len(drafts))
	for _, d := range drafts {
		seq++
		id := d.ID
		if id == "" {
			id = s.ids.NewID(domain.PrefixEvent)
		}
		payload := d.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		now := s.clock.Now().UTC()
		var processedAt pgtype.Timestamptz
		var processedPtr *time.Time
		if domain.ProcessedOnReceipt(d.Type) {
			processedAt = tsUTC(now)
			t := now
			processedPtr = &t
		}
		if err := q.InsertEvent(ctx, pgstore.InsertEventParams{
			ID:          id,
			SessionID:   sessionID,
			Seq:         seq,
			Type:        d.Type,
			Payload:     payloadJSON,
			TurnEventID: turnEventID,
			CreatedAt:   tsUTC(now),
			ProcessedAt: processedAt,
		}); err != nil {
			return nil, 0, err
		}
		out = append(out, domain.Event{
			ID:          id,
			SessionID:   sessionID,
			Sequence:    seq,
			Type:        d.Type,
			Payload:     payload,
			CreatedAt:   now,
			ProcessedAt: processedPtr,
		})
	}
	return out, seq, nil
}

func (s *Store) putProjection(ctx context.Context, q *pgstore.Queries, session domain.Session) error {
	body, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return q.UpdateSessionStatus(ctx, pgstore.UpdateSessionStatusParams{
		Status:    string(session.Status),
		Body:      body,
		UpdatedAt: tsUTC(session.UpdatedAt),
		ID:        session.ID,
	})
}

// EventsAfter returns the session's public events with sequence strictly greater
// than cursor, in ascending receipt order, bounded by limit. This is the ordered
// consumption path the SessionWorkflow uses after its durable cursor.
func (s *Store) EventsAfter(ctx context.Context, sessionID string, cursor int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ListEventsAfter(ctx, pgstore.ListEventsAfterParams{
		SessionID: sessionID,
		AfterSeq:  cursor,
		RowLimit:  int32(limit),
	})
	if err != nil {
		return nil, err
	}
	return eventsFromRows(rows)
}

// GetSession returns the current session projection.
func (s *Store) GetSession(ctx context.Context, id string) (domain.Session, error) {
	row, err := s.q.GetSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.NotFound("session not found")
	}
	if err != nil {
		return domain.Session{}, err
	}
	return sessionFromRow(row)
}

// GetEvent returns a single public event by id.
func (s *Store) GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error) {
	row, err := s.q.GetEvent(ctx, pgstore.GetEventParams{SessionID: sessionID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Event{}, domain.NotFound("event not found")
	}
	if err != nil {
		return domain.Event{}, err
	}
	return eventFromRow(row)
}

// HistoryThrough returns the session's public events with sequence at or below
// seq, in ascending receipt order. It is the conversation projection input for a
// turn: bounding at the trigger's own sequence guarantees a turn never sees a
// later event admitted while it was queued.
func (s *Store) HistoryThrough(ctx context.Context, sessionID string, seq int64, limit int) ([]domain.Event, error) {
	all, err := s.EventsAfter(ctx, sessionID, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Event, 0, len(all))
	for _, e := range all {
		if e.Sequence <= seq {
			out = append(out, e)
		}
	}
	return out, nil
}

// TurnCompletion reports the committed output of a turn and whether this call
// actually performed the commit (Applied) or replayed an already-processed turn.
type TurnCompletion struct {
	Session domain.Session
	Events  []domain.Event
	Applied bool
}

// CompleteTurn atomically commits the authoritative output of one turn: it
// appends the runtime's output events (tagged with the trigger id), marks the
// trigger event processed, and updates the session projection to status. It is
// idempotent — the required property for a Temporal Activity, which may run more
// than once: a retry that finds the trigger already processed replays the exact
// events the first commit wrote instead of appending a second copy.
func (s *Store) CompleteTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	outputDrafts []domain.EventDraft,
	status domain.Status,
) (TurnCompletion, error) {
	var result TurnCompletion
	err := s.withTx(ctx, func(q *pgstore.Queries) error {
		row, err := q.LockSession(ctx, sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("session not found")
		}
		if err != nil {
			return err
		}
		session, err := sessionFromRow(row)
		if err != nil {
			return err
		}

		trigger, err := q.GetEvent(ctx, pgstore.GetEventParams{SessionID: sessionID, ID: triggerEventID})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("trigger event not found")
		}
		if err != nil {
			return err
		}
		// Idempotent replay: a trigger already stamped processed means this turn's
		// completion already committed. Return the exact events it wrote, by turn id,
		// so a duplicate Activity execution is harmless.
		if trigger.ProcessedAt.Valid {
			priorRows, err := q.ListEventsByTurn(ctx, pgstore.ListEventsByTurnParams{
				SessionID:   sessionID,
				TurnEventID: &triggerEventID,
			})
			if err != nil {
				return err
			}
			prior, err := eventsFromRows(priorRows)
			if err != nil {
				return err
			}
			result = TurnCompletion{Session: session, Events: prior, Applied: false}
			return nil
		}

		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}
		events, _, err := s.appendDrafts(ctx, q, sessionID, outputDrafts, maxSeq, &triggerEventID)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if err := q.MarkEventProcessed(ctx, pgstore.MarkEventProcessedParams{
			ProcessedAt: tsUTC(now),
			SessionID:   sessionID,
			ID:          triggerEventID,
		}); err != nil {
			return err
		}
		session.Status = status
		session.UpdatedAt = now
		if err := s.putProjection(ctx, q, session); err != nil {
			return err
		}
		result = TurnCompletion{Session: session, Events: events, Applied: true}
		return nil
	})
	if err != nil {
		return TurnCompletion{}, err
	}
	return result, nil
}
