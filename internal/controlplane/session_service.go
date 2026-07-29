// Package controlplane wires the public HTTP resource semantics to the
// PostgreSQL ledger and Temporal admission boundary.
package controlplane

import (
	"context"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

type SessionOrchestrator interface {
	CreateAPISession(
		context.Context,
		domain.Session,
		[]domain.EventDraft,
	) (domain.Session, []domain.Event, error)
	Admit(context.Context, string, []domain.EventDraft) ([]domain.Event, error)
	TerminateSession(context.Context, string) error
}

// SessionService owns public Session validation and delegates the atomic
// session/event admission boundary to PostgreSQL plus the Temporal outbox.
type SessionService struct {
	store        *pg.Store
	agents       app.AgentRepository
	environments app.EnvironmentRepository
	orchestrator SessionOrchestrator
	ids          domain.IDGenerator
	clock        domain.Clock
}

func NewSessionService(
	store *pg.Store,
	agents app.AgentRepository,
	environments app.EnvironmentRepository,
	orchestrator SessionOrchestrator,
	ids domain.IDGenerator,
	clock domain.Clock,
) *SessionService {
	return &SessionService{
		store: store, agents: agents, environments: environments,
		orchestrator: orchestrator, ids: ids, clock: clock,
	}
}

func (s *SessionService) Create(
	ctx context.Context,
	input app.CreateSessionInput,
) (domain.Session, error) {
	if err := app.ValidateMetadata(input.Metadata); err != nil {
		return domain.Session{}, err
	}
	var (
		agent domain.Agent
		err   error
	)
	if input.AgentVersion != nil {
		agent, err = s.agents.GetVersion(ctx, input.AgentID, *input.AgentVersion)
	} else {
		agent, err = s.agents.Latest(ctx, input.AgentID)
	}
	if err != nil {
		return domain.Session{}, domain.Validation("agent not found")
	}
	if agent.ArchivedAt != nil {
		return domain.Session{}, domain.Validation("agent is archived")
	}
	environment, err := s.environments.Get(ctx, input.EnvironmentID)
	if err != nil {
		return domain.Session{}, domain.Validation("environment not found")
	}
	if environment.ArchivedAt != nil {
		return domain.Session{}, domain.Validation("environment is archived")
	}
	if environment.ConfigType == "self_hosted" {
		return domain.Session{}, domain.Unsupported(
			"sessions against self_hosted environments are not supported in v0.1",
		)
	}
	if len(input.InitialEvents) > 50 {
		return domain.Session{}, domain.Validation("initial_events exceeds 50")
	}
	for _, event := range input.InitialEvents {
		if !domain.IsInitialEventType(event.Type) {
			return domain.Session{}, domain.Validation(
				"initial_events may contain only user.message or user.define_outcome",
			)
		}
	}

	snapshot := agent
	if input.Overrides != nil {
		snapshot = agent.WithOverrides(*input.Overrides)
	}
	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	now := s.clock.Now().UTC()
	session := domain.Session{
		ID:            s.ids.NewID(domain.PrefixSession),
		AgentID:       agent.ID,
		AgentVersion:  agent.Version,
		EnvironmentID: environment.ID,
		Status:        domain.StatusIdle,
		Title:         input.Title,
		Metadata:      metadata,
		AgentSnapshot: snapshot,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	created, _, err := s.orchestrator.CreateAPISession(ctx, session, input.InitialEvents)
	return created, err
}

func (s *SessionService) Get(ctx context.Context, id string) (domain.Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s *SessionService) List(
	ctx context.Context,
	query app.ListPage,
) (app.SessionListPage, error) {
	return s.store.ListSessions(ctx, query)
}

func (s *SessionService) SendEvent(
	ctx context.Context,
	id string,
	drafts []domain.EventDraft,
) ([]domain.Event, error) {
	for _, draft := range drafts {
		switch draft.Type {
		case domain.EvUserMessage,
			domain.EvUserDefineOutcome,
			domain.EvUserCustomToolResult,
			domain.EvUserToolConfirmation:
			// define_outcome is processed on receipt; messages schedule ordinary
			// turns; custom results and confirmations claim the durable
			// pending-action barrier and wake the v2 Workflow only when the full
			// result set is present.
		default:
			return nil, domain.Unsupported(
				"this event requires Temporal interrupt support that is not " +
					"available on the PostgreSQL backend yet",
			)
		}
	}
	return s.orchestrator.Admit(ctx, id, drafts)
}

func (s *SessionService) UpdateTitle(
	ctx context.Context,
	id string,
	title string,
) (domain.Session, error) {
	return s.store.UpdateSessionTitle(ctx, id, title)
}

func (s *SessionService) Archive(ctx context.Context, id string) (domain.Session, error) {
	return s.store.ArchiveSession(ctx, id)
}

func (s *SessionService) Delete(ctx context.Context, id string) error {
	// Fence new admission before the external termination call. Without this
	// phase, an admission could make the session running after termination but
	// before the physical delete, leaving a running projection with no Workflow.
	if err := s.store.PrepareSessionDeletion(ctx, id); err != nil {
		return err
	}
	if err := s.orchestrator.TerminateSession(ctx, id); err != nil {
		// Keep the fence on an ambiguous external result. Retrying DELETE safely
		// repeats termination; reopening admission here could race a termination
		// that actually reached Temporal despite its lost response.
		return err
	}
	// Once termination succeeds, finish the fenced delete even if the client
	// disconnects. If the database write fails, the marker intentionally remains
	// and a later DELETE can safely retry the termination/finalization sequence.
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.FinalizeSessionDeletion(finalizeCtx, id)
}
