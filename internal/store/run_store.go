package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// RunStore owns the transactional boundary between sessions, public events,
// internal work items, and the durable execution-attempt/tool-step journal. It
// remains a single-node queue; distributed leases are outside this layer.
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
	// Runs holds one durable queued run per processable trigger event, in the
	// same stable order as the admitted events. Multiple trigger IDs are never
	// grouped into a single run.
	Runs []domain.SessionRun
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
// present, admits one durable queued run per processable trigger event.
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

// Admit atomically persists client events, enqueues one durable run per
// processable trigger event (in admission order), and reopens the session to
// running only when that new work is actually claimable — projecting to running
// and emitting session.status_running. Work admitted while an unresolved pending
// action still gates the session (and that admission is not the matching
// resolution) stays durably queued but leaves the session idle with no
// session.status_running.
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
	// A resolution event (user.custom_tool_result / user.tool_confirmation) must
	// reference a currently OPEN pending action of the matching kind. Validate and
	// claim each in the same transaction that commits it, so an unknown,
	// already-resolved, duplicate, wrong-session, or wrong-kind reference fails
	// atomically without ever creating runnable work. The referenced action event
	// id comes from the event's own type-specific payload; the resolution kind is
	// derived from the event type, never trusted from an arbitrary caller string.
	// We also note whether this admission claimed any pending action: a matching
	// resolution is what allows the session to reopen to running below, whereas
	// ordinary work admitted while a park is still open stays idle and gated.
	admittedResolution := false
	for _, event := range events {
		actionEventID, kind, ok := domain.ResolutionReference(event.Type, event.Payload)
		if !ok {
			continue
		}
		if err := claimPendingActionTx(ctx, tx, session.ID, actionEventID, kind, event.ID); err != nil {
			return Admission{}, err
		}
		admittedResolution = true
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

	// Reopen the session to running only when the newly admitted work is actually
	// claimable now. If the session is idle with an unresolved pending action and
	// this admission did NOT claim a matching resolution, the new run is durably
	// queued but gated (ClaimNext will not claim it), so the session must stay idle
	// and emit no session.status_running — otherwise the projection would lie
	// (running while really waiting for the action). A matching resolution, or work
	// admitted with no open pending action, proceeds to running as before.
	reopen := session.Status != domain.StatusRunning
	if reopen && session.Status == domain.StatusIdle && !admittedResolution {
		gated, err := hasUnresolvedPendingTx(ctx, tx, session.ID)
		if err != nil {
			return Admission{}, err
		}
		if gated {
			reopen = false
		}
	}

	if reopen {
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

	// One durable queued run per trigger, in admission order. Grouping multiple
	// triggers into a single run would let a later trigger project history
	// before the earlier trigger's agent output was committed.
	for _, triggerID := range triggerIDs {
		run, err := s.insertRunTx(ctx, tx, session.ID, triggerID)
		if err != nil {
			return Admission{}, err
		}
		admission.Runs = append(admission.Runs, run)
	}
	return admission, nil
}

func (s *RunStore) insertRunTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	triggerID string,
) (domain.SessionRun, error) {
	triggerIDs := []string{triggerID}
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
		TriggerEventIDs: triggerIDs,
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

	// A terminated session is final: never claim its leftover queued work and
	// never flip it back to running. Guard before selecting a run so a session
	// that terminated with runs still queued stays terminated.
	session, err := getSessionTx(ctx, tx, sessionID)
	if err != nil {
		// getSessionTx already maps a missing row to domain.NotFound; propagate
		// the truthful error rather than silently reporting "nothing to claim".
		return RunClaim{}, false, err
	}
	if session.Status == domain.StatusTerminated {
		return RunClaim{}, false, nil
	}

	// Claim gate: while the session has unresolved pending actions, ordinary
	// queued runs must not be claimed even if they were admitted before the run
	// parked. Only a run whose trigger is the matching resolution (its
	// resolving_event_id, recorded at admission) may bypass those earlier queued
	// runs. This keeps at most one running run and deterministic admission-order
	// selection within whichever set is claimable.
	pending, err := unresolvedPendingActions(ctx, tx, sessionID)
	if err != nil {
		return RunClaim{}, false, err
	}
	var run domain.SessionRun
	if len(pending) > 0 {
		resolving := make(map[string]struct{}, len(pending))
		for _, p := range pending {
			if p.resolvingEventID != nil {
				resolving[*p.resolvingEventID] = struct{}{}
			}
		}
		run, err = selectNextResolutionRun(ctx, tx, sessionID, resolving)
	} else {
		run, err = selectNextQueuedRun(ctx, tx, sessionID)
	}
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
// processed, updates the session projection, and closes the durable run. When
// the run parked (pendingActionEventIDs is non-empty) it also persists one
// durable pending action per action event in the SAME transaction, deriving each
// expected response kind from the committed action event's type. When this run's
// triggers resolved earlier parked actions, those pending actions are marked
// resolved in this same transaction, so the ordinary queued work a park blocked
// becomes claimable only after the resume run has closed.
func (s *RunStore) Complete(
	ctx context.Context,
	runID string,
	drafts []domain.EventDraft,
	status domain.Status,
	runError *string,
	pendingActionEventIDs []string,
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

	// Resolve any parked action this run's triggers answered. The resume run's
	// trigger (a user.custom_tool_result / user.tool_confirmation admitted with a
	// recorded resolving_event_id) clears the gate here, in the same transaction
	// that closes the run, so ordinary queued work only continues after a
	// successful resume commit. A failed resume leaves resolved_at set too (the
	// park is answered honestly), but a terminated session never resurrects.
	if _, err := resolvePendingActionsForTriggers(ctx, tx, s.clock, run.SessionID, run.TriggerEventIDs); err != nil {
		return RunCompletion{}, err
	}

	// Persist the run's parked actions as durable pending actions. Each id must be
	// one of the action events THIS Complete call just committed above — validated
	// against the committed drafts, never mere session-local existence, so an old
	// action event from an earlier run, a phantom id, or any non-action output is
	// rejected and rolls the whole transaction back. The allowed set is built from
	// appendEventsTx's return BEFORE any later synthetic status_running append, so
	// only genuine drafts of this run can park. The kind is derived from the
	// committed event's type AND payload (see insertPendingActionTx). This is the
	// same transaction as the action events, the status_idle{requires_action}, the
	// session projection, and the run completion.
	allowedActions := make(map[string]domain.Event, len(events))
	for _, event := range events {
		allowedActions[event.ID] = event
	}
	// Duplicate ids in one park would silently collapse to a single gate via the
	// ON CONFLICT no-op, hiding a caller mistake behind misleading success. Reject
	// them explicitly so a park names each action event exactly once.
	seenActions := make(map[string]struct{}, len(pendingActionEventIDs))
	for _, actionEventID := range pendingActionEventIDs {
		if _, dup := seenActions[actionEventID]; dup {
			return RunCompletion{}, domain.Validation("duplicate pending action event id")
		}
		seenActions[actionEventID] = struct{}{}
		if err := insertPendingActionTx(ctx, tx, s.ids, s.clock, run.SessionID, actionEventID, allowedActions); err != nil {
			return RunCompletion{}, err
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
	// Ordinary queued runs are gated while any pending action is unresolved: a run
	// that just parked (or a resume that answered one park while another remains
	// open) must NOT reopen the session to running for work that cannot yet be
	// claimed. Only reopen when queued work exists AND nothing gates it.
	gated, err := hasUnresolvedPendingTx(ctx, tx, run.SessionID)
	if err != nil {
		return RunCompletion{}, err
	}
	// A terminated session is final: even if later runs were queued before the
	// failure, we do not resurrect it to running. Only a non-terminal completion
	// with more claimable queued work reopens the session to running.
	if hasQueued && !gated && status != domain.StatusRunning && status != domain.StatusTerminated {
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
	// Persist the exact committed output event ids on the run in the same
	// transaction that closes it. These are precisely the events this run
	// appended above (agent output plus the run's terminal/status events), in
	// commit order. Writing them here — not in a follow-up statement — keeps the
	// invariant that there is never a completed run without its output
	// association. ModelHistory later replays these ids to rebuild causal history.
	outputIDs := make([]string, len(events))
	for i, event := range events {
		outputIDs[i] = event.ID
	}
	run.OutputEventIDs = outputIDs
	outputJSON, err := json.Marshal(outputIDs)
	if err != nil {
		return RunCompletion{}, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE session_runs
SET state=?, error=?, output_event_ids=?, updated_at=?
WHERE id=? AND state=?`,
		string(run.State), nullableString(run.Error), string(outputJSON), timeVal(run.UpdatedAt),
		run.ID, string(domain.RunRunning))
	if err != nil {
		return RunCompletion{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunCompletion{}, err
	}
	return RunCompletion{Run: run, Session: session, Events: events}, nil
}

// ModelHistory reconstructs the causal conversation history for a claimed/current
// run, to be projected into the model. Public event ordering (History/List/the
// live stream) is authoritative receipt/commit order and is deliberately NOT
// what a turn should replay: a later queued trigger admitted before an earlier
// run finished must not appear in that earlier turn's projection. This method
// rebuilds history from run causality instead of raw sequence:
//
//   - It walks the prior completed/failed runs for the same session in admission
//     order and, for each, appends that run's trigger events followed by that
//     run's persisted output events (the exact events it committed on
//     completion). This interleaves user turn / agent reply in the causal order
//     they actually resolved.
//   - It then appends the current run's own trigger events.
//   - Every later queued trigger (admission_seq greater than this run's, or any
//     run not yet completed/failed) is excluded, because only prior terminal runs
//     and this run's trigger are visited.
//
// The reconstructed history is finally bounded to the newest `limit` events (the
// historyProjectionLimit-equivalent), preserving chronological causal order. A
// window that cuts a tool_use/tool_result pair is left for ProjectMessages'
// existing dangling/orphan filtering to repair.
func (s *RunStore) ModelHistory(
	ctx context.Context,
	run domain.SessionRun,
	limit int,
) ([]domain.Event, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
SELECT id
FROM session_runs
WHERE session_id=? AND admission_seq<? AND state IN (?, ?)
ORDER BY admission_seq`,
		run.SessionID, run.AdmissionSeq, string(domain.RunCompleted), string(domain.RunFailed))
	if err != nil {
		return nil, err
	}
	var priorRunIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		priorRunIDs = append(priorRunIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// Build the ordered event-id list: each prior terminal run contributes its
	// trigger events then its committed output events, then the current run's
	// trigger closes the causal chain.
	var orderedIDs []string
	for _, runID := range priorRunIDs {
		prior, err := getRunTx(ctx, tx, runID)
		if err != nil {
			return nil, err
		}
		orderedIDs = append(orderedIDs, prior.TriggerEventIDs...)
		orderedIDs = append(orderedIDs, prior.OutputEventIDs...)
	}
	orderedIDs = append(orderedIDs, run.TriggerEventIDs...)

	// Bound to the newest `limit` events, keeping causal order. A cut pair is
	// tolerated by ProjectMessages' dangling/orphan filtering.
	if limit > 0 && len(orderedIDs) > limit {
		orderedIDs = orderedIDs[len(orderedIDs)-limit:]
	}
	return loadEventsByIDTx(ctx, tx, run.SessionID, orderedIDs)
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
		outputJSON              string
		state, created, updated string
		runError                sql.NullString
	)
	err := q.QueryRowContext(ctx, `
SELECT id, session_id, admission_seq, trigger_event_ids, output_event_ids, state, error, created_at, updated_at
FROM session_runs
WHERE id=?`, id).Scan(
		&run.ID, &run.SessionID, &run.AdmissionSeq, &triggerJSON, &outputJSON, &state,
		&runError, &created, &updated)
	if err != nil {
		return domain.SessionRun{}, err
	}
	if err := json.Unmarshal([]byte(triggerJSON), &run.TriggerEventIDs); err != nil {
		return domain.SessionRun{}, fmt.Errorf("store: decode run triggers: %w", err)
	}
	if err := json.Unmarshal([]byte(outputJSON), &run.OutputEventIDs); err != nil {
		return domain.SessionRun{}, fmt.Errorf("store: decode run outputs: %w", err)
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

// selectNextResolutionRun picks the oldest queued run (admission order) whose
// trigger is one of the given resolving event ids, honoring the same
// at-most-one-running guard as selectNextQueuedRun. It is the gate's bypass: while
// unresolved pending actions exist, only such a resume run is claimable; earlier
// ordinary queued runs are skipped until the park is resolved. Returns
// sql.ErrNoRows when no matching resume run is queued yet (the session stays
// idle, waiting for the client's response).
func selectNextResolutionRun(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	resolving map[string]struct{},
) (domain.SessionRun, error) {
	var running bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM session_runs WHERE session_id=? AND state=?)`,
		sessionID, string(domain.RunRunning)).Scan(&running); err != nil {
		return domain.SessionRun{}, err
	}
	if running {
		return domain.SessionRun{}, sql.ErrNoRows
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, trigger_event_ids
FROM session_runs
WHERE session_id=? AND state=?
ORDER BY admission_seq`, sessionID, string(domain.RunQueued))
	if err != nil {
		return domain.SessionRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, triggerJSON string
		if err := rows.Scan(&id, &triggerJSON); err != nil {
			return domain.SessionRun{}, err
		}
		var triggerIDs []string
		if err := json.Unmarshal([]byte(triggerJSON), &triggerIDs); err != nil {
			return domain.SessionRun{}, err
		}
		for _, triggerID := range triggerIDs {
			if _, ok := resolving[triggerID]; ok {
				if err := rows.Err(); err != nil {
					return domain.SessionRun{}, err
				}
				rows.Close()
				return getRunTx(ctx, tx, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.SessionRun{}, err
	}
	return domain.SessionRun{}, sql.ErrNoRows
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
