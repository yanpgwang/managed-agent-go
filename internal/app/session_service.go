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
// EventService.HistoryTail): an over-limit session carries its most recent
// context rather than the oldest events. Compaction is a later slice; until
// then this is a generous ceiling that keeps a single unbounded session from
// OOMing a turn.
const historyProjectionLimit = 10000

type SessionService struct {
	sess    *store.SessionRepo
	agents  *store.AgentRepo
	envs    *store.EnvironmentRepo
	events  *EventService
	runs    *store.RunStore
	rt      agentruntime.AgentRuntime
	sandbox sandbox.Provider
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
		sandbox: sandboxProvider, ids: ids, clock: clock, lockSeed: maphash.MakeSeed()}
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
	if admission.Run != nil {
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
	if admission.Run != nil {
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
	defer lock.Unlock()

	sess, err := s.sess.Get(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == domain.StatusRunning {
		return domain.Conflict("cannot delete a running session; interrupt first")
	}
	if err := s.sess.Delete(ctx, id); err != nil {
		return err
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

		// Provision one sandbox per run when the session has tools, and destroy
		// it when the run ends. Provisioning and destruction are external calls
		// made outside any transaction or process lock. A provisioning failure
		// terminates the session like any other unrecoverable runtime error.
		var box sandbox.Sandbox
		if runErr == nil && toolSetHasTools(toolSet) {
			box, err = s.sandbox.Provision(ctx, sandbox.Spec{Timeout: 120 * time.Second})
			if err != nil {
				log.Printf("drain: sandbox provision failed session_id=%s run_id=%s: %v", sessionID, runID, err)
				runErr = err
			} else {
				log.Printf("drain: sandbox provisioned session_id=%s run_id=%s", sessionID, runID)
			}
		}

		if runErr == nil {
			// Process one trigger to completion before the next. Each trigger
			// re-reads and re-projects the newest bounded window of the ordered
			// event log (HistoryTail returns the most recent historyProjectionLimit
			// events in chronological order, not the whole log). Note the limit of
			// this within a single claim: the runtime's drafts are held in the
			// in-memory bufferedSink and only committed to the store when the run
			// completes, so HistoryTail here observes only the user events admission
			// already persisted — not the agent output an earlier trigger in this
			// same claim produced but has not yet committed. True per-trigger
			// isolation (each trigger committed independently before the next
			// projects) is deferred. History is read outside any transaction,
			// before calling the runtime — the server owns history via the event
			// log. ProjectMessages merges adjacent same-role events, so each
			// snapshot alternates roles and is a legal Messages-API request.
			for _, trigger := range claim.Triggers {
				history, histErr := s.events.HistoryTail(ctx, sessionID, historyProjectionLimit)
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
			if destroyErr := box.Destroy(ctx); destroyErr != nil {
				// Destroy failure can leak sandbox resources (host temp dir or a
				// container). Log it; the run outcome itself is unaffected.
				log.Printf("drain: sandbox destroy failed session_id=%s run_id=%s: %v", sessionID, runID, destroyErr)
			}
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
