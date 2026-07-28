package app

import (
	"context"
	"hash/maphash"
	"log"
	"sync"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

type CreateSessionInput struct {
	AgentID       string
	AgentVersion  *int // nil -> latest; set -> pinned version
	Overrides     *domain.AgentOverrides
	EnvironmentID string
	Title         string
	Metadata      map[string]any
	InitialEvents []domain.EventDraft
}

type ListPage struct {
	AgentID         string
	AgentVersion    *int
	CreatedAtGt     *time.Time
	CreatedAtGte    *time.Time
	CreatedAtLt     *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	Statuses        []domain.Status
	DeploymentID    *string
	MemoryStoreID   *string
	Boundary        *SessionPageBoundary
	Limit           int
	Desc            bool
}

type SessionPageBoundary struct {
	CreatedAt time.Time
	ID        string
	Backward  bool
}

type SessionListPage struct {
	Sessions []domain.Session
	HasPrev  bool
	HasNext  bool
}

const sessionLockShardCount = 256

// historyProjectionLimit bounds how many events are replayed into the model
// conversation per turn. Projection uses the NEWEST window of this size (see
// RunStore.ModelHistory): an over-limit session carries its most recent causal
// context rather than the oldest events. Compaction is a later slice; until
// then this is a generous ceiling that keeps a single unbounded session from
// OOMing a turn.
const historyProjectionLimit = 10000

// sandboxReleaseTimeout bounds the detached sandbox teardown performed during
// session deletion. The durable delete has already committed before teardown
// runs, so teardown executes on a context detached from the request's
// cancellation; this ceiling keeps that detached cleanup finite instead of
// letting a stuck provider Destroy block forever.
const sandboxReleaseTimeout = 30 * time.Second

type SessionService struct {
	sess    *store.SessionRepo
	agents  *store.AgentRepo
	envs    *store.EnvironmentRepo
	events  *EventService
	runs    *store.RunStore
	rt      agentruntime.AgentRuntime
	sandbox *sandbox.SessionManager
	ids     domain.IDGenerator
	clock   domain.Clock

	lockSeed   maphash.Seed
	lockShards [sessionLockShardCount]sync.Mutex

	// cancelers holds the cancel function of the single active run per session so
	// a durably admitted user.interrupt can cancel it mid-execution. See
	// interrupt.go for the ordering contract with the shard locks above.
	cancelers *runCancelers
}

func NewSessionService(sess *store.SessionRepo, agents *store.AgentRepo, envs *store.EnvironmentRepo,
	events *EventService, runs *store.RunStore, rt agentruntime.AgentRuntime,
	sandboxProvider sandbox.Provider, ids domain.IDGenerator, clock domain.Clock,
) *SessionService {
	return &SessionService{sess: sess, agents: agents, envs: envs, events: events, runs: runs, rt: rt,
		sandbox: sandbox.NewSessionManager(sandboxProvider), ids: ids, clock: clock, lockSeed: maphash.MakeSeed(),
		cancelers: newRunCancelers()}
}

func (s *SessionService) lockFor(id string) *sync.Mutex {
	index := maphash.String(s.lockSeed, id) % uint64(len(s.lockShards))
	return &s.lockShards[index]
}

func (s *SessionService) Create(ctx context.Context, in CreateSessionInput) (domain.Session, error) {
	if err := validateMetadata(in.Metadata); err != nil {
		return domain.Session{}, err
	}
	var ag domain.Agent
	var err error
	if in.AgentVersion != nil {
		ag, err = s.agents.GetVersion(ctx, in.AgentID, *in.AgentVersion)
	} else {
		ag, err = s.agents.Latest(ctx, in.AgentID)
	}
	if err != nil {
		return domain.Session{}, domain.Validation("agent not found")
	}
	if ag.ArchivedAt != nil {
		return domain.Session{}, domain.Validation("agent is archived")
	}
	env, err := s.envs.Get(ctx, in.EnvironmentID)
	if err != nil {
		return domain.Session{}, domain.Validation("environment not found")
	}
	if env.ArchivedAt != nil {
		return domain.Session{}, domain.Validation("environment is archived")
	}
	if env.ConfigType == "self_hosted" {
		return domain.Session{}, domain.Unsupported("sessions against self_hosted environments are not supported in v0.1")
	}
	if len(in.InitialEvents) > 50 {
		return domain.Session{}, domain.Validation("initial_events exceeds 50")
	}
	for _, e := range in.InitialEvents {
		if !domain.IsInitialEventType(e.Type) {
			return domain.Session{}, domain.Validation("initial_events may contain only user.message or user.define_outcome")
		}
	}
	// Resolve the immutable snapshot: pinned/latest version plus any per-session
	// overrides. The snapshot keeps the base agent's id and version.
	snapshot := ag
	if in.Overrides != nil {
		snapshot = ag.WithOverrides(*in.Overrides)
	}
	metadata := in.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	now := s.clock.Now().UTC()
	sess := domain.Session{
		ID:            s.ids.NewID(domain.PrefixSession),
		AgentID:       ag.ID,
		AgentVersion:  ag.Version,
		EnvironmentID: env.ID,
		Status:        domain.StatusIdle,
		Title:         in.Title,
		Metadata:      metadata,
		AgentSnapshot: snapshot,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if len(in.InitialEvents) == 0 {
		if err := s.sess.CreateIfDependenciesActive(ctx, sess); err != nil {
			return domain.Session{}, err
		}
		return sess, nil
	}
	admission, err := s.runs.CreateSession(ctx, sess, in.InitialEvents)
	if err != nil {
		return domain.Session{}, err
	}
	s.events.PublishCommitted(admission.Events)
	if len(admission.Runs) > 0 {
		s.kick(sess.ID)
	}
	return admission.Session, nil
}

func (s *SessionService) Get(ctx context.Context, id string) (domain.Session, error) {
	return s.sess.Get(ctx, id)
}

func (s *SessionService) List(ctx context.Context, p ListPage) (SessionListPage, error) {
	query := store.SessionListQuery{
		AgentID:              p.AgentID,
		AgentVersion:         p.AgentVersion,
		CreatedAtGt:          p.CreatedAtGt,
		CreatedAtGte:         p.CreatedAtGte,
		CreatedAtLt:          p.CreatedAtLt,
		CreatedAtLte:         p.CreatedAtLte,
		IncludeArchived:      p.IncludeArchived,
		Statuses:             p.Statuses,
		HasDeploymentFilter:  p.DeploymentID != nil,
		HasMemoryStoreFilter: p.MemoryStoreID != nil,
		Limit:                p.Limit,
		Desc:                 p.Desc,
	}
	if p.Boundary != nil {
		query.Boundary = &store.SessionListBoundary{
			CreatedAt: p.Boundary.CreatedAt,
			ID:        p.Boundary.ID,
			Backward:  p.Boundary.Backward,
		}
	}
	result, err := s.sess.List(ctx, query)
	if err != nil {
		return SessionListPage{}, err
	}
	return SessionListPage{
		Sessions: result.Sessions,
		HasPrev:  result.HasBefore,
		HasNext:  result.HasAfter,
	}, nil
}

func (s *SessionService) SendEvent(ctx context.Context, id string, drafts []domain.EventDraft) ([]domain.Event, error) {
	lock := s.lockFor(id)
	lock.Lock()
	admission, err := s.runs.Admit(ctx, id, drafts)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	// Publish the admitted events to subscribers BEFORE firing the cancel, while
	// still holding the shard lock. Hub publication is nonblocking, and every
	// same-session commit (Admit here, ClaimNext/Complete in drainRuns) publishes
	// under this same lock in commit order. Publishing the interrupt admission
	// before the cancel — and before releasing the lock — guarantees the
	// user.interrupt event reaches subscribers ahead of any later-sequence output
	// the canceled drain commits when it next takes the lock. Publishing after the
	// unlock (as before) left a window where the canceled drain could acquire the
	// lock, complete, and publish its higher-sequence output before this admission
	// was ever delivered, reordering the live stream relative to durable sequence.
	s.events.PublishCommitted(admission.Events)
	// The interrupt is durably admitted the moment Admit's transaction committed
	// above. Only now — never before durable admission — cancel the session's
	// active run, and do it while still holding the shard lock so this cancel is
	// serialized against drainRuns' claim+register under the same lock: either the
	// active run's canceler is already registered (we cancel it) or the run has not
	// yet been claimed (a no-op, and drainRuns will observe the interrupt through
	// normal claim ordering). This is what stops a running session from missing the
	// interrupt. Cancel is scoped to this session id and is a safe no-op when no run
	// is active (interrupt while idle). Repeated cancellation is idempotent.
	if containsInterrupt(admission.Events) {
		s.cancelers.cancel(id, errInterrupted)
	}
	lock.Unlock()

	if len(admission.Runs) > 0 {
		s.kick(id)
	}
	// Send Events echoes only the caller-submitted events, not the status event
	// emitted by the same admission transaction.
	return admission.Events[:len(drafts)], nil
}

// containsInterrupt reports whether an admitted batch carried a user.interrupt.
func containsInterrupt(events []domain.Event) bool {
	for _, e := range events {
		if e.Type == domain.EvUserInterrupt {
			return true
		}
	}
	return false
}

func (s *SessionService) UpdateTitle(ctx context.Context, id, title string) (domain.Session, error) {
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	sess, events, err := s.runs.UpdateTitle(ctx, id, title)
	if err != nil {
		return domain.Session{}, err
	}
	s.events.PublishCommitted(events)
	return sess, nil
}

func (s *SessionService) Archive(ctx context.Context, id string) (domain.Session, error) {
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()

	sess, err := s.sess.Get(ctx, id)
	if err != nil {
		return domain.Session{}, err
	}
	if sess.Status == domain.StatusRunning {
		return domain.Session{}, domain.Conflict("cannot archive a running session; interrupt first")
	}
	now := s.clock.Now().UTC()
	sess.ArchivedAt = &now
	sess.UpdatedAt = now
	return sess, s.sess.Put(ctx, sess)
}

func (s *SessionService) Delete(ctx context.Context, id string) error {
	lock := s.lockFor(id)
	lock.Lock()

	sess, err := s.sess.Get(ctx, id)
	if err != nil {
		lock.Unlock()
		return err
	}
	if sess.Status == domain.StatusRunning {
		lock.Unlock()
		return domain.Conflict("cannot delete a running session; interrupt first")
	}
	if err := s.sess.Delete(ctx, id); err != nil {
		lock.Unlock()
		return err
	}
	// The durable delete is committed under the shard lock. Drop the lock before
	// the external provider teardown and stream close: sandbox Destroy is an
	// out-of-process call (a Docker container or host temp dir) that must not
	// hold the shard — and thus stall every other session hashing to it — for
	// its whole duration. Each path above unlocks exactly once, so there is no
	// double unlock.
	lock.Unlock()

	// Permanently clean up the session's logical sandbox. Release is idempotent
	// and a no-op when the session never provisioned one; when it did, it runs
	// the provider teardown exactly once. This is an external call made after the
	// durable delete, outside the store transaction and outside the shard lock.
	//
	// Teardown must not inherit cancellation from the request context. The
	// durable delete has already committed, so if the caller cancels (client
	// disconnects, request deadline elapses) we still have to finish tearing the
	// sandbox down or leak a container / host temp dir. context.WithoutCancel
	// detaches from the request's cancellation while preserving its context
	// values (trace metadata, etc.); the timeout then bounds the detached
	// teardown so a stuck provider Destroy cannot hang deletion forever.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxReleaseTimeout)
	defer cancel()
	if err := s.sandbox.Release(cleanupCtx, id); err != nil {
		// A teardown failure can leak sandbox resources (a host temp dir or a
		// container). Log it; the session is already durably deleted.
		log.Printf("delete: sandbox release failed session_id=%s: %v", id, err)
	}

	now := s.clock.Now().UTC()
	s.events.CloseSession(id, domain.Event{
		ID:          s.ids.NewID(domain.PrefixEvent),
		SessionID:   id,
		Type:        domain.EvSessionDeleted,
		Payload:     map[string]any{},
		CreatedAt:   now,
		ProcessedAt: &now,
	})
	return nil
}

func (s *SessionService) kick(sessionID string) {
	go s.drainRuns(sessionID)
}

func (s *SessionService) drainRuns(sessionID string) {
	// Base context for durable store work (claim, history, sandbox acquire,
	// completion). It is deliberately NOT the runtime context: an interrupt
	// cancels only the runtime call below, while the completion commit that
	// records the interrupted run's buffered output must still run.
	ctx := context.Background()
	for {
		// Claim the next run and register its cancellation token atomically under
		// the session's shard lock. This is the other half of the ordering contract
		// with SendEvent (see interrupt.go and SendEvent): SendEvent admits an
		// interrupt and cancels the active run under the same lock, so once the
		// interrupt is durably admitted either this claim has already registered the
		// active run's canceler (SendEvent cancels it) or the run is not yet claimed
		// (SendEvent's cancel is a no-op and the interrupt is handled through normal
		// claim ordering). Claiming without the lock would open a window where a
		// just-claimed run has no registered canceler and would miss the interrupt.
		lock := s.lockFor(sessionID)
		lock.Lock()
		claim, ok, err := s.runs.ClaimNext(ctx, sessionID)
		if err != nil {
			lock.Unlock()
			// A claim failure ends the drain loop and leaves the run queued for a
			// later kick/restart-recovery. Log it so it does not vanish silently.
			log.Printf("drain: claim next run failed session_id=%s: %v", sessionID, err)
			return
		}
		if !ok {
			lock.Unlock()
			return
		}
		// runCtx derives from the base context and is canceled (with cause
		// errInterrupted) when a durably admitted user.interrupt targets this
		// session. Only the runtime call receives it, so cancellation propagates
		// through the model and tool calls while the completion commit stays on the
		// uncanceled base context.
		runCtx, cancelRun := context.WithCancelCause(ctx)
		tok := s.cancelers.register(sessionID, cancelRun)
		// Publish the claim's committed events (e.g. session.status_running) while
		// still holding the shard lock and after the canceler is registered, so the
		// live stream preserves durable commit order for this session: every
		// same-session commit publishes under this lock in commit order (admission in
		// SendEvent, this claim, and completion below). Registering the canceler first
		// keeps the interrupt-linearization contract intact; publishing before the
		// unlock closes the window where a concurrent SendEvent could otherwise
		// interleave its admission publish out of sequence order.
		s.events.PublishCommitted(claim.Events)
		lock.Unlock()

		runID := claim.Run.ID

		sink := newBufferedSink(s.ids, s.events, sessionID)
		var runErr error
		var outcome agentruntime.RunOutcome

		// Resolve the session's tool configuration from the immutable agent
		// snapshot. A parse failure here is a misconfigured session and is
		// surfaced as a session error via the terminate path below.
		toolSet, toolErr := domain.ParseTools(claim.AgentSnapshot.Tools)
		if toolErr != nil {
			runErr = toolErr
		}

		// Resolve the session's logical sandbox when the session has tools. The
		// sandbox is scoped to the session, not this run: the first run that needs
		// tools provisions it and later runs in the same session reuse the same
		// instance, so filesystem state a tool created in an earlier run is visible
		// now. Acquisition is an external call made outside any transaction or
		// process lock. A provisioning failure terminates the session like any
		// other unrecoverable runtime error. The sandbox is NOT destroyed when the
		// run ends; it is released only when the session is deleted (see Delete).
		var box sandbox.Sandbox
		if runErr == nil && toolSetHasTools(toolSet) {
			box, err = s.sandbox.Acquire(ctx, sessionID, sandbox.Spec{Timeout: 120 * time.Second})
			if err != nil {
				log.Printf("drain: sandbox acquire failed session_id=%s run_id=%s: %v", sessionID, runID, err)
				runErr = err
			} else {
				log.Printf("drain: sandbox acquired session_id=%s run_id=%s", sessionID, runID)
			}
		}

		if runErr == nil {
			// Each claim carries exactly one trigger (admission enqueues one durable
			// run per processable event, in admission order). The run projects
			// history reconstructed from run causality — RunStore.ModelHistory walks
			// prior completed/failed runs in admission order, replaying each run's
			// trigger events followed by that run's persisted output events, then
			// this run's own trigger. That deliberately differs from raw
			// receipt/commit order (EventStore.History): a later trigger admitted
			// before this run finished is excluded, so a turn never sees a future
			// user message. Because run N commits its output and marks its trigger
			// processed before run N+1 is claimed, run N+1's causal history already
			// includes run N's committed agent reply. History is read outside the
			// runtime call — the server owns history via the event log.
			// ProjectMessages merges adjacent same-role events and drops a dangling
			// tool_use / orphan tool_result, so each snapshot is a legal Messages-API
			// request even when the bounded window cut a pair. The loop below
			// iterates the claim's single trigger; the requires_action break still
			// parks the run so its awaited result is admitted as the next trigger.
			for _, trigger := range claim.Triggers {
				history, histErr := s.runs.ModelHistory(ctx, claim.Run, historyProjectionLimit)
				if histErr != nil {
					runErr = histErr
					break
				}
				// A user.tool_confirmation resumes a parked always_ask built-in. Recover
				// the ORIGINAL committed agent.tool_use from server-owned causal history
				// (never from client-supplied name/input) so the runtime can execute
				// (allow) or reject (deny) it and emit the paired agent.tool_result. The
				// event is one of the parked run's output events, so it is present in the
				// reconstructed history. A confirmation with no resolvable original action
				// leaves ConfirmedToolUse nil; the runtime then fails safely without
				// executing anything.
				var confirmedToolUse *domain.Event
				if trigger.Type == domain.EvUserToolConfirmation {
					confirmedToolUse = confirmedToolUseFromHistory(trigger, history)
				}
				if outcome, runErr = s.rt.Run(runCtx, agentruntime.RunRequest{
					SessionID:        sessionID,
					Trigger:          trigger,
					Messages:         domain.ProjectMessages(history),
					AgentSnapshot:    claim.AgentSnapshot,
					ToolSet:          toolSet,
					Sandbox:          box,
					ConfirmedToolUse: confirmedToolUse,
				}, sink); runErr != nil {
					log.Printf("drain: runtime execution failed session_id=%s run_id=%s: %v", sessionID, runID, runErr)
					break
				}
				// A parked run (custom tool / always_ask) cannot make progress
				// until the app admits the awaited result as a new trigger. Stop
				// draining this claim's remaining triggers; the terminal
				// session.status_idle{requires_action} is appended below.
				if outcome.RequiresAction {
					break
				}
			}
		}

		if box != nil {
			// The sandbox is session-scoped: it deliberately outlives this run so
			// the next run in the session sees the filesystem state this run left
			// behind. Teardown happens on session deletion (Delete releases it),
			// not here.
			log.Printf("drain: sandbox retained for session session_id=%s run_id=%s", sessionID, runID)
		}

		drafts := sink.Drafts()

		// Linearize finish-vs-interrupt and the completion commit under the session
		// shard lock, serialized with SendEvent's admit+cancel under the same lock.
		// finish records that this run reached completion and reports whether an
		// interrupt had already claimed it (tok.interrupted, set by cancel). Holding
		// the lock across finish → classification → RunStore.Complete closes the late
		// window the old context.Cause-only check left open: an interrupt can no
		// longer durably admit after we classify but before we commit, so a run is
		// classified interrupted iff the interrupt won the race, and a run that
		// completed first is a normal completion the later interrupt cannot re-open.
		// cancelRun(nil) then releases the run context's resources; finish has
		// already deregistered the token, so this cannot mask the interrupt state.
		lock.Lock()
		interrupted := s.cancelers.finish(sessionID, tok)
		cancelRun(nil)

		var errorMessage *string
		var pendingActionEventIDs []string
		switch {
		case interrupted:
			// Deliberate user interrupt. This is NOT a failure: commit the
			// already-buffered authoritative drafts honestly (a partial agent.message
			// the model streamed before cancellation stays committed), close the run
			// as completed, and emit no session.error, no session.status_terminated,
			// and no idle terminal here. We also drop any RequiresAction outcome that
			// raced with the cancellation — no requires_action terminal and no durable
			// pending action are persisted from it. A Fake/custom runtime may have
			// buffered its own session terminal status draft (e.g.
			// session.status_idle) before the interrupt won the completion race; strip
			// those so the interrupt's own durable control run is the single public
			// idle/end_turn. Authoritative nonterminal drafts (agent.message,
			// agent.tool_use, …) are kept honestly. Because more queued work exists
			// (the interrupt control run, plus any redirect message), the session
			// never actually left running; status is forced to running below so the
			// completion neither idles it here nor appends a synthetic
			// session.status_running for that still-queued work.
			drafts = stripSessionTerminalStatusDrafts(drafts)
			log.Printf("drain: run canceled by user.interrupt session_id=%s run_id=%s", sessionID, runID)
		case runErr != nil:
			message := runErr.Error()
			errorMessage = &message
			// An unrecoverable runtime error terminates the session. We have no
			// attempt/lease/retry machinery, so projecting to `rescheduling`
			// would promise an automatic retry that never happens; `terminated`
			// is the honest public status. We deliberately do NOT emit a
			// session.status_idle with a stop_reason here: the documented
			// stop_reason.type union is only end_turn | requires_action, so a
			// stop_reason of "error" would be an invented wire field.
			drafts = append(drafts,
				domain.EventDraft{
					Type: domain.EvSessionError,
					Payload: map[string]any{"error": map[string]any{
						"type": "api_error", "message": message,
					}},
				},
				domain.EventDraft{
					Type:    domain.EvSessionStatusTerminated,
					Payload: map[string]any{},
				},
			)
		case outcome.RequiresAction:
			// The run parked on a custom tool or an always_ask built-in. The
			// session goes idle and waits: the terminal carries a requires_action
			// stop_reason naming the committed agent.custom_tool_use /
			// agent.tool_use events the client must answer (with
			// user.custom_tool_result / user.tool_confirmation). Those results are
			// admitted as ordinary triggers that start a fresh run and resume the
			// loop. event_ids is the documented wire field for this handoff.
			eventIDs := make([]any, len(outcome.ActionEventIDs))
			for i, id := range outcome.ActionEventIDs {
				eventIDs[i] = id
			}
			drafts = append(drafts, domain.EventDraft{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{
					"stop_reason": map[string]any{
						"type":      "requires_action",
						"event_ids": eventIDs,
					},
				},
			})
			// When the run parked, its outcome names the committed action events; the
			// store persists one durable pending action per id in the same transaction
			// that closes the run, deriving the expected response kind from each event's
			// type. A normal end_turn / error passes no action ids.
			pendingActionEventIDs = outcome.ActionEventIDs
		default:
			if !hasTerminalDraft(drafts) {
				drafts = append(drafts, domain.EventDraft{
					Type: domain.EvSessionStatusIdle,
					Payload: map[string]any{
						"stop_reason": map[string]any{"type": "end_turn"},
					},
				})
			}
		}
		status := terminalStatus(drafts)
		if interrupted {
			// The interrupted run committed no terminal status of its own (any
			// buffered terminal draft was stripped above), so terminalStatus(drafts)
			// would report idle. But the session never actually left running: the
			// interrupt's own durable control run — plus any redirect message — is
			// still queued and drives the single public idle/end_turn. Pass
			// StatusRunning explicitly so Complete keeps the session running and does
			// NOT append a synthetic session.status_running for that still-queued
			// work (which would appear as a spurious running→running blip). The
			// queued interrupt claim is preserved; the drain loop claims it next.
			status = domain.StatusRunning
		}
		completion, err := s.runs.Complete(ctx, claim.Run.ID, drafts, status, errorMessage, pendingActionEventIDs)
		if err != nil {
			// The run's terminal drafts could not be committed. Ending the loop
			// leaves the run mid-flight for restart recovery; log so the failure
			// is observable rather than silent. Release the shard lock first — a
			// bare return under the held lock would deadlock every later operation
			// hashing to this shard.
			lock.Unlock()
			log.Printf("drain: run completion failed session_id=%s run_id=%s status=%s: %v", sessionID, runID, status, err)
			return
		}
		// Publish the completion's committed events while still holding the shard
		// lock, in commit order, so this run's output reaches subscribers in durable
		// sequence relative to a concurrent SendEvent's admission publish (which also
		// holds this lock). Hub publication is nonblocking; the external/runtime work
		// above already ran outside the lock, so this adds no blocking call under it.
		s.events.PublishCommitted(completion.Events)
		lock.Unlock()
	}
}

// confirmedToolUseFromHistory resolves the original committed agent.tool_use
// event a user.tool_confirmation trigger references. The referenced id lives in
// the trigger's tool_use_id payload field (validated at admission); the event
// itself is recovered from server-owned causal history, so the client cannot
// substitute a different tool name or input. It returns nil when the reference
// is missing or resolves to anything other than a persisted agent.tool_use —
// the runtime then fails the resume safely without executing a tool.
func confirmedToolUseFromHistory(trigger domain.Event, history []domain.Event) *domain.Event {
	actionEventID, kind, ok := domain.ResolutionReference(trigger.Type, trigger.Payload)
	if !ok || kind != domain.PendingToolConfirmation {
		return nil
	}
	for i := range history {
		if history[i].ID == actionEventID && history[i].Type == domain.EvAgentToolUse {
			e := history[i]
			return &e
		}
	}
	return nil
}

// toolSetHasTools reports whether a resolved toolset offers any tool, in which
// case the run needs a provisioned sandbox for built-in execution.
func toolSetHasTools(ts domain.ToolSet) bool {
	return ts.Builtin != nil || len(ts.Custom) > 0 || len(ts.MCP) > 0
}

func hasTerminalDraft(drafts []domain.EventDraft) bool {
	for _, draft := range drafts {
		switch draft.Type {
		case domain.EvSessionStatusIdle,
			domain.EvSessionStatusRescheduling,
			domain.EvSessionStatusTerminated,
			domain.EvAgentCustomToolUse:
			return true
		}
	}
	return false
}

// stripSessionTerminalStatusDrafts removes any buffered session terminal-status
// draft (session.status_idle / _rescheduled / _terminated) from an interrupted
// run's drafts, keeping every authoritative nonterminal draft (agent.message,
// agent.tool_use, …) in order. AgentCore leaves terminal ownership to the app and
// buffers none, but a Fake/custom runtime can emit session.status_idle before an
// interrupt wins the completion race; stripping it guarantees the interrupt's own
// control run is the single public idle/end_turn. agent.custom_tool_use is NOT a
// session status event, so it is preserved here — the interrupted path already
// drops the RequiresAction outcome, so no pending action or requires_action
// terminal is persisted from it regardless.
func stripSessionTerminalStatusDrafts(drafts []domain.EventDraft) []domain.EventDraft {
	kept := drafts[:0:0]
	for _, draft := range drafts {
		switch draft.Type {
		case domain.EvSessionStatusIdle,
			domain.EvSessionStatusRescheduling,
			domain.EvSessionStatusTerminated:
			continue
		}
		kept = append(kept, draft)
	}
	return kept
}

func terminalStatus(drafts []domain.EventDraft) domain.Status {
	status := domain.StatusIdle
	for _, draft := range drafts {
		switch draft.Type {
		case domain.EvSessionStatusRunning:
			status = domain.StatusRunning
		case domain.EvSessionStatusIdle, domain.EvAgentCustomToolUse:
			status = domain.StatusIdle
		case domain.EvSessionStatusRescheduling:
			status = domain.StatusRescheduling
		case domain.EvSessionStatusTerminated:
			status = domain.StatusTerminated
		}
	}
	return status
}
