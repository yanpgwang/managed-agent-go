package store

import (
	"context"
	"database/sql"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// pendingActionRow is the internal projection of an unresolved pending action
// used by the claim gate and admission validation. It is never serialized onto
// the public wire.
type pendingActionRow struct {
	id               string
	actionEventID    string
	kind             domain.PendingActionKind
	resolvingEventID *string
}

// unresolvedPendingActions returns every pending action for the session that has
// not yet been resolved (resolved_at IS NULL), in stable creation order. A
// non-empty result gates ordinary queued runs: only a run whose trigger matches
// one of these (by resolving_event_id) may be claimed.
func unresolvedPendingActions(ctx context.Context, tx *sql.Tx, sessionID string) ([]pendingActionRow, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, action_event_id, kind, resolving_event_id
FROM pending_actions
WHERE session_id=? AND resolved_at IS NULL
ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingActionRow
	for rows.Next() {
		var (
			r         pendingActionRow
			kind      string
			resolving sql.NullString
		)
		if err := rows.Scan(&r.id, &r.actionEventID, &kind, &resolving); err != nil {
			return nil, err
		}
		r.kind = domain.PendingActionKind(kind)
		if resolving.Valid {
			r.resolvingEventID = &resolving.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// insertPendingActionTx persists one durable pending action for a parked action
// event. allowed maps the ids of the action events THIS Complete call just
// committed to their committed type and payload; only such an id may park. This
// rejects an old action event from an earlier run in the same session, a phantom
// id, or any output that is not in the current drafts — merely session-local
// existence is not trusted. The expected response kind is derived from the
// committed event's own type AND payload (an agent.tool_use parks only when its
// evaluated_permission is "ask") — never a caller string. Duplicate action ids
// within one park are rejected by the caller before reaching this function, so
// the (session_id, action_event_id) ON CONFLICT clause only guards a repeated
// park of the same event across separate Complete calls, keeping it a single
// gate rather than erroring.
func insertPendingActionTx(
	ctx context.Context,
	tx *sql.Tx,
	ids domain.IDGenerator,
	clock domain.Clock,
	sessionID string,
	actionEventID string,
	allowed map[string]domain.Event,
) error {
	event, ok := allowed[actionEventID]
	if !ok {
		return domain.Validation("pending action must reference an action event committed by this run")
	}
	kind, ok := domain.PendingActionKindForEvent(event.Type, event.Payload)
	if !ok {
		return domain.Validation("event type cannot park a pending action")
	}
	now := timeVal(clock.Now().UTC())
	_, err := tx.ExecContext(ctx, `
INSERT INTO pending_actions (id, session_id, action_event_id, kind, resolving_event_id, created_at, resolved_at)
VALUES (?, ?, ?, ?, NULL, ?, NULL)
ON CONFLICT (session_id, action_event_id) DO NOTHING`,
		ids.NewID(domain.PrefixPendingAction), sessionID, actionEventID, string(kind), now)
	return err
}

// claimPendingActionTx validates that a resolution event may resolve a pending
// action and records the resolving event id. It enforces, atomically:
//   - the referenced action event has an OPEN pending action in this session
//     (unknown / wrong-session references find no row);
//   - the pending action's kind matches the resolution kind (wrong-kind);
//   - the pending action has not already been resolved or claimed by an earlier
//     resolution (already-resolved / duplicate).
//
// On success it sets resolving_event_id to the committed resolution event id,
// moving the pending action from OPEN to CLAIMED. resolved_at stays NULL until
// the resume run closes, so the gate remains active for ordinary queued runs
// while the matching resume run is the one allowed to proceed.
func claimPendingActionTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	actionEventID string,
	kind domain.PendingActionKind,
	resolvingEventID string,
) error {
	var (
		storedKind string
		resolvedAt sql.NullString
		resolving  sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
SELECT kind, resolved_at, resolving_event_id
FROM pending_actions
WHERE session_id=? AND action_event_id=?`, sessionID, actionEventID).Scan(&storedKind, &resolvedAt, &resolving)
	if err == sql.ErrNoRows {
		return domain.Validation("resolution references unknown pending action")
	}
	if err != nil {
		return err
	}
	if domain.PendingActionKind(storedKind) != kind {
		return domain.Validation("resolution kind does not match the pending action")
	}
	if resolvedAt.Valid {
		return domain.Conflict("pending action is already resolved")
	}
	if resolving.Valid {
		return domain.Conflict("pending action already has a pending resolution")
	}
	result, err := tx.ExecContext(ctx, `
UPDATE pending_actions
SET resolving_event_id=?
WHERE session_id=? AND action_event_id=? AND resolving_event_id IS NULL AND resolved_at IS NULL`,
		resolvingEventID, sessionID, actionEventID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		// Lost a race with a concurrent resolution for the same action; treat as a
		// duplicate rather than silently succeeding.
		return domain.Conflict("pending action already has a pending resolution")
	}
	return nil
}

// resolvePendingActionsForTriggers marks resolved every pending action whose
// resolving_event_id is one of this run's trigger events. Called in the same
// transaction that closes the resume run, so the ordinary queued work a park
// blocked becomes claimable only after the resume run has closed. Returns the
// number of pending actions resolved.
func resolvePendingActionsForTriggers(
	ctx context.Context,
	tx *sql.Tx,
	clock domain.Clock,
	sessionID string,
	triggerEventIDs []string,
) (int, error) {
	resolved := 0
	now := timeVal(clock.Now().UTC())
	for _, triggerID := range triggerEventIDs {
		result, err := tx.ExecContext(ctx, `
UPDATE pending_actions
SET resolved_at=?
WHERE session_id=? AND resolving_event_id=? AND resolved_at IS NULL`,
			now, sessionID, triggerID)
		if err != nil {
			return resolved, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return resolved, err
		}
		resolved += int(affected)
	}
	return resolved, nil
}

// hasUnresolvedPendingTx reports whether the session still has any pending
// action that has not been resolved. While true, ordinary queued runs are gated.
func hasUnresolvedPendingTx(ctx context.Context, tx *sql.Tx, sessionID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM pending_actions WHERE session_id=? AND resolved_at IS NULL)`,
		sessionID).Scan(&exists)
	return exists, err
}
