package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// GetThreadContextSnapshotForTrigger returns the first committed compacted
// projection for one Thread trigger. It lets an Activity retry restore that
// projection before applying a possibly upgraded context policy.
func (s *Store) GetThreadContextSnapshotForTrigger(
	ctx context.Context,
	sessionID string,
	threadID string,
	triggerEventID string,
) (domain.ContextSnapshot, bool, error) {
	if sessionID == "" || threadID == "" || triggerEventID == "" {
		return domain.ContextSnapshot{}, false, domain.Validation(
			"session, thread, and trigger event are required for a context snapshot",
		)
	}
	row, err := s.q.GetThreadContextSnapshotForTrigger(
		ctx,
		pgstore.GetThreadContextSnapshotForTriggerParams{
			SessionID:      sessionID,
			ThreadID:       threadID,
			TriggerEventID: triggerEventID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ContextSnapshot{}, false, nil
	}
	if err != nil {
		return domain.ContextSnapshot{}, false, err
	}
	snapshot, err := contextSnapshotFromRow(row)
	return snapshot, err == nil, err
}

// PutThreadContextSnapshot creates the immutable compacted-context record for
// one Thread trigger. A retry returns the first committed value instead of
// recomputing the projection under possibly upgraded context-policy code.
func (s *Store) PutThreadContextSnapshot(
	ctx context.Context,
	sessionID string,
	threadID string,
	triggerEventID string,
	transcriptTriggerEventIDs []string,
	messages []domain.Message,
	projection domain.ContextProjection,
) (domain.ContextSnapshot, error) {
	if sessionID == "" || threadID == "" || triggerEventID == "" {
		return domain.ContextSnapshot{}, domain.Validation(
			"session, thread, and trigger event are required for a context snapshot",
		)
	}
	if !projection.Compacted {
		return domain.ContextSnapshot{}, domain.Validation(
			"a context snapshot requires a compacted projection",
		)
	}
	if len(messages) == 0 {
		return domain.ContextSnapshot{}, domain.Validation(
			"a context snapshot requires projected messages",
		)
	}
	for _, eventID := range transcriptTriggerEventIDs {
		if eventID == "" {
			return domain.ContextSnapshot{}, domain.Validation(
				"context snapshot transcript event ids cannot be empty",
			)
		}
	}

	transcriptJSON, err := json.Marshal(transcriptTriggerEventIDs)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	messagesJSON, err := json.Marshal(messages)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}
	projectionJSON, err := json.Marshal(projection)
	if err != nil {
		return domain.ContextSnapshot{}, err
	}

	var snapshot domain.ContextSnapshot
	err = s.withTx(ctx, func(q *pgstore.Queries) error {
		existing, err := q.GetThreadContextSnapshotForTrigger(
			ctx,
			pgstore.GetThreadContextSnapshotForTriggerParams{
				SessionID: sessionID, ThreadID: threadID,
				TriggerEventID: triggerEventID,
			},
		)
		if err == nil {
			snapshot, err = contextSnapshotFromRow(existing)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		var parentID *string
		parent, err := q.LatestThreadContextSnapshot(
			ctx,
			pgstore.LatestThreadContextSnapshotParams{
				SessionID: sessionID, ThreadID: threadID,
			},
		)
		switch {
		case err == nil:
			parentID = &parent.ID
		case errors.Is(err, pgx.ErrNoRows):
			// This is the first compaction on the Thread.
		default:
			return err
		}

		now := s.clock.Now().UTC()
		if err := q.InsertThreadContextSnapshot(
			ctx,
			pgstore.InsertThreadContextSnapshotParams{
				ID:        s.ids.NewID(domain.PrefixContextSnapshot),
				SessionID: sessionID, ThreadID: threadID,
				TriggerEventID: triggerEventID, ParentSnapshotID: parentID,
				TranscriptTriggerEventIds: transcriptJSON,
				Messages:                  messagesJSON, Projection: projectionJSON,
				ContextPolicyVersion: int32(domain.ContextPolicyVersion),
				CreatedAt:            tsUTC(now),
			},
		); err != nil {
			return err
		}
		row, err := q.GetThreadContextSnapshotForTrigger(
			ctx,
			pgstore.GetThreadContextSnapshotForTriggerParams{
				SessionID: sessionID, ThreadID: threadID,
				TriggerEventID: triggerEventID,
			},
		)
		if err != nil {
			return err
		}
		snapshot, err = contextSnapshotFromRow(row)
		return err
	})
	return snapshot, err
}

func contextSnapshotFromRow(
	row pgstore.ThreadContextSnapshot,
) (domain.ContextSnapshot, error) {
	var transcriptEventIDs []string
	if err := json.Unmarshal(row.TranscriptTriggerEventIds, &transcriptEventIDs); err != nil {
		return domain.ContextSnapshot{}, fmt.Errorf(
			"pg: decode context snapshot %s transcript boundary: %w",
			row.ID,
			err,
		)
	}
	var messages []domain.Message
	if err := json.Unmarshal(row.Messages, &messages); err != nil {
		return domain.ContextSnapshot{}, fmt.Errorf(
			"pg: decode context snapshot %s messages: %w",
			row.ID,
			err,
		)
	}
	var projection domain.ContextProjection
	if err := json.Unmarshal(row.Projection, &projection); err != nil {
		return domain.ContextSnapshot{}, fmt.Errorf(
			"pg: decode context snapshot %s projection: %w",
			row.ID,
			err,
		)
	}
	return domain.ContextSnapshot{
		ID: row.ID, SessionID: row.SessionID, ThreadID: row.ThreadID,
		TriggerEventID:            row.TriggerEventID,
		ParentSnapshotID:          row.ParentSnapshotID,
		TranscriptTriggerEventIDs: transcriptEventIDs,
		Messages:                  messages, Projection: projection,
		ContextPolicyVersion: int(row.ContextPolicyVersion),
		CreatedAt:            row.CreatedAt.Time.UTC(),
	}, nil
}
