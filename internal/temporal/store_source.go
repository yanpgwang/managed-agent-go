package temporal

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/mcpclient"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
)

func (s storeSource) GetMCPDiscoverySnapshot(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
) ([]mcpclient.Tool, bool, error) {
	return s.store.GetMCPDiscoverySnapshot(ctx, sessionID, server)
}

func (s storeSource) PutMCPDiscoverySnapshot(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
	tools []mcpclient.Tool,
) ([]mcpclient.Tool, error) {
	return s.store.PutMCPDiscoverySnapshot(ctx, sessionID, server, tools)
}

// storeSource adapts *pg.Store to the EventSource interface the Activities
// depend on. It exists so the temporal package depends on a narrow interface
// rather than the pg package's concrete completion type, keeping the domain
// boundary clean (no Temporal wire types leak into pg, no pg types into the
// workflow contract).
type storeSource struct{ store *pg.Store }

func (s storeSource) SessionSkillsForRuntime(
	ctx context.Context,
	sessionID string,
) ([]domain.SkillVersion, error) {
	return s.store.SessionSkillsForRuntime(ctx, sessionID)
}

// NewStoreSource wraps a PostgreSQL store as an Activity EventSource.
func NewStoreSource(store *pg.Store) EventSource { return storeSource{store: store} }

func (s storeSource) EventsAfter(ctx context.Context, sessionID string, cursor int64, limit int) ([]domain.Event, error) {
	return s.store.EventsAfter(ctx, sessionID, cursor, limit)
}

func (s storeSource) FirstUnprocessedInterruptAfter(
	ctx context.Context,
	sessionID string,
	afterSeq int64,
) (*domain.Event, error) {
	return s.store.FirstUnprocessedInterruptAfter(ctx, sessionID, afterSeq)
}

func (s storeSource) HistoryThrough(ctx context.Context, sessionID, triggerEventID string, limit int) ([]domain.Event, error) {
	return s.store.HistoryThrough(ctx, sessionID, triggerEventID, limit)
}

func (s storeSource) GetSession(ctx context.Context, id string) (domain.Session, error) {
	return s.store.GetSession(ctx, id)
}

func (s storeSource) GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error) {
	return s.store.GetEvent(ctx, sessionID, id)
}

func (s storeSource) UnresolvedPendingActions(
	ctx context.Context,
	sessionID string,
) ([]domain.PendingAction, error) {
	return s.store.UnresolvedPendingActions(ctx, sessionID)
}

func (s storeSource) AppendWorkflowEvents(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	drafts []domain.EventDraft,
) error {
	return s.store.AppendWorkflowEvents(ctx, sessionID, triggerEventID, drafts)
}

func (s storeSource) RecordWorkflowRetry(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	errorEventID string,
	statusEventID string,
	errorPayload map[string]any,
) error {
	return s.store.RecordWorkflowRetry(
		ctx,
		sessionID,
		triggerEventID,
		errorEventID,
		statusEventID,
		errorPayload,
	)
}

func (s storeSource) ResumeWorkflowRetry(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	statusEventID string,
) error {
	return s.store.ResumeWorkflowRetry(
		ctx,
		sessionID,
		triggerEventID,
		statusEventID,
	)
}

func (s storeSource) LoadProviderTranscript(
	ctx context.Context,
	sessionID string,
) (domain.ProviderTranscript, error) {
	return s.store.LoadProviderTranscript(ctx, sessionID)
}

func (s storeSource) CompleteWorkflowTurn(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
) (TurnCompletionResult, error) {
	return s.CompleteWorkflowTurnWithUsage(
		ctx,
		sessionID,
		triggerEventID,
		output,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
		domain.TokenUsage{},
	)
}

func (s storeSource) CompleteWorkflowTurnWithUsage(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
	usage domain.TokenUsage,
) (TurnCompletionResult, error) {
	res, err := s.store.CompleteWorkflowTurnWithUsage(
		ctx,
		sessionID,
		triggerEventID,
		output,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
		usage,
	)
	if err != nil {
		return TurnCompletionResult{}, err
	}
	parked := res.Parked
	return TurnCompletionResult{
		Events:  res.Events,
		Applied: res.Applied,
		Status:  res.Session.Status,
		Parked:  &parked,
	}, nil
}

func (s storeSource) CompleteWorkflowTurnWithTranscript(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
	transcriptDelta []domain.Message,
	toolUseMappings []domain.ProviderToolUseMapping,
) (TurnCompletionResult, error) {
	return s.CompleteWorkflowTurnWithTranscriptAndUsage(
		ctx,
		sessionID,
		triggerEventID,
		output,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
		transcriptDelta,
		toolUseMappings,
		domain.TokenUsage{},
	)
}

func (s storeSource) CompleteWorkflowTurnWithTranscriptAndUsage(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	output []domain.EventDraft,
	status domain.Status,
	attemptID string,
	attemptState domain.RunAttemptState,
	attemptError *string,
	pendingActionEventIDs []string,
	resolutionEventIDs []string,
	transcriptDelta []domain.Message,
	toolUseMappings []domain.ProviderToolUseMapping,
	usage domain.TokenUsage,
) (TurnCompletionResult, error) {
	res, err := s.store.CompleteWorkflowTurnWithTranscriptAndUsage(
		ctx,
		sessionID,
		triggerEventID,
		output,
		status,
		attemptID,
		attemptState,
		attemptError,
		pendingActionEventIDs,
		resolutionEventIDs,
		transcriptDelta,
		toolUseMappings,
		usage,
	)
	if err != nil {
		return TurnCompletionResult{}, err
	}
	parked := res.Parked
	return TurnCompletionResult{
		Events:  res.Events,
		Applied: res.Applied,
		Status:  res.Session.Status,
		Parked:  &parked,
	}, nil
}

// storeSource also satisfies JournalStore; the methods below delegate directly.

func (s storeSource) EnsureAttempt(ctx context.Context, sessionID, triggerEventID, attemptID string) error {
	_, err := s.store.EnsureAttempt(ctx, sessionID, triggerEventID, attemptID)
	return err
}

func (s storeSource) EnsureToolStep(
	ctx context.Context,
	attemptID string,
	stepID string,
	ordinal int,
	toolUseEventID string,
	toolName string,
	input map[string]any,
) (domain.ToolStep, error) {
	return s.store.EnsureToolStep(ctx, attemptID, stepID, ordinal, toolUseEventID, toolName, input)
}

func (s storeSource) StartToolStep(ctx context.Context, stepID string) error {
	return s.store.StartToolStep(ctx, stepID)
}

func (s storeSource) CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error {
	return s.store.CompleteToolStep(ctx, stepID, result)
}

func (s storeSource) MarkToolStepAmbiguous(ctx context.Context, stepID string) error {
	return s.store.MarkToolStepAmbiguous(ctx, stepID)
}
