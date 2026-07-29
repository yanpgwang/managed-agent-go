package temporal

import (
	"context"
	"fmt"
	"log"
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
	ActivityRunTurn              = "RunTurn"
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
	CompleteTurn(ctx context.Context, sessionID, triggerEventID string, output []domain.EventDraft, status domain.Status) (TurnCompletionResult, error)
	CompleteWorkflowTurn(
		ctx context.Context,
		sessionID string,
		triggerEventID string,
		output []domain.EventDraft,
		status domain.Status,
		attemptID string,
		attemptState domain.RunAttemptState,
		attemptError *string,
	) (TurnCompletionResult, error)
}

// JournalStore is the durable tool-execution journal used by both the granular
// ExecuteTool Activity and the replay-compatible RunTurn Activity. It preserves
// the prepared/started/completed/ambiguous boundary across Activity retries.
// *pg.Store implements it.
type JournalStore interface {
	// RecoverTurn classifies leftovers from a crashed attempt (started steps ->
	// ambiguous, active attempt -> failed) and reports whether the turn now
	// carries prior tool execution and must not be freshly re-run.
	RecoverTurn(ctx context.Context, sessionID, triggerEventID string) (hasPriorExecution bool, err error)
	BeginAttempt(ctx context.Context, sessionID, triggerEventID string) (attemptID string, err error)
	EnsureAttempt(ctx context.Context, sessionID, triggerEventID, attemptID string) error
	FinishAttempt(ctx context.Context, attemptID string, state domain.RunAttemptState, attemptError *string) error
	PrepareToolStep(ctx context.Context, attemptID string, ordinal int, toolUseEventID, toolName string, input map[string]any) (stepID string, err error)
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

// Activities holds the I/O dependencies of the session Activities: the legacy
// runtime retained for old Workflow histories, the model client used by new
// histories, the PostgreSQL event source, the durable tool journal, and the
// session-scoped sandbox lease. All non-deterministic work (SQL, model calls,
// tool side effects) lives here, never in the workflow. journal and sandboxes
// may be nil for a deployment that never routes tool-using turns.
type Activities struct {
	rt          agentruntime.AgentRuntime
	modelClient model.Client
	source      EventSource
	journal     JournalStore
	sandboxes   SandboxLease
	ids         domain.IDGenerator
	previews    PreviewPublisher
}

func NewActivities(
	rt agentruntime.AgentRuntime,
	modelClient model.Client,
	source EventSource,
	journal JournalStore,
	sandboxes SandboxLease,
	ids domain.IDGenerator,
	previewPublisher ...PreviewPublisher,
) *Activities {
	activities := &Activities{
		rt: rt, modelClient: modelClient, source: source,
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
	if trigger.ProcessedAt != nil {
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
	)
	if err != nil {
		return RunTurnResult{}, err
	}
	return RunTurnResult{Terminated: completion.Status == domain.StatusTerminated}, nil
}

// RunTurn is the legacy opaque turn Activity retained for replay compatibility
// with Workflow histories created before workflowAgentLoopChangeID. New Workflow
// executions use PrepareTurn, CallModel, ExecuteTool, and
// CompleteWorkflowTurn.
//
// Because a Temporal Activity may run more than once, correctness rests on two
// idempotency layers:
//   - CompleteTurn is idempotent: a retry after a committed turn replays the same
//     events instead of appending a second copy (handled here by the early
//     already-processed short-circuit, which also avoids re-invoking the model).
//   - The tool journal makes a crossed side-effect boundary explicit: a retry
//     whose prior attempt started a tool step but never recorded a result finds
//     that step recovered as ambiguous and refuses to re-execute, terminating the
//     turn honestly rather than silently replaying the side effect.
func (a *Activities) RunTurn(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
	trigger, err := a.source.GetEvent(ctx, in.SessionID, in.TriggerEventID)
	if err != nil {
		return RunTurnResult{}, err
	}
	session, err := a.source.GetSession(ctx, in.SessionID)
	if err != nil {
		return RunTurnResult{}, err
	}
	// Idempotent short-circuit: a trigger already stamped processed means this
	// turn's completion already committed. Do not re-invoke the model or re-run a
	// tool. Report the session's CURRENT projected status so a turn that
	// previously terminated the session is not treated as an ordinary completion
	// on retry — the workflow must still stop draining the batch.
	if trigger.ProcessedAt != nil {
		return RunTurnResult{Terminated: session.Status == domain.StatusTerminated}, nil
	}
	history, err := a.source.HistoryThrough(ctx, in.SessionID, in.TriggerEventID, historyLimit)
	if err != nil {
		return RunTurnResult{}, err
	}

	toolSet, err := domain.ParseTools(session.AgentSnapshot.Tools)
	if err != nil {
		return a.terminate(ctx, in.SessionID, in.TriggerEventID, "invalid toolset: "+err.Error())
	}

	req := agentruntime.RunRequest{
		SessionID:     in.SessionID,
		Trigger:       trigger,
		Messages:      domain.ProjectMessages(history),
		AgentSnapshot: session.AgentSnapshot,
		ToolSet:       toolSet,
	}

	var attemptID string
	if toolSetHasTools(toolSet) {
		if a.journal == nil || a.sandboxes == nil {
			return a.terminate(ctx, in.SessionID, in.TriggerEventID,
				"tool-using session requires a journal and sandbox on the Temporal path")
		}
		// Recover any leftover tool execution from a crashed prior attempt BEFORE
		// starting a fresh one. If the turn already crossed the side-effect boundary
		// (a step left started and now classified ambiguous, or an already
		// completed/ambiguous step), it must not be freshly re-run: terminate
		// honestly. This slice does not yet resume the model loop from a durable
		// completed result — that is deferred — so a completed prior step is treated
		// as "prior tool execution that cannot be resumed here", NOT as ambiguous.
		hasPrior, err := a.journal.RecoverTurn(ctx, in.SessionID, in.TriggerEventID)
		if err != nil {
			return RunTurnResult{}, err
		}
		if hasPrior {
			log.Printf("temporal: refusing to re-run a turn with prior tool execution session_id=%s trigger=%s",
				in.SessionID, in.TriggerEventID)
			return a.terminate(ctx, in.SessionID, in.TriggerEventID,
				"a prior attempt already executed a tool for this turn; resuming from a durable "+
					"tool result is not supported yet, so the turn cannot be safely retried")
		}
		box, err := a.sandboxes.Acquire(ctx, in.SessionID, sandbox.Spec{Timeout: sandboxTurnTimeout})
		if err != nil {
			return RunTurnResult{}, err
		}
		attemptID, err = a.journal.BeginAttempt(ctx, in.SessionID, in.TriggerEventID)
		if err != nil {
			return RunTurnResult{}, err
		}
		req.Sandbox = box
		req.ToolJournal = activityToolJournal{store: a.journal, attemptID: attemptID}
	}

	sink := newActivitySink(a.ids)
	outcome, runErr := a.rt.Run(ctx, req, sink)
	if runErr != nil {
		// Best-effort close of the attempt as failed, on a durable context so a
		// cancellation that surfaced as runErr does not also prevent recording the
		// classification. If a tool step is still in started state, FinishAttempt
		// refuses; that is fine — the started step is left for the next attempt's
		// RecoverTurn to classify ambiguous. We surface the error so Temporal retries.
		if attemptID != "" {
			dctx, cancel := durableCtx(ctx)
			msg := runErr.Error()
			if ferr := a.journal.FinishAttempt(dctx, attemptID, domain.RunAttemptFailed, &msg); ferr != nil {
				log.Printf("temporal: finish failed attempt (left for recovery) session_id=%s: %v", in.SessionID, ferr)
			}
			cancel()
		}
		return RunTurnResult{}, runErr
	}

	// The legacy path supports always_allow built-ins to end_turn. A park
	// (custom tool / always_ask) is out of scope on the Temporal path; terminate
	// honestly rather than inventing a park protocol here.
	if outcome.RequiresAction {
		if attemptID != "" {
			dctx, cancel := durableCtx(ctx)
			_ = a.journal.FinishAttempt(dctx, attemptID, domain.RunAttemptFailed, strPtr("requires_action not supported on the Temporal path yet"))
			cancel()
		}
		return a.terminate(ctx, in.SessionID, in.TriggerEventID,
			"client-action tools are not supported on the Temporal path yet")
	}

	// Finalize the attempt and commit the turn on durable contexts: after the tool
	// side effect has happened, an Activity-context cancellation must not prevent
	// recording that the attempt completed and committing the authoritative
	// output. Each durable write gets its own fresh WithoutCancel+timeout context
	// (never one created before the long runtime call, which could expire mid-run).
	if attemptID != "" {
		dctx, cancel := durableCtx(ctx)
		err := a.journal.FinishAttempt(dctx, attemptID, domain.RunAttemptCompleted, nil)
		cancel()
		if err != nil {
			return RunTurnResult{}, err
		}
	}

	drafts := append(sink.Drafts(), domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
	})
	dctx, cancel := durableCtx(ctx)
	completion, err := a.source.CompleteTurn(dctx, in.SessionID, in.TriggerEventID, drafts, domain.StatusIdle)
	cancel()
	if err != nil {
		return RunTurnResult{}, err
	}
	return RunTurnResult{
		Terminated: completion.Status == domain.StatusTerminated,
	}, nil
}

// terminate commits an honest terminal failure for a turn: a session.error and
// session.status_terminated, with the session projected to terminated. It is the
// path taken when a turn cannot proceed safely (prior tool execution that cannot
// be resumed, an out-of-scope park, or a misconfiguration). It returns success
// to Temporal because the turn is durably resolved — retrying would not help —
// and reports Terminated so the workflow stops draining the loaded batch. The
// completion runs on a durable context for the same reason as the success path.
func (a *Activities) terminate(ctx context.Context, sessionID, triggerEventID, message string) (RunTurnResult, error) {
	drafts := []domain.EventDraft{
		{Type: domain.EvSessionError, Payload: map[string]any{"error": map[string]any{
			"type": "api_error", "message": message,
		}}},
		{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
	}
	dctx, cancel := durableCtx(ctx)
	_, err := a.source.CompleteTurn(dctx, sessionID, triggerEventID, drafts, domain.StatusTerminated)
	cancel()
	if err != nil {
		return RunTurnResult{}, err
	}
	return RunTurnResult{Terminated: true}, nil
}

func strPtr(s string) *string { return &s }

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

// toolSetHasTools reports whether a resolved toolset offers any tool, in which
// case the turn needs a provisioned sandbox and a durable journal.
func toolSetHasTools(ts domain.ToolSet) bool {
	return ts.Builtin != nil || len(ts.Custom) > 0 || len(ts.MCP) > 0
}
