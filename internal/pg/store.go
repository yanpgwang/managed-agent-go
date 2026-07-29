package pg

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

// Store is the primary PostgreSQL control-plane store. It owns the event-admission
// transaction, cursor reads for the SessionWorkflow, idempotent turn
// completion, and the coalescible orchestration outbox.
//
// PostgreSQL — not Temporal — is the source of truth for public events and the
// session projection.
type Store struct {
	pool     *pgxpool.Pool
	q        *pgstore.Queries
	ids      domain.IDGenerator
	clock    domain.Clock
	notifier EventNotifier
}

func NewStore(pool *pgxpool.Pool, ids domain.IDGenerator, clock domain.Clock) *Store {
	return &Store{pool: pool, q: pgstore.New(pool), ids: ids, clock: clock}
}

// EventNotifier wakes live-event subscribers after a PostgreSQL commit. It is a
// latency optimization only: subscribers always reconcile from the durable
// event ledger and correctness never depends on notification delivery.
type EventNotifier interface {
	NotifySession(context.Context, string) error
}

// SetEventNotifier installs the process-local live notification publisher
// during startup, before the Store is shared with request handlers or workers.
func (s *Store) SetEventNotifier(notifier EventNotifier) {
	s.notifier = notifier
}

func (s *Store) notifySession(ctx context.Context, sessionID string) {
	if s.notifier == nil {
		return
	}
	if err := s.notifier.NotifySession(ctx, sessionID); err != nil {
		log.Printf(
			"pg: live event notification failed session_id=%s (ledger remains authoritative): %v",
			sessionID,
			err,
		)
	}
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
	return s.createSession(ctx, session, drafts, false)
}

// CreateAPISession creates a public API session while holding share locks on
// its exact Agent version and Environment. FOR SHARE conflicts with both
// archival's non-key UPDATE and Environment deletion, so the dependency checks
// and session insert linearize with those lifecycle operations.
func (s *Store) CreateAPISession(
	ctx context.Context,
	session domain.Session,
	drafts []domain.EventDraft,
) (Admission, error) {
	return s.createSession(ctx, session, drafts, true)
}

func (s *Store) createSession(
	ctx context.Context,
	session domain.Session,
	drafts []domain.EventDraft,
	checkDependencies bool,
) (Admission, error) {
	// PostgreSQL timestamptz has microsecond precision. Normalize the JSON
	// projection to the same value as the relational key so a list cursor never
	// compares a nanosecond boundary against a truncated database timestamp.
	session.CreatedAt = session.CreatedAt.UTC().Truncate(time.Microsecond)
	session.UpdatedAt = session.UpdatedAt.UTC().Truncate(time.Microsecond)
	if session.ArchivedAt != nil {
		archivedAt := session.ArchivedAt.UTC().Truncate(time.Microsecond)
		session.ArchivedAt = &archivedAt
	}
	body, err := json.Marshal(session)
	if err != nil {
		return Admission{}, err
	}
	var admission Admission
	err = s.withTx(ctx, func(q *pgstore.Queries) error {
		if checkDependencies {
			if _, err := q.LockActiveAgentVersion(ctx, pgstore.LockActiveAgentVersionParams{
				ID: session.AgentID, Version: int32(session.AgentVersion),
			}); errors.Is(err, pgx.ErrNoRows) {
				return domain.Validation("agent is missing or archived")
			} else if err != nil {
				return err
			}
			if _, err := q.LockActiveEnvironment(ctx, session.EnvironmentID); errors.Is(err, pgx.ErrNoRows) {
				return domain.Validation("environment is missing or archived")
			} else if err != nil {
				return err
			}
		}
		if err := q.InsertSession(ctx, insertSessionParams(session, body)); err != nil {
			return err
		}
		var innerErr error
		admission, innerErr = s.admitLocked(ctx, q, session, drafts)
		return innerErr
	})
	if err != nil {
		return Admission{}, err
	}
	s.notifySession(ctx, session.ID)
	return admission, nil
}

func insertSessionParams(session domain.Session, body []byte) pgstore.InsertSessionParams {
	return pgstore.InsertSessionParams{
		ID:            session.ID,
		Status:        string(session.Status),
		Body:          body,
		CreatedAt:     tsUTC(session.CreatedAt),
		UpdatedAt:     tsUTC(session.UpdatedAt),
		AgentID:       stringPtr(session.AgentID),
		AgentVersion:  int32Ptr(session.AgentVersion),
		EnvironmentID: stringPtr(session.EnvironmentID),
		ArchivedAt:    tsPtr(session.ArchivedAt),
	}
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
		if row.DeletingAt.Valid {
			return domain.Conflict("session deletion is in progress")
		}
		session, err := sessionFromLockRow(row)
		if err != nil {
			return err
		}
		if session.ArchivedAt != nil {
			return domain.Conflict("cannot send events to an archived session")
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
	s.notifySession(ctx, sessionID)
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

	// Claim matching client-action resolutions in the same transaction that
	// commits them. The row remains unresolved until the resume turn closes, so
	// ordinary queued messages cannot overtake an in-flight resolution.
	admittedResolution, err := s.claimPendingResolutionsLocked(
		ctx,
		q,
		session.ID,
		events,
	)
	if err != nil {
		return Admission{}, err
	}

	hasMessage := false
	for _, event := range events {
		if event.Type == domain.EvUserMessage {
			hasMessage = true
			break
		}
	}

	// Ordinary work may be admitted while a requires_action wait is open, but it
	// is not runnable yet: keep the session idle, emit no status_running, and
	// write no wakeup for work the Workflow must not consume. A partial resolution
	// is also only a durable claim: the official barrier reopens once every
	// blocking action has a result, not once per result.
	gated, err := q.HasUnresolvedPendingActions(ctx, session.ID)
	if err != nil {
		return Admission{}, err
	}
	hasUnclaimed := false
	if gated {
		hasUnclaimed, err = q.HasUnclaimedPendingActions(ctx, session.ID)
		if err != nil {
			return Admission{}, err
		}
	}
	resolutionBarrierReady := admittedResolution && gated && !hasUnclaimed
	hasTrigger := hasMessage || resolutionBarrierReady

	admission := Admission{Session: session, Events: events, MaxSeq: maxSeq}
	if !hasTrigger {
		if err := s.putProjection(ctx, q, session); err != nil {
			return Admission{}, err
		}
		return admission, nil
	}

	runnable := !gated || resolutionBarrierReady
	if !runnable {
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
	return sessionFromGetRow(row)
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

// HistoryThrough reconstructs the causal conversation history for the turn
// triggered by triggerEventID, to be projected into the model. It mirrors the
// SQLite path's RunStore.ModelHistory: public receipt order is deliberately NOT
// what a turn replays, because a later user.message admitted (at a lower
// sequence) before an earlier turn finished must not appear as a peer of the
// current trigger.
//
// The reconstruction walks the prior *processed* user.message triggers in
// receipt order and, for each, appends that trigger followed by the exact output
// events it committed (identified by turn_event_id). It then appends the current
// trigger. This interleaves user turn / agent reply in causal order, so a batch
// A,B projects as [A, agent(A), B] rather than collapsing A and B into two
// consecutive user turns.
//
// The result is bounded to the newest `limit` events, preserving causal order —
// an over-limit session carries its most recent context, not the oldest. A
// window that cuts a tool_use/tool_result pair is left to ProjectMessages'
// existing dangling/orphan repair.
func (s *Store) HistoryThrough(ctx context.Context, sessionID, triggerEventID string, limit int) ([]domain.Event, error) {
	trigger, err := s.GetEvent(ctx, sessionID, triggerEventID)
	if err != nil {
		return nil, err
	}

	priorRows, err := s.q.PriorProcessedUserTriggers(ctx, pgstore.PriorProcessedUserTriggersParams{
		SessionID: sessionID,
		BeforeSeq: trigger.Sequence,
	})
	if err != nil {
		return nil, err
	}
	priors, err := eventsFromRows(priorRows)
	if err != nil {
		return nil, err
	}

	var ordered []domain.Event
	for _, prior := range priors {
		ordered = append(ordered, prior)
		id := prior.ID
		outRows, err := s.q.ListEventsByTurn(ctx, pgstore.ListEventsByTurnParams{
			SessionID:   sessionID,
			TurnEventID: &id,
		})
		if err != nil {
			return nil, err
		}
		outputs, err := eventsFromRows(outRows)
		if err != nil {
			return nil, err
		}
		ordered = append(ordered, outputs...)
	}
	ordered = append(ordered, trigger)

	// Bound to the newest `limit` events, keeping causal order.
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered, nil
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
	return s.completeTurn(ctx, sessionID, triggerEventID, outputDrafts, status, "", "", nil, nil, nil)
}

// CompleteWorkflowTurn atomically finalizes a Workflow-owned tool attempt (when
// present), commits the public turn output, and persists any client-action wait
// rows. Keeping those mutations in one PostgreSQL transaction closes the crash
// windows between "attempt completed", "requires_action published", "wait
// durable", and "trigger processed"; a retried Activity either applies the
// whole transition or observes the already-processed trigger.
func (s *Store) CompleteWorkflowTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	outputDrafts []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
) (TurnCompletion, error) {
	if attemptID == "" {
		if attemptState != "" || attemptError != nil {
			return TurnCompletion{}, domain.Validation("attempt state requires an attempt id")
		}
	} else if err := validateAttemptFinish(attemptState, attemptError); err != nil {
		return TurnCompletion{}, err
	}
	return s.completeTurn(
		ctx,
		sessionID,
		triggerEventID,
		outputDrafts,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
	)
}

func (s *Store) completeTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	outputDrafts []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
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
		if row.DeletingAt.Valid {
			return domain.Conflict("session deletion is in progress")
		}
		session, err := sessionFromLockRow(row)
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
		// Idempotent replay: a trigger already stamped processed normally means
		// this turn's completion committed. A claimed pending-action resolution is
		// the exception: user.custom_tool_result is processed on receipt by public
		// contract, while its resume turn still has to close the pending row. That
		// row remains unresolved until this transaction succeeds and therefore
		// disambiguates admission processing from turn completion.
		if trigger.ProcessedAt.Valid {
			pendingResume, err := q.IsUnresolvedPendingResolution(
				ctx,
				pgstore.IsUnresolvedPendingResolutionParams{
					SessionID:        sessionID,
					ResolvingEventID: &triggerEventID,
				},
			)
			if err != nil {
				return err
			}
			if !pendingResume {
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
		}

		if err := validatePendingCompletion(
			status,
			outputDrafts,
			pendingActionEventIDs,
		); err != nil {
			return err
		}
		if attemptID != "" {
			if err := s.finishAttemptLocked(
				ctx,
				q,
				attemptID,
				attemptState,
				attemptError,
				sessionID,
				triggerEventID,
			); err != nil {
				return err
			}
		}

		// Resolutions keep their pending rows unresolved while the resume turn is
		// in flight. Clear the complete barrier only inside this completion
		// transaction; if it rolls back, ordinary queued work remains gated.
		resolvedPending, err := s.resolvePendingBarrierLocked(
			ctx,
			q,
			sessionID,
			triggerEventID,
			resolutionEventIDs,
		)
		if err != nil {
			return err
		}
		hasUnresolved, err := q.HasUnresolvedPendingActions(ctx, sessionID)
		if err != nil {
			return err
		}
		gatedAfterCompletion := hasUnresolved || len(pendingActionEventIDs) > 0

		maxSeq, err := q.MaxEventSeq(ctx, sessionID)
		if err != nil {
			return err
		}

		// Intermediate-idle suppression: when this is an ordinary end_turn
		// completion (status idle) but more user.message triggers are still
		// unprocessed — e.g. a batch A,B where A finishes while B is queued — the
		// session must NOT flip to idle between turns. Doing so would emit a
		// spurious public session.status_idle and momentarily lie about the session
		// being done. Keep it running and drop the terminal idle draft; only the
		// last turn's completion (no remaining unprocessed user.message) idles the
		// session. A terminated status is never softened, and a non-idle status
		// (e.g. still running by the caller's choice) is left as-is.
		effectiveStatus := status
		drafts := outputDrafts
		var remaining int32
		if status == domain.StatusIdle && !gatedAfterCompletion {
			remaining, err = q.CountUnprocessedUserMessages(ctx, pgstore.CountUnprocessedUserMessagesParams{
				SessionID: sessionID,
				ExcludeID: triggerEventID,
			})
			if err != nil {
				return err
			}
			if remaining > 0 {
				effectiveStatus = domain.StatusRunning
				drafts = withoutTerminalIdle(outputDrafts)
			}
		}

		events, finalMaxSeq, err := s.appendDrafts(
			ctx,
			q,
			sessionID,
			drafts,
			maxSeq,
			&triggerEventID,
		)
		if err != nil {
			return err
		}
		allowedActions := make(map[string]domain.Event, len(events))
		for _, event := range events {
			allowedActions[event.ID] = event
		}
		if err := s.insertPendingActionsLocked(
			ctx,
			q,
			sessionID,
			pendingActionEventIDs,
			allowedActions,
		); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		processedIDs := resolutionEventIDs
		if len(processedIDs) == 0 {
			processedIDs = []string{triggerEventID}
		}
		for _, eventID := range processedIDs {
			if err := q.MarkEventProcessed(ctx, pgstore.MarkEventProcessedParams{
				ProcessedAt: tsUTC(now),
				SessionID:   sessionID,
				ID:          eventID,
			}); err != nil {
				return err
			}
		}
		session.Status = effectiveStatus
		session.UpdatedAt = now
		if err := s.putProjection(ctx, q, session); err != nil {
			return err
		}
		// Messages admitted while the barrier was open intentionally wrote no
		// wakeup. When this transaction clears the last row and exposes queued
		// ordinary work, enqueue a fresh durable wakeup in the same commit. Without
		// it a message racing after the current Workflow drain could leave a
		// truthful running projection with no future signal.
		if resolvedPending &&
			!gatedAfterCompletion &&
			effectiveStatus == domain.StatusRunning &&
			remaining > 0 {
			if err := q.UpsertOutbox(ctx, pgstore.UpsertOutboxParams{
				SessionID:   sessionID,
				MaxEventSeq: finalMaxSeq,
				EnqueuedAt:  tsUTC(now),
			}); err != nil {
				return err
			}
		}
		result = TurnCompletion{Session: session, Events: events, Applied: true}
		return nil
	})
	if err != nil {
		return TurnCompletion{}, err
	}
	s.notifySession(ctx, sessionID)
	return result, nil
}

// withoutTerminalIdle returns drafts with any session.status_idle draft removed,
// keeping every other draft in order. Used when an intermediate turn must not
// publish an idle event because later user.message work is still queued.
func withoutTerminalIdle(drafts []domain.EventDraft) []domain.EventDraft {
	out := make([]domain.EventDraft, 0, len(drafts))
	for _, d := range drafts {
		if d.Type == domain.EvSessionStatusIdle {
			continue
		}
		out = append(out, d)
	}
	return out
}
