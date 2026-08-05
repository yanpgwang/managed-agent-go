// Package controlplane wires the public HTTP resource semantics to the
// PostgreSQL ledger and Temporal admission boundary.
package controlplane

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

type SessionOrchestrator interface {
	CreateAPISession(
		context.Context,
		domain.Session,
		[]domain.EventDraft,
		...[]app.PreparedSessionResource,
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
	resources    *SessionResourceService
	memoryStores interface {
		GetStore(context.Context, string) (domain.MemoryStore, error)
	}
	skillRef app.SkillReferenceResolver
}

func NewSessionService(
	store *pg.Store,
	agents app.AgentRepository,
	environments app.EnvironmentRepository,
	orchestrator SessionOrchestrator,
	ids domain.IDGenerator,
	clock domain.Clock,
	skillRef app.SkillReferenceResolver,
	resourceServices ...*SessionResourceService,
) *SessionService {
	service := &SessionService{
		store: store, agents: agents, environments: environments,
		orchestrator: orchestrator, ids: ids, clock: clock, skillRef: skillRef,
	}
	if len(resourceServices) > 0 {
		service.resources = resourceServices[0]
	}
	return service
}

// EnableMemoryStoreResources installs the deployment's Memory Store reader.
// Composition calls this only when the configured sandbox adapter can expose
// durable /mnt/memory mounts; API admission otherwise fails explicitly.
func (s *SessionService) EnableMemoryStoreResources(reader interface {
	GetStore(context.Context, string) (domain.MemoryStore, error)
}) {
	s.memoryStores = reader
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
	snapshot.Skills, err = app.ResolveAgentSkillReferences(
		ctx,
		s.skillRef,
		snapshot.Skills,
	)
	if err != nil {
		return domain.Session{}, err
	}
	if environment.ConfigType == "self_hosted" && len(snapshot.Skills) > 0 {
		return domain.Session{}, domain.Unsupported(
			"custom Skills are unavailable for self-hosted Sessions",
		)
	}
	if environment.ConfigType == "self_hosted" && len(input.MemoryResources) > 0 {
		return domain.Session{}, domain.Unsupported(
			"Memory Store resources are unavailable for self-hosted Sessions",
		)
	}
	if err := domain.ValidateToolConfiguration(
		snapshot.Tools,
		snapshot.MCPServers,
	); err != nil {
		return domain.Session{}, domain.Validation(
			"invalid agent tool configuration: " + err.Error(),
		)
	}
	if err := domain.ValidateSkillToolConfiguration(
		snapshot.Tools,
		len(snapshot.Skills) > 0,
	); err != nil {
		return domain.Session{}, domain.Validation(
			"invalid agent Skill tool configuration: " + err.Error(),
		)
	}
	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	now := s.clock.Now().UTC()
	session := domain.Session{
		ID:                s.ids.NewID(domain.PrefixSession),
		AgentID:           agent.ID,
		AgentVersion:      agent.Version,
		EnvironmentID:     environment.ID,
		EnvironmentType:   environment.ConfigType,
		EnvironmentConfig: environment.SessionConfig(),
		Status:            domain.StatusIdle,
		Title:             input.Title,
		Metadata:          metadata,
		AgentSnapshot:     snapshot,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	prepared, err := s.prepareMemoryStoreResources(ctx, session, input.MemoryResources)
	if err != nil {
		return domain.Session{}, err
	}
	if len(input.Resources) > 0 {
		if s.resources == nil {
			return domain.Session{}, domain.Unsupported(
				"File resources are unavailable for the configured deployment",
			)
		}
		fileResources, prepareErr := s.resources.PrepareForSession(ctx, session, input.Resources)
		err = prepareErr
		if err != nil {
			return domain.Session{}, err
		}
		prepared = append(prepared, fileResources...)
	}
	if len(prepared) > 0 {
		session.Resources = make([]domain.SessionResource, len(prepared))
		for index := range prepared {
			session.Resources[index] = prepared[index].Resource
		}
	}
	created, _, err := s.orchestrator.CreateAPISession(
		ctx, session, input.InitialEvents, prepared,
	)
	if err != nil && s.resources != nil {
		var domainErr *domain.DomainError
		if errors.As(err, &domainErr) {
			s.resources.DiscardPrepared(ctx, prepared)
		}
	}
	return created, err
}

func (s *SessionService) prepareMemoryStoreResources(
	ctx context.Context,
	session domain.Session,
	inputs []app.MemorySessionResourceInput,
) ([]app.PreparedSessionResource, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > domain.MaxSessionMemoryStores {
		return nil, domain.Validation("resources may contain at most 8 Memory Stores")
	}
	if s.memoryStores == nil {
		return nil, domain.Unsupported(
			"Memory Store resources are unavailable for the configured deployment",
		)
	}
	seenStores := make(map[string]struct{}, len(inputs))
	seenMounts := make(map[string]struct{}, len(inputs))
	prepared := make([]app.PreparedSessionResource, 0, len(inputs))
	now := session.CreatedAt.UTC()
	for _, input := range inputs {
		if input.MemoryStoreID == "" {
			return nil, domain.Validation("memory_store_id is required")
		}
		if _, duplicate := seenStores[input.MemoryStoreID]; duplicate {
			return nil, domain.Validation("a Memory Store may be attached only once")
		}
		seenStores[input.MemoryStoreID] = struct{}{}
		if input.Access == "" {
			input.Access = domain.MemoryAccessReadWrite
		}
		if input.Access != domain.MemoryAccessReadWrite &&
			input.Access != domain.MemoryAccessReadOnly {
			return nil, domain.Validation("access must be read_write or read_only")
		}
		if !utf8.ValidString(input.Instructions) ||
			utf8.RuneCountInString(input.Instructions) > domain.MaxSessionMemoryInstructionsChars {
			return nil, domain.Validation(
				"instructions must contain at most 4096 valid UTF-8 characters",
			)
		}
		store, err := s.memoryStores.GetStore(ctx, input.MemoryStoreID)
		if err != nil {
			var domainErr *domain.DomainError
			if errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound {
				return nil, domain.Validation("memory store not found")
			}
			return nil, err
		}
		if store.ArchivedAt != nil {
			return nil, domain.Validation("memory store is archived")
		}
		mountPath, err := domain.NormalizeSessionMemoryStoreMountPath(store.Name)
		if err != nil {
			return nil, err
		}
		if _, collision := seenMounts[mountPath]; collision {
			return nil, domain.Validation(
				"attached Memory Store names must produce distinct mount paths",
			)
		}
		seenMounts[mountPath] = struct{}{}
		prepared = append(prepared, app.PreparedSessionResource{Resource: domain.SessionResource{
			ID:                     s.ids.NewID(domain.PrefixSessionResource),
			SessionID:              session.ID,
			ResourceType:           domain.SessionResourceTypeMemoryStore,
			MemoryStoreID:          store.ID,
			MemoryAccess:           input.Access,
			MemoryInstructions:     input.Instructions,
			MemoryStoreName:        store.Name,
			MemoryStoreDescription: store.Description,
			MountPath:              mountPath,
			CreatedAt:              now,
			UpdatedAt:              now,
			State:                  domain.SessionResourceActive,
		}})
	}
	return prepared, nil
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
			domain.EvUserToolResult,
			domain.EvUserToolConfirmation,
			domain.EvSystemMessage:
			// define_outcome is processed on receipt; messages schedule ordinary
			// turns; custom results and confirmations claim the durable
			// pending-action barrier and wake the SessionWorkflow only when the full
			// result set is present.
		case domain.EvUserInterrupt:
			if _, targeted := draft.Payload["session_thread_id"]; targeted {
				return nil, domain.Unsupported(
					"targeted multi-agent interrupts are not supported",
				)
			}
		default:
			return nil, domain.Unsupported(
				"this client event is not supported on the PostgreSQL backend",
			)
		}
	}
	return s.orchestrator.Admit(ctx, id, drafts)
}

// Update applies the documented session update body. Validation of the merged
// result and the idle precondition for mid-session agent changes both run
// inside the store transaction that holds the session's admission lock, so a
// concurrent turn admission cannot slip between the check and the write.
func (s *SessionService) Update(
	ctx context.Context,
	id string,
	update domain.SessionUpdate,
) (domain.Session, error) {
	return s.store.UpdateSession(ctx, id, update)
}

func (s *SessionService) Archive(ctx context.Context, id string) (domain.Session, error) {
	return s.store.ArchiveSession(ctx, id)
}

func (s *SessionService) Delete(ctx context.Context, id string) error {
	// Fence new admission before stopping orchestration and releasing the
	// provider sandbox. Without this phase, an admission could make the session
	// running in the gap before physical deletion.
	if err := s.store.PrepareSessionDeletion(ctx, id); err != nil {
		return err
	}
	if err := s.orchestrator.TerminateSession(ctx, id); err != nil {
		// Keep the fence on an ambiguous external result. Retrying DELETE safely
		// repeats Workflow termination and idempotent sandbox cleanup.
		return err
	}
	if s.resources != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := s.resources.CleanupSession(cleanupCtx, id)
		cancel()
		if err != nil {
			return err
		}
	}
	memoryCleanupCtx, memoryCleanupCancel := context.WithTimeout(
		context.WithoutCancel(ctx), 5*time.Second,
	)
	err := s.store.FinalizeSessionMemoryResources(memoryCleanupCtx, id)
	memoryCleanupCancel()
	if err != nil {
		return err
	}
	// Once termination succeeds, finish the fenced delete even if the client
	// disconnects. If the database write fails, the marker intentionally remains
	// and a later DELETE can safely retry the termination/finalization sequence.
	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.store.FinalizeSessionDeletion(finalizeCtx, id)
}
