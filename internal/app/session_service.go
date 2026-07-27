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
}

func NewSessionService(sess *store.SessionRepo, agents *store.AgentRepo, envs *store.EnvironmentRepo,
	events *EventService, runs *store.RunStore, rt agentruntime.AgentRuntime,
	sandboxProvider sandbox.Provider, ids domain.IDGenerator, clock domain.Clock,
) *SessionService {
	return &SessionService{sess: sess, agents: agents, envs: envs, events: events, runs: runs, rt: rt,
		sandbox: sandbox.NewSessionManager(sandboxProvider), ids: ids, clock: clock, lockSeed: maphash.MakeSeed()}
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
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	s.events.PublishCommitted(admission.Events)
	if len(admission.Runs) > 0 {
		s.kick(id)
	}
	// Send Events echoes only the caller-submitted events, not the status event
	// emitted by the same admission transaction.
	return admission.Events[:len(drafts)], nil
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
	ctx := context.Background()
	for {
		claim, ok, err := s.runs.ClaimNext(ctx, sessionID)
		if err != nil {
			// A claim failure ends the drain loop and leaves the run queued for a
			// later kick/restart-recovery. Log it so it does not vanish silently.
			log.Printf("drain: claim next run failed session_id=%s: %v", sessionID, err)
			return
		}
		if !ok {
			return
		}
		runID := claim.Run.ID
		s.events.PublishCommitted(claim.Events)

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
				if outcome, runErr = s.rt.Run(ctx, agentruntime.RunRequest{
					SessionID:     sessionID,
					Trigger:       trigger,
					Messages:      domain.ProjectMessages(history),
					AgentSnapshot: claim.AgentSnapshot,
					ToolSet:       toolSet,
					Sandbox:       box,
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
		var errorMessage *string
		if runErr != nil {
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
		} else if outcome.RequiresAction {
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
		} else if !hasTerminalDraft(drafts) {
			drafts = append(drafts, domain.EventDraft{
				Type: domain.EvSessionStatusIdle,
				Payload: map[string]any{
					"stop_reason": map[string]any{"type": "end_turn"},
				},
			})
		}
		status := terminalStatus(drafts)
		completion, err := s.runs.Complete(ctx, claim.Run.ID, drafts, status, errorMessage)
		if err != nil {
			// The run's terminal drafts could not be committed. Ending the loop
			// leaves the run mid-flight for restart recovery; log so the failure
			// is observable rather than silent.
			log.Printf("drain: run completion failed session_id=%s run_id=%s status=%s: %v", sessionID, runID, status, err)
			return
		}
		s.events.PublishCommitted(completion.Events)
	}
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
