package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// UnresolvedPendingActions returns every client action that still gates the
// session, including an action whose matching resolution has been admitted but
// whose resume turn has not completed. PostgreSQL is the source of truth for
// this wait state; Temporal wakeups carry only event-sequence metadata.
func (s *Store) UnresolvedPendingActions(
	ctx context.Context,
	sessionID string,
) ([]domain.PendingAction, error) {
	rows, err := s.q.ListUnresolvedPendingActions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PendingAction, 0, len(rows))
	for _, row := range rows {
		out = append(out, pendingActionFromRow(row))
	}
	return out, nil
}

func pendingActionFromRow(row pgstore.PendingAction) domain.PendingAction {
	return domain.PendingAction{
		ID:               row.ID,
		SessionID:        row.SessionID,
		ActionEventID:    row.ActionEventID,
		Kind:             domain.PendingActionKind(row.Kind),
		ResolvingEventID: row.ResolvingEventID,
		CreatedAt:        row.CreatedAt.Time.UTC(),
		ResolvedAt:       timePtr(row.ResolvedAt),
	}
}

// validatePendingCompletion requires the internal completion contract to match
// the public requires_action boundary. The pending ids are never accepted as an
// independent caller assertion: they must exactly match the ids in the
// session.status_idle draft committed by the same transaction.
func validatePendingCompletion(
	status domain.Status,
	drafts []domain.EventDraft,
	pendingActionEventIDs []string,
) error {
	if len(pendingActionEventIDs) == 0 {
		return nil
	}
	if status != domain.StatusIdle {
		return domain.Validation("a pending-action turn must complete idle")
	}

	expected := make(map[string]struct{}, len(pendingActionEventIDs))
	for _, id := range pendingActionEventIDs {
		if id == "" {
			return domain.Validation("pending action event id is required")
		}
		if _, duplicate := expected[id]; duplicate {
			return domain.Validation("duplicate pending action event id")
		}
		expected[id] = struct{}{}
	}

	var required []string
	for _, draft := range drafts {
		if draft.Type != domain.EvSessionStatusIdle {
			continue
		}
		stopReason, _ := draft.Payload["stop_reason"].(map[string]any)
		if stopReason["type"] != "requires_action" {
			continue
		}
		var ok bool
		required, ok = stringList(stopReason["event_ids"])
		if !ok {
			return domain.Validation("requires_action event_ids must be a string array")
		}
		break
	}
	if required == nil {
		return domain.Validation("pending actions require session.status_idle with requires_action")
	}
	if len(required) != len(expected) {
		return domain.Validation("requires_action event_ids must match pending action ids")
	}
	seen := make(map[string]struct{}, len(required))
	for _, id := range required {
		if _, duplicate := seen[id]; duplicate {
			return domain.Validation("requires_action contains a duplicate event id")
		}
		seen[id] = struct{}{}
		if _, ok := expected[id]; !ok {
			return domain.Validation("requires_action event_ids must match pending action ids")
		}
	}
	return nil
}

func stringList(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			item, ok := value.(string)
			if !ok || item == "" {
				return nil, false
			}
			out = append(out, item)
		}
		return out, true
	default:
		return nil, false
	}
}

// insertPendingActionsLocked persists the gate rows for action events committed
// by this completion. allowed contains only the events appended by the current
// turn, preventing a stale same-session event id from being reused as a new
// park.
func (s *Store) insertPendingActionsLocked(
	ctx context.Context,
	q *pgstore.Queries,
	sessionID string,
	pendingActionEventIDs []string,
	allowed map[string]domain.Event,
) error {
	for _, actionEventID := range pendingActionEventIDs {
		event, ok := allowed[actionEventID]
		if !ok {
			return domain.Validation(
				"pending action must reference an action event committed by this turn",
			)
		}
		kind, ok := domain.PendingActionKindForEvent(event.Type, event.Payload)
		if !ok {
			return domain.Validation("event cannot park a pending action")
		}
		if err := q.InsertPendingAction(ctx, pgstore.InsertPendingActionParams{
			ID:            s.ids.NewID(domain.PrefixPendingAction),
			SessionID:     sessionID,
			ActionEventID: actionEventID,
			Kind:          string(kind),
			CreatedAt:     tsUTC(s.clock.Now().UTC()),
		}); err != nil {
			return err
		}
	}
	return nil
}

// claimPendingResolutionsLocked validates and claims every resolution in an
// admitted batch. A claimed row remains unresolved until its resume turn closes,
// which keeps ordinary queued work behind the durable wait boundary.
func (s *Store) claimPendingResolutionsLocked(
	ctx context.Context,
	q *pgstore.Queries,
	sessionID string,
	events []domain.Event,
) (bool, error) {
	claimed := false
	for _, event := range events {
		actionEventID, kind, ok := domain.ResolutionReference(event.Type, event.Payload)
		if !ok {
			switch event.Type {
			case domain.EvUserCustomToolResult, domain.EvUserToolConfirmation:
				return false, domain.Validation("resolution event is missing its action event id")
			default:
				continue
			}
		}

		row, err := q.GetPendingActionForUpdate(ctx, pgstore.GetPendingActionForUpdateParams{
			SessionID:     sessionID,
			ActionEventID: actionEventID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, domain.Validation("resolution references unknown pending action")
		}
		if err != nil {
			return false, err
		}
		if domain.PendingActionKind(row.Kind) != kind {
			return false, domain.Validation("resolution kind does not match the pending action")
		}
		if row.ResolvedAt.Valid {
			return false, domain.Conflict("pending action is already resolved")
		}
		if row.ResolvingEventID != nil {
			return false, domain.Conflict("pending action already has a pending resolution")
		}
		affected, err := q.ClaimPendingAction(ctx, pgstore.ClaimPendingActionParams{
			ResolvingEventID: &event.ID,
			SessionID:        sessionID,
			ActionEventID:    actionEventID,
		})
		if err != nil {
			return false, err
		}
		if affected != 1 {
			return false, domain.Conflict("pending action already has a pending resolution")
		}
		claimed = true
	}
	return claimed, nil
}

// resolvePendingBarrierLocked atomically closes an entire requires_action
// barrier. The caller-supplied resolution ids are accepted only when they
// exactly match every unresolved row and include the turn trigger. This keeps a
// Workflow from accidentally clearing a partial or unrelated wait.
//
// An empty resolutionEventIDs slice retains the pre-barrier single-trigger
// behavior for existing Workflow histories.
func (s *Store) resolvePendingBarrierLocked(
	ctx context.Context,
	q *pgstore.Queries,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
) (bool, error) {
	if len(resolutionEventIDs) == 0 {
		affected, err := q.ResolvePendingActionsForTrigger(
			ctx,
			pgstore.ResolvePendingActionsForTriggerParams{
				ResolvedAt:       tsUTC(s.clock.Now().UTC()),
				SessionID:        sessionID,
				ResolvingEventID: &triggerEventID,
			},
		)
		return affected > 0, err
	}

	expected := make(map[string]struct{}, len(resolutionEventIDs))
	for _, id := range resolutionEventIDs {
		if id == "" {
			return false, domain.Validation("resolution event id is required")
		}
		if _, duplicate := expected[id]; duplicate {
			return false, domain.Validation("duplicate resolution event id")
		}
		expected[id] = struct{}{}
	}
	if _, ok := expected[triggerEventID]; !ok {
		return false, domain.Validation("resume trigger must be part of the resolution barrier")
	}

	rows, err := q.ListUnresolvedPendingActions(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expected) {
		return false, domain.Validation(
			"resolution event ids must match the complete pending-action barrier",
		)
	}
	// The schema's UNIQUE(session_id, resolving_event_id) constraint makes this
	// membership+cardinality check a bijection between rows and supplied ids.
	for _, row := range rows {
		if row.ResolvingEventID == nil {
			return false, domain.Validation("pending-action barrier is not fully claimed")
		}
		if _, ok := expected[*row.ResolvingEventID]; !ok {
			return false, domain.Validation(
				"resolution event ids must match the complete pending-action barrier",
			)
		}
	}

	affected, err := q.ResolvePendingActionsForEvents(
		ctx,
		pgstore.ResolvePendingActionsForEventsParams{
			ResolvedAt:        tsUTC(s.clock.Now().UTC()),
			SessionID:         sessionID,
			ResolvingEventIds: resolutionEventIDs,
		},
	)
	if err != nil {
		return false, err
	}
	if affected != int64(len(resolutionEventIDs)) {
		return false, domain.Conflict("pending-action barrier changed during completion")
	}
	return true, nil
}
