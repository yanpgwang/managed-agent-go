package pg

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// tsUTC wraps a UTC time as a valid pgtype.Timestamptz for insertion.
func tsUTC(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

// tsPtr wraps an optional time as a Timestamptz, invalid (NULL) when nil.
func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return tsUTC(*t)
}

// timePtr converts a Timestamptz back to an optional time.
func timePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time.UTC()
	return &t
}

// eventFromRow converts a generated pgstore.Event into the domain type,
// decoding the JSON payload.
func eventFromRow(row pgstore.Event) (domain.Event, error) {
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return domain.Event{}, fmt.Errorf("pg: decode event payload %s: %w", row.ID, err)
	}
	return domain.Event{
		ID:          row.ID,
		SessionID:   row.SessionID,
		Sequence:    row.Seq,
		Type:        row.Type,
		Payload:     payload,
		CreatedAt:   row.CreatedAt.Time.UTC(),
		ProcessedAt: timePtr(row.ProcessedAt),
	}, nil
}

func eventsFromRows(rows []pgstore.Event) ([]domain.Event, error) {
	out := make([]domain.Event, 0, len(rows))
	for _, row := range rows {
		event, err := eventFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

// sessionFromRow decodes the stored session body snapshot.
func sessionFromRow(row pgstore.Session) (domain.Session, error) {
	var session domain.Session
	if err := json.Unmarshal(row.Body, &session); err != nil {
		return domain.Session{}, fmt.Errorf("pg: decode session body %s: %w", row.ID, err)
	}
	return session, nil
}
