package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type EventStore struct {
	db    *DB
	ids   domain.IDGenerator
	clock domain.Clock
}

// eventProcessedKeySQL normalizes the processed_at column to a fixed-width
// nanosecond representation for correct ordering and boundary comparisons.
var eventProcessedKeySQL = nanoKeySQL("processed_at")

const eventProcessedKeyFormat = "2006-01-02T15:04:05.000000000Z"

func NewEventStore(db *DB, ids domain.IDGenerator, clock domain.Clock) *EventStore {
	return &EventStore{db: db, ids: ids, clock: clock}
}

func (s *EventStore) Append(ctx context.Context, sessionID string, drafts []domain.EventDraft) ([]domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	out, err := appendEventsTx(ctx, tx, s.ids, s.clock, sessionID, drafts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func appendEventsTx(
	ctx context.Context,
	tx *sql.Tx,
	ids domain.IDGenerator,
	clock domain.Clock,
	sessionID string,
	drafts []domain.EventDraft,
) ([]domain.Event, error) {
	var maxSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM events WHERE session_id=?`, sessionID).Scan(&maxSeq); err != nil {
		return nil, err
	}

	out := make([]domain.Event, 0, len(drafts))
	for _, d := range drafts {
		maxSeq++
		// Honor a pre-assigned committed id (set by a server-side emitter that
		// needed the id before this transaction); otherwise mint a fresh one.
		id := d.ID
		if id == "" {
			id = ids.NewID(domain.PrefixEvent)
		}
		payload, err := json.Marshal(d.Payload)
		if err != nil {
			return nil, err
		}
		now := clock.Now().UTC()
		var processedAt *string
		var processedTime *time.Time
		if domain.ProcessedOnReceipt(d.Type) {
			str := timeVal(now)
			processedAt = &str
			t := now
			processedTime = &t
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (id, session_id, seq, type, payload, created_at, processed_at) VALUES (?,?,?,?,?,?,?)`,
			id, sessionID, maxSeq, d.Type, string(payload), timeVal(now), processedAt); err != nil {
			return nil, err
		}
		out = append(out, domain.Event{
			ID: id, SessionID: sessionID, Sequence: maxSeq, Type: d.Type,
			Payload: d.Payload, CreatedAt: now, ProcessedAt: processedTime,
		})
	}
	return out, nil
}

// EventQuery expresses the public List Events filters. AfterSeq/BeforeSeq are
// internal cursors (never exposed on the wire); the HTTP layer encodes them into
// an opaque page token. Types filters by event type. The public created_at[*]
// query names compare against processed_at, matching the official SDK contract.
type EventQuery struct {
	AfterSeq     int64 // exclusive lower bound (ascending pagination)
	BeforeSeq    int64 // exclusive upper bound (descending pagination)
	Limit        int
	Desc         bool
	Types        []string
	CreatedAtGt  *time.Time
	CreatedAtGte *time.Time
	CreatedAtLt  *time.Time
	CreatedAtLte *time.Time
}

// History returns events after a sequence cursor in ascending order. It backs
// internal reconciliation, which needs total, stably ordered history.
func (s *EventStore) History(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]domain.Event, error) {
	return s.Query(ctx, sessionID, EventQuery{AfterSeq: afterSeq, Limit: limit})
}

// HistoryTail returns the newest `limit` events for a session in ascending
// (chronological) sequence order. Where History returns the OLDEST events after
// a cursor, HistoryTail bounds an over-limit session to its most RECENT window
// so model projection carries recent context rather than the start of the
// conversation. The newest events are selected by sequence descending, then
// reversed back to chronological order. Pairing across the window boundary is
// not repaired here — ProjectMessages already drops a dangling tool_use or an
// orphan tool_result, so a window that splits a pair still projects to a legal
// Messages-API request.
func (s *EventStore) HistoryTail(ctx context.Context, sessionID string, limit int) ([]domain.Event, error) {
	desc, err := s.Query(ctx, sessionID, EventQuery{Limit: limit, Desc: true})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc, nil
}

// Query lists events for a session applying the public List Events filters.
// Ordering remains the stable internal sequence until processed_at keyset
// ordering (including null semantics) is implemented.
func (s *EventStore) Query(ctx context.Context, sessionID string, q EventQuery) ([]domain.Event, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}

	where := []string{"session_id=?"}
	args := []any{sessionID}
	if q.AfterSeq > 0 {
		where = append(where, "seq>?")
		args = append(args, q.AfterSeq)
	}
	if q.BeforeSeq > 0 {
		where = append(where, "seq<?")
		args = append(args, q.BeforeSeq)
	}
	if len(q.Types) > 0 {
		placeholders := make([]string, len(q.Types))
		for i, t := range q.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		where = append(where, "type IN ("+strings.Join(placeholders, ",")+")")
	}
	for _, b := range []struct {
		t  *time.Time
		op string
	}{
		{q.CreatedAtGt, ">"}, {q.CreatedAtGte, ">="},
		{q.CreatedAtLt, "<"}, {q.CreatedAtLte, "<="},
	} {
		if b.t != nil {
			where = append(where, eventProcessedKeySQL+" "+b.op+" ?")
			args = append(args, eventProcessedKey(*b.t))
		}
	}

	order := "ASC"
	if q.Desc {
		order = "DESC"
	}
	args = append(args, limit)

	query := `SELECT id, seq, type, payload, created_at, processed_at FROM events WHERE ` +
		strings.Join(where, " AND ") + ` ORDER BY seq ` + order + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var ev domain.Event
		var payload, createdAt string
		var processedAt sql.NullString
		if err := rows.Scan(&ev.ID, &ev.Sequence, &ev.Type, &payload, &createdAt, &processedAt); err != nil {
			return nil, err
		}
		ev.SessionID = sessionID
		if err := json.Unmarshal([]byte(payload), &ev.Payload); err != nil {
			return nil, fmt.Errorf("store: decode payload for event %s: %w", ev.ID, err)
		}
		if tm, err := parseRFC3339(createdAt); err == nil {
			ev.CreatedAt = tm
		}
		if processedAt.Valid {
			if tm, err := parseRFC3339(processedAt.String); err == nil {
				ev.ProcessedAt = &tm
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func eventProcessedKey(value time.Time) string {
	return value.UTC().Format(eventProcessedKeyFormat)
}
