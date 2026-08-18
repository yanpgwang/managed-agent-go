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

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func int32Ptr(value int) *int32 {
	if value == 0 {
		return nil
	}
	converted := int32(value)
	return &converted
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
		ThreadID:    row.ThreadID,
		Sequence:    row.Seq,
		Type:        row.Type,
		Payload:     payload,
		TurnEventID: row.TurnEventID,
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

func sessionFromBody(id string, workspaceID string, body []byte) (domain.Session, error) {
	var session domain.Session
	if err := json.Unmarshal(body, &session); err != nil {
		return domain.Session{}, fmt.Errorf("pg: decode session body %s: %w", id, err)
	}
	session.WorkspaceID = workspaceID
	return session, nil
}

func sessionFromGetRow(row pgstore.GetSessionRow) (domain.Session, error) {
	return sessionFromBody(row.ID, row.WorkspaceID, row.Body)
}

func sessionFromLockRow(row pgstore.LockSessionRow) (domain.Session, error) {
	return sessionFromBody(row.ID, row.WorkspaceID, row.Body)
}

func turnAttemptFromRow(row pgstore.TurnAttempt) TurnAttempt {
	return TurnAttempt{
		ID:             row.ID,
		SessionID:      row.SessionID,
		TriggerEventID: row.TriggerEventID,
		AttemptNo:      int(row.AttemptNo),
		State:          domain.RunAttemptState(row.State),
	}
}

func toolStepFromRow(row pgstore.ToolStep) (domain.ToolStep, error) {
	var input map[string]any
	if err := json.Unmarshal(row.Input, &input); err != nil {
		return domain.ToolStep{}, fmt.Errorf("pg: decode tool step input %s: %w", row.ID, err)
	}
	var result *domain.ToolStepResult
	if len(row.Result) > 0 {
		var decoded domain.ToolStepResult
		if err := json.Unmarshal(row.Result, &decoded); err != nil {
			return domain.ToolStep{}, fmt.Errorf("pg: decode tool step result %s: %w", row.ID, err)
		}
		result = &decoded
	}
	if row.State == string(domain.ToolStepCompleted) && result == nil {
		return domain.ToolStep{}, fmt.Errorf("pg: completed tool step %s has no durable result", row.ID)
	}
	return domain.ToolStep{
		ID:             row.ID,
		AttemptID:      row.AttemptID,
		Ordinal:        int(row.Ordinal),
		ToolUseEventID: row.ToolUseEventID,
		ToolName:       row.ToolName,
		Input:          input,
		State:          domain.ToolStepState(row.State),
		Result:         result,
		CreatedAt:      row.CreatedAt.Time.UTC(),
		UpdatedAt:      row.UpdatedAt.Time.UTC(),
		StartedAt:      timePtr(row.StartedAt),
		FinishedAt:     timePtr(row.FinishedAt),
	}, nil
}
