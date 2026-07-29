package temporal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/agentruntime/tools"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

// Registered activity names. Referenced by the workflow through the exported
// symbols; named explicitly so a rename cannot silently break replay.
const (
	ActivityLoadEvents           = "LoadEvents"
	ActivityLoadPendingActions   = "LoadPendingActions"
	ActivityPrepareTurn          = "PrepareTurn"
	ActivityCallModel            = "CallModel"
	ActivityExecuteTool          = "ExecuteTool"
	ActivityCompleteWorkflowTurn = "CompleteWorkflowTurn"
)

// EventSource is the read side of the PostgreSQL ledger the Activities depend
// on. The concrete implementation is *pg.Store; the interface keeps the
// Activities testable with an in-memory fake.
type EventSource interface {
	EventsAfter(ctx context.Context, sessionID string, cursor int64, limit int) ([]domain.Event, error)
	HistoryThrough(ctx context.Context, sessionID, triggerEventID string, limit int) ([]domain.Event, error)
	GetSession(ctx context.Context, id string) (domain.Session, error)
	GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error)
	UnresolvedPendingActions(ctx context.Context, sessionID string) ([]domain.PendingAction, error)
	CompleteWorkflowTurn(
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
	) (TurnCompletionResult, error)
}

// JournalStore is the durable tool-execution journal used by the granular
// ExecuteTool Activity. It preserves the prepared/started/completed/ambiguous
// boundary across Activity retries. *pg.Store implements it.
type JournalStore interface {
	EnsureAttempt(ctx context.Context, sessionID, triggerEventID, attemptID string) error
	EnsureToolStep(ctx context.Context, attemptID, stepID string, ordinal int, toolUseEventID, toolName string, input map[string]any) (domain.ToolStep, error)
	StartToolStep(ctx context.Context, stepID string) error
	CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error
	MarkToolStepAmbiguous(ctx context.Context, stepID string) error
}

// SandboxLease provisions the session-scoped sandbox a built-in tool executes
// in. *sandbox.SessionManager implements it. The sandbox outlives a single turn:
// it is keyed by session so a later turn reuses the filesystem an earlier turn
// left behind.
type SandboxLease interface {
	Acquire(ctx context.Context, sessionID string, spec sandbox.Spec) (sandbox.Sandbox, error)
}

// PreviewPublisher carries best-effort model deltas to live subscribers. It is
// never part of turn correctness and may be nil.
type PreviewPublisher interface {
	PublishPreview(context.Context, string, domain.PreviewFrame) error
}

// TurnCompletionResult mirrors pg.TurnCompletion without importing the pg
// package into the workflow-facing types, keeping the domain boundary intact.
// Status is the session's projected status after the completion committed, so
// the Activity can tell the workflow whether the session is now terminated.
type TurnCompletionResult struct {
	Events  []domain.Event
	Applied bool
	Status  domain.Status
}

// historyLimit bounds how many prior events a turn projects into the model. It
// matches the app layer's generous ceiling.
const historyLimit = 10000

// sandboxTurnTimeout bounds a built-in tool execution within a turn.
const sandboxTurnTimeout = 120 * time.Second

// toolResultWriteAttempts gives a known in-memory tool result a brief chance to
// cross a transient PostgreSQL outage before the Activity returns an error. A
// later Activity retry must conservatively classify a still-started step as
// ambiguous, so this bounded write-only retry belongs before that boundary.
const toolResultWriteAttempts = 3

// Activities holds the I/O dependencies of the session Activities: the model
// client, PostgreSQL event source, durable tool journal, and session-scoped
// sandbox lease. All non-deterministic work (SQL, model calls, tool side effects)
// lives here, never in the workflow. journal and sandboxes may be nil for a
// deployment that never routes tool-using turns.
type Activities struct {
	modelClient model.Client
	source      EventSource
	journal     JournalStore
	sandboxes   SandboxLease
	ids         domain.IDGenerator
	previews    PreviewPublisher
}

func NewActivities(
	modelClient model.Client,
	source EventSource,
	journal JournalStore,
	sandboxes SandboxLease,
	ids domain.IDGenerator,
	previewPublisher ...PreviewPublisher,
) *Activities {
	activities := &Activities{
		modelClient: modelClient, source: source,
		journal: journal, sandboxes: sandboxes, ids: ids,
	}
	if len(previewPublisher) > 0 {
		activities.previews = previewPublisher[0]
	}
	return activities
}

// LoadEvents returns the ordered public event references after a cursor. Only
// metadata (id, seq, type) crosses back into workflow history; payloads stay in
// PostgreSQL.
func (a *Activities) LoadEvents(ctx context.Context, in LoadEventsInput) (LoadEventsResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = loadBatchLimit
	}
	events, err := a.source.EventsAfter(ctx, in.SessionID, in.Cursor, limit)
	if err != nil {
		return LoadEventsResult{}, err
	}
	refs := make([]EventRef, 0, len(events))
	for _, e := range events {
		refs = append(refs, EventRef{ID: e.ID, Seq: e.Sequence, Type: e.Type})
	}
	return LoadEventsResult{Events: refs}, nil
}

// LoadPendingActions returns the durable requires_action barrier as a small
// selector projection. The Workflow uses only this recorded Activity result to
// choose between parking, resuming the full barrier, and consuming ordinary
// messages.
func (a *Activities) LoadPendingActions(
	ctx context.Context,
	in LoadPendingActionsInput,
) (LoadPendingActionsResult, error) {
	pending, err := a.source.UnresolvedPendingActions(ctx, in.SessionID)
	if err != nil {
		return LoadPendingActionsResult{}, err
	}
	result := LoadPendingActionsResult{
		Actions: make([]PendingActionRef, 0, len(pending)),
	}
	for _, action := range pending {
		actionEvent, err := a.source.GetEvent(ctx, in.SessionID, action.ActionEventID)
		if err != nil {
			return LoadPendingActionsResult{}, err
		}
		ref := PendingActionRef{
			ActionEventID:  action.ActionEventID,
			ActionEventSeq: actionEvent.Sequence,
			Kind:           action.Kind,
		}
		if action.ResolvingEventID != nil {
			resolution, err := a.source.GetEvent(ctx, in.SessionID, *action.ResolvingEventID)
			if err != nil {
				return LoadPendingActionsResult{}, err
			}
			ref.ResolutionEventID = resolution.ID
			ref.ResolutionEventSeq = resolution.Sequence
		}
		result.Actions = append(result.Actions, ref)
	}
	sort.Slice(result.Actions, func(i, j int) bool {
		if result.Actions[i].ActionEventSeq == result.Actions[j].ActionEventSeq {
			return result.Actions[i].ActionEventID < result.Actions[j].ActionEventID
		}
		return result.Actions[i].ActionEventSeq < result.Actions[j].ActionEventSeq
	})
	return result, nil
}

// PrepareTurn reads one turn's immutable starting state from PostgreSQL. The
// result becomes Workflow history; replay never performs these reads in
// Workflow code.
func (a *Activities) PrepareTurn(ctx context.Context, in PrepareTurnInput) (PrepareTurnResult, error) {
	trigger, err := a.source.GetEvent(ctx, in.SessionID, in.TriggerEventID)
	if err != nil {
		return PrepareTurnResult{}, err
	}
	session, err := a.source.GetSession(ctx, in.SessionID)
	if err != nil {
		return PrepareTurnResult{}, err
	}
	var pending []domain.PendingAction
	if len(in.ResolutionEventIDs) > 0 {
		pending, err = a.source.UnresolvedPendingActions(ctx, in.SessionID)
		if err != nil {
			return PrepareTurnResult{}, err
		}
	}
	if trigger.ProcessedAt != nil && !pendingBarrierContainsTrigger(pending, trigger.ID) {
		return PrepareTurnResult{
			AlreadyCompleted: true,
			Terminated:       session.Status == domain.StatusTerminated,
		}, nil
	}
	history, err := a.source.HistoryThrough(ctx, in.SessionID, in.TriggerEventID, historyLimit)
	if err != nil {
		return PrepareTurnResult{}, err
	}
	toolSet, err := domain.ParseTools(session.AgentSnapshot.Tools)
	if err != nil {
		return PrepareTurnResult{FatalError: "invalid toolset: " + err.Error()}, nil
	}

	system := ""
	if session.AgentSnapshot.System != nil {
		system = *session.AgentSnapshot.System
	}
	result := PrepareTurnResult{
		AttemptID: a.ids.NewID(domain.PrefixRunAttempt),
		Request: model.Request{
			Model:    session.AgentSnapshot.Model.ID,
			System:   system,
			Messages: domain.ProjectMessages(history),
			Tools:    agentruntime.EnabledToolSchemas(toolSet),
		},
	}
	if len(in.ResolutionEventIDs) > 0 {
		resumeActions, err := a.prepareResumeActions(
			ctx,
			in.SessionID,
			trigger.ID,
			in.ResolutionEventIDs,
			pending,
		)
		if err != nil {
			var domainErr *domain.DomainError
			if errors.As(err, &domainErr) {
				return PrepareTurnResult{
					FatalError: "invalid pending-action resume: " + err.Error(),
				}, nil
			}
			return PrepareTurnResult{}, err
		}
		result.ResumeActions = resumeActions
		history = withoutResumeEvents(history, resumeActions)
		result.Request.Messages = domain.ProjectMessages(history)
	}
	for _, name := range domain.BuiltinToolNames {
		enabled, policy := toolSet.BuiltinEnabled(name)
		if enabled {
			result.Tools = append(result.Tools, TurnTool{
				Name: name, Kind: TurnToolBuiltin, Permission: policy,
			})
		}
	}
	for _, custom := range toolSet.Custom {
		result.Tools = append(result.Tools, TurnTool{
			Name: custom.Name, Kind: TurnToolCustom,
		})
	}
	return result, nil
}

func pendingBarrierContainsTrigger(pending []domain.PendingAction, triggerEventID string) bool {
	for _, action := range pending {
		if action.ResolvingEventID != nil && *action.ResolvingEventID == triggerEventID {
			return true
		}
	}
	return false
}

func (a *Activities) prepareResumeActions(
	ctx context.Context,
	sessionID string,
	triggerEventID string,
	resolutionEventIDs []string,
	pending []domain.PendingAction,
) ([]ResumeAction, error) {
	expected := make(map[string]struct{}, len(resolutionEventIDs))
	for _, id := range resolutionEventIDs {
		if id == "" {
			return nil, domain.Validation("resolution event id is required")
		}
		if _, duplicate := expected[id]; duplicate {
			return nil, domain.Validation("duplicate resolution event id")
		}
		expected[id] = struct{}{}
	}
	if _, ok := expected[triggerEventID]; !ok {
		return nil, domain.Validation("resume trigger is not in the pending-action barrier")
	}
	if len(pending) != len(expected) {
		return nil, domain.Validation("resolution events do not match the complete pending-action barrier")
	}

	type orderedAction struct {
		seq    int64
		resume ResumeAction
	}
	ordered := make([]orderedAction, 0, len(pending))
	for _, row := range pending {
		if row.ResolvingEventID == nil {
			return nil, domain.Validation("pending-action barrier is not fully claimed")
		}
		resolutionID := *row.ResolvingEventID
		if _, ok := expected[resolutionID]; !ok {
			return nil, domain.Validation("resolution events do not match the complete pending-action barrier")
		}
		action, err := a.source.GetEvent(ctx, sessionID, row.ActionEventID)
		if err != nil {
			return nil, err
		}
		kind, ok := domain.PendingActionKindForEvent(action.Type, action.Payload)
		if !ok || kind != row.Kind {
			return nil, domain.Validation("pending action no longer matches its server event")
		}
		name, _ := action.Payload["name"].(string)
		input, inputOK := action.Payload["input"].(map[string]any)
		if name == "" || !inputOK {
			return nil, domain.Validation("pending action has invalid tool name or input")
		}
		resolution, err := a.source.GetEvent(ctx, sessionID, resolutionID)
		if err != nil {
			return nil, err
		}
		refID, refKind, ok := domain.ResolutionReference(resolution.Type, resolution.Payload)
		if !ok || refID != action.ID || refKind != row.Kind {
			return nil, domain.Validation("client result does not match its pending action")
		}

		resume := ResumeAction{
			ActionEventID:     action.ID,
			Kind:              row.Kind,
			ToolName:          name,
			Input:             input,
			ResolutionEventID: resolution.ID,
		}
		switch row.Kind {
		case domain.PendingCustomToolResult:
			if raw, present := resolution.Payload["content"]; present {
				content, ok := raw.([]any)
				if !ok {
					return nil, domain.Validation("custom tool result content must be an array")
				}
				resume.Content = content
			}
			resume.IsError, _ = resolution.Payload["is_error"].(bool)
		case domain.PendingToolConfirmation:
			resume.Confirmation, _ = resolution.Payload["result"].(string)
			if resume.Confirmation != "allow" && resume.Confirmation != "deny" {
				return nil, domain.Validation("tool confirmation must be allow or deny")
			}
			resume.DenyMessage, _ = resolution.Payload["deny_message"].(string)
			if resume.Confirmation == "allow" {
				resume.ToolStepID = a.ids.NewID(domain.PrefixToolStep)
			}
		default:
			return nil, domain.Validation("unknown pending action kind")
		}
		ordered = append(ordered, orderedAction{seq: action.Sequence, resume: resume})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].seq == ordered[j].seq {
			return ordered[i].resume.ActionEventID < ordered[j].resume.ActionEventID
		}
		return ordered[i].seq < ordered[j].seq
	})
	out := make([]ResumeAction, 0, len(ordered))
	for _, item := range ordered {
		out = append(out, item.resume)
	}
	return out, nil
}

func withoutResumeEvents(history []domain.Event, actions []ResumeAction) []domain.Event {
	excluded := make(map[string]struct{}, len(actions)*2)
	for _, action := range actions {
		excluded[action.ActionEventID] = struct{}{}
		excluded[action.ResolutionEventID] = struct{}{}
	}
	filtered := make([]domain.Event, 0, len(history))
	for _, event := range history {
		if _, drop := excluded[event.ID]; !drop {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// CallModel performs exactly one model call. Its full normalized response is
// returned to Temporal, which durably records the text/tool round structure.
func (a *Activities) CallModel(ctx context.Context, in CallModelInput) (CallModelResult, error) {
	if a.modelClient == nil {
		return CallModelResult{}, fmt.Errorf("temporal: model client is not configured")
	}
	messageEventID := a.ids.NewID(domain.PrefixEvent)
	startedPreview := false
	var previewMu sync.Mutex
	response, err := a.modelClient.CreateMessageStream(ctx, in.Request, func(index int, text string) {
		if a.previews == nil || text == "" {
			return
		}
		previewMu.Lock()
		defer previewMu.Unlock()
		if !startedPreview {
			_ = a.previews.PublishPreview(ctx, in.SessionID, domain.PreviewFrame{
				Kind:      domain.PreviewEventStart,
				EventID:   messageEventID,
				EventType: domain.EvAgentMessage,
			})
			startedPreview = true
		}
		_ = a.previews.PublishPreview(ctx, in.SessionID, domain.PreviewFrame{
			Kind:      domain.PreviewEventDelta,
			EventID:   messageEventID,
			EventType: domain.EvAgentMessage,
			Index:     index,
			Text:      text,
		})
	})
	if err != nil {
		var apiErr *model.APIError
		if errors.As(err, &apiErr) && !apiErr.Retryable() {
			// Permanent provider failures cannot succeed while the immutable turn
			// input and worker configuration remain unchanged. Return them through
			// the existing successful-result terminal channel so the Workflow
			// commits session.error and status_terminated instead of retrying the
			// Activity forever.
			return CallModelResult{FatalError: apiErr.Error()}, nil
		}
		return CallModelResult{}, err
	}
	result := CallModelResult{Response: response}
	normalized := append([]domain.ContentBlock(nil), response.Content...)
	hasText := false
	for i := range normalized {
		switch normalized[i].Type {
		case "text":
			if normalized[i].Text != "" {
				hasText = true
			}
		case "tool_use":
			if normalized[i].ToolName == "" {
				result.FatalError = "model returned a tool_use without a name"
				return result, nil
			}
			if normalized[i].Input == nil {
				result.FatalError = "model returned a tool_use without an input object"
				return result, nil
			}
			// The provider's transient tool id is replaced with the public
			// server-owned event id used by the journal and event ledger.
			normalized[i].ToolUseID = a.ids.NewID(domain.PrefixEvent)
			result.ToolSteps = append(result.ToolSteps, PlannedToolStep{
				ToolUseEventID: normalized[i].ToolUseID,
				ToolStepID:     a.ids.NewID(domain.PrefixToolStep),
			})
		}
	}
	result.Response.Content = normalized
	if hasText {
		result.MessageEventID = messageEventID
	}
	return result, nil
}

// ExecuteTool runs one always-allow built-in behind the durable per-step
// journal. A completed step is returned without re-execution; a started step
// without a result is classified ambiguous and reported as a successful result
// so the Workflow terminates rather than retrying the side effect.
func (a *Activities) ExecuteTool(ctx context.Context, in ExecuteToolInput) (ExecuteToolResult, error) {
	if a.journal == nil || a.sandboxes == nil {
		return ExecuteToolResult{}, fmt.Errorf("temporal: tool execution requires a journal and sandbox")
	}
	if err := a.journal.EnsureAttempt(
		ctx, in.SessionID, in.TriggerEventID, in.AttemptID,
	); err != nil {
		return ExecuteToolResult{}, err
	}
	step, err := a.journal.EnsureToolStep(
		ctx,
		in.AttemptID,
		in.ToolStepID,
		in.Ordinal,
		in.ToolUseEventID,
		in.ToolName,
		in.Input,
	)
	if err != nil {
		return ExecuteToolResult{}, err
	}
	out := ExecuteToolResult{}
	switch step.State {
	case domain.ToolStepCompleted:
		if step.Result == nil {
			return ExecuteToolResult{}, fmt.Errorf("temporal: completed tool step %s has no result", step.ID)
		}
		out.Result = *step.Result
		return out, nil
	case domain.ToolStepAmbiguous:
		out.Ambiguous = true
		return out, nil
	case domain.ToolStepStarted:
		dctx, cancel := durableCtx(ctx)
		err := a.journal.MarkToolStepAmbiguous(dctx, step.ID)
		cancel()
		if err != nil {
			return ExecuteToolResult{}, err
		}
		out.Ambiguous = true
		return out, nil
	case domain.ToolStepPrepared:
		// Continue below.
	default:
		return ExecuteToolResult{}, fmt.Errorf("temporal: invalid tool step state %q", step.State)
	}

	executor, ok := tools.Registry()[in.ToolName]
	if !ok {
		out.FatalError = "built-in tool is not registered: " + in.ToolName
		return out, nil
	}
	// Provisioning happens before Start: a transient sandbox failure cannot turn
	// a never-executed tool into an ambiguous side effect.
	box, err := a.sandboxes.Acquire(ctx, in.SessionID, sandbox.Spec{Timeout: sandboxTurnTimeout})
	if err != nil {
		return ExecuteToolResult{}, err
	}
	dctx, cancel := durableCtx(ctx)
	err = a.journal.StartToolStep(dctx, step.ID)
	cancel()
	if err != nil {
		return ExecuteToolResult{}, err
	}

	executed := executor(ctx, box, in.Input)
	out.Result = domain.ToolStepResult{Content: executed.Content, IsError: executed.IsError}
	if err := completeToolResultDurably(ctx, a.journal, step.ID, out.Result); err != nil {
		return ExecuteToolResult{}, err
	}
	return out, nil
}

func completeToolResultDurably(
	ctx context.Context,
	journal JournalStore,
	stepID string,
	result domain.ToolStepResult,
) error {
	dctx, cancel := durableCtx(ctx)
	defer cancel()

	var lastErr error
	for attempt := 0; attempt < toolResultWriteAttempts; attempt++ {
		if err := journal.CompleteToolStep(dctx, stepID, result); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt+1 == toolResultWriteAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-dctx.Done():
			timer.Stop()
			return lastErr
		}
	}
	return lastErr
}

// CompleteWorkflowTurn commits the Workflow's durable output and optional tool
// attempt through one idempotent PostgreSQL transaction.
func (a *Activities) CompleteWorkflowTurn(
	ctx context.Context,
	in CompleteWorkflowTurnInput,
) (RunTurnResult, error) {
	dctx, cancel := durableCtx(ctx)
	defer cancel()
	completion, err := a.source.CompleteWorkflowTurn(
		dctx,
		in.SessionID,
		in.TriggerEventID,
		in.Output,
		in.Status,
		in.AttemptID,
		in.AttemptState,
		in.AttemptError,
		in.PendingActionEventIDs,
		in.ResolutionEventIDs,
	)
	if err != nil {
		return RunTurnResult{}, err
	}
	switch {
	case completion.Status == domain.StatusTerminated:
		return RunTurnResult{Disposition: TurnTerminated}, nil
	case len(in.PendingActionEventIDs) > 0:
		return RunTurnResult{Disposition: TurnParked}, nil
	default:
		return RunTurnResult{Disposition: TurnCompleted}, nil
	}
}

// durableWriteTimeout bounds a durable write that must run even after the
// Activity context is canceled (e.g. a tool side effect already happened and its
// fact must be recorded). It is deliberately generous but finite.
const durableWriteTimeout = 30 * time.Second

// durableCtx returns a context detached from the caller's cancellation
// (context.WithoutCancel preserves values like tracing metadata) with a fresh
// bounded timeout. It is created per durable write, never once before a long
// runtime call, so the timeout cannot expire mid-run. This mirrors the SQLite
// app's runToolJournal, which records tool facts on a durable context so an
// interrupt reaching a tool executor still commits the result.
func durableCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), durableWriteTimeout)
}
