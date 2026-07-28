package temporal

import (
	"context"
	"log"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

// Registered activity names. Referenced by the workflow through the exported
// symbols; named explicitly so a rename cannot silently break replay.
const (
	ActivityLoadEvents = "LoadEvents"
	ActivityRunTurn    = "RunTurn"
)

// EventSource is the read side of the PostgreSQL ledger the Activities depend
// on. The concrete implementation is *pg.Store; the interface keeps the
// Activities testable with an in-memory fake.
type EventSource interface {
	EventsAfter(ctx context.Context, sessionID string, cursor int64, limit int) ([]domain.Event, error)
	HistoryThrough(ctx context.Context, sessionID string, seq int64, limit int) ([]domain.Event, error)
	GetSession(ctx context.Context, id string) (domain.Session, error)
	GetEvent(ctx context.Context, sessionID, id string) (domain.Event, error)
	CompleteTurn(ctx context.Context, sessionID, triggerEventID string, output []domain.EventDraft, status domain.Status) (TurnCompletionResult, error)
}

// JournalStore is the durable tool-execution journal the RunTurn Activity uses to
// preserve the prepared/started/completed/ambiguous boundary across Activity
// retries. *pg.Store implements it. It is separate from EventSource so a turn
// with no tools needs no journal.
type JournalStore interface {
	// RecoverTurn classifies leftovers from a crashed attempt (started steps ->
	// ambiguous, active attempt -> failed) and reports whether the turn now
	// carries prior tool execution and must not be freshly re-run.
	RecoverTurn(ctx context.Context, sessionID, triggerEventID string) (hasPriorExecution bool, err error)
	BeginAttempt(ctx context.Context, sessionID, triggerEventID string) (attemptID string, err error)
	FinishAttempt(ctx context.Context, attemptID string, state domain.RunAttemptState, attemptError *string) error
	PrepareToolStep(ctx context.Context, attemptID string, ordinal int, toolUseEventID, toolName string, input map[string]any) (stepID string, err error)
	StartToolStep(ctx context.Context, stepID string) error
	CompleteToolStep(ctx context.Context, stepID string, result domain.ToolStepResult) error
}

// SandboxLease provisions the session-scoped sandbox a built-in tool executes
// in. *sandbox.SessionManager implements it. The sandbox outlives a single turn:
// it is keyed by session so a later turn reuses the filesystem an earlier turn
// left behind.
type SandboxLease interface {
	Acquire(ctx context.Context, sessionID string, spec sandbox.Spec) (sandbox.Sandbox, error)
}

// TurnCompletionResult mirrors pg.TurnCompletion without importing the pg
// package into the workflow-facing types, keeping the domain boundary intact.
type TurnCompletionResult struct {
	Events  []domain.Event
	Applied bool
}

// historyLimit bounds how many prior events a turn projects into the model. It
// matches the app layer's generous ceiling.
const historyLimit = 10000

// sandboxTurnTimeout bounds a built-in tool execution within a turn.
const sandboxTurnTimeout = 120 * time.Second

// Activities holds the I/O dependencies of the session Activities: the runtime
// that calls the model, the PostgreSQL event source, the durable tool journal,
// and the session-scoped sandbox lease. All non-deterministic work (SQL, model
// calls, tool side effects) lives here, never in the workflow. journal and
// sandboxes may be nil for a deployment that never routes tool-using turns.
type Activities struct {
	rt        agentruntime.AgentRuntime
	source    EventSource
	journal   JournalStore
	sandboxes SandboxLease
	ids       domain.IDGenerator
}

func NewActivities(rt agentruntime.AgentRuntime, source EventSource, journal JournalStore, sandboxes SandboxLease, ids domain.IDGenerator) *Activities {
	return &Activities{rt: rt, source: source, journal: journal, sandboxes: sandboxes, ids: ids}
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

// RunTurn executes one model turn for a trigger event and commits its
// authoritative output through the idempotent PostgreSQL completion. It handles
// both the plain (no-tool) path and a single built-in tool step under the durable
// journal.
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
	// Idempotent short-circuit: a trigger already stamped processed means this
	// turn's completion already committed. Do not re-invoke the model or re-run a
	// tool; the workflow's cursor advances past the committed events on its own.
	if trigger.ProcessedAt != nil {
		return RunTurnResult{}, nil
	}
	session, err := a.source.GetSession(ctx, in.SessionID)
	if err != nil {
		return RunTurnResult{}, err
	}
	history, err := a.source.HistoryThrough(ctx, in.SessionID, trigger.Sequence, historyLimit)
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
		// (a started step now classified ambiguous, or a completed/ambiguous step),
		// it must not be replayed: terminate honestly instead.
		hasPrior, err := a.journal.RecoverTurn(ctx, in.SessionID, in.TriggerEventID)
		if err != nil {
			return RunTurnResult{}, err
		}
		if hasPrior {
			log.Printf("temporal: refusing to replay ambiguous tool turn session_id=%s trigger=%s",
				in.SessionID, in.TriggerEventID)
			return a.terminate(ctx, in.SessionID, in.TriggerEventID,
				"tool execution outcome is ambiguous and cannot be safely retried")
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
		// Best-effort close of the attempt as failed. If a tool step is still in
		// started state (the crash happened mid-side-effect), FinishAttempt refuses;
		// that is fine — the started step is left for the next attempt's RecoverTurn
		// to classify ambiguous. Either way we surface the error so Temporal retries.
		if attemptID != "" {
			msg := runErr.Error()
			if ferr := a.journal.FinishAttempt(ctx, attemptID, domain.RunAttemptFailed, &msg); ferr != nil {
				log.Printf("temporal: finish failed attempt (left for recovery) session_id=%s: %v", in.SessionID, ferr)
			}
		}
		return RunTurnResult{}, runErr
	}

	// This slice supports a single always_allow built-in step to end_turn. A park
	// (custom tool / always_ask) is out of scope on the Temporal path; terminate
	// honestly rather than inventing a park protocol here.
	if outcome.RequiresAction {
		if attemptID != "" {
			_ = a.journal.FinishAttempt(ctx, attemptID, domain.RunAttemptFailed, strPtr("requires_action not supported on the Temporal path yet"))
		}
		return a.terminate(ctx, in.SessionID, in.TriggerEventID,
			"client-action tools are not supported on the Temporal path yet")
	}

	if attemptID != "" {
		if err := a.journal.FinishAttempt(ctx, attemptID, domain.RunAttemptCompleted, nil); err != nil {
			return RunTurnResult{}, err
		}
	}

	drafts := append(sink.Drafts(), domain.EventDraft{
		Type:    domain.EvSessionStatusIdle,
		Payload: map[string]any{"stop_reason": map[string]any{"type": "end_turn"}},
	})
	completion, err := a.source.CompleteTurn(ctx, in.SessionID, in.TriggerEventID, drafts, domain.StatusIdle)
	if err != nil {
		return RunTurnResult{}, err
	}
	return RunTurnResult{MaxEventSeq: maxSeq(completion.Events)}, nil
}

// terminate commits an honest terminal failure for a turn: a session.error and
// session.status_terminated, with the session projected to terminated. It is the
// path taken when a turn cannot proceed safely (ambiguous tool replay, an
// out-of-scope park, or a misconfiguration). It returns success to Temporal
// because the turn is durably resolved — retrying would not help.
func (a *Activities) terminate(ctx context.Context, sessionID, triggerEventID, message string) (RunTurnResult, error) {
	drafts := []domain.EventDraft{
		{Type: domain.EvSessionError, Payload: map[string]any{"error": map[string]any{
			"type": "api_error", "message": message,
		}}},
		{Type: domain.EvSessionStatusTerminated, Payload: map[string]any{}},
	}
	completion, err := a.source.CompleteTurn(ctx, sessionID, triggerEventID, drafts, domain.StatusTerminated)
	if err != nil {
		return RunTurnResult{}, err
	}
	return RunTurnResult{MaxEventSeq: maxSeq(completion.Events)}, nil
}

func maxSeq(events []domain.Event) int64 {
	var m int64
	for _, e := range events {
		if e.Sequence > m {
			m = e.Sequence
		}
	}
	return m
}

func strPtr(s string) *string { return &s }

// toolSetHasTools reports whether a resolved toolset offers any tool, in which
// case the turn needs a provisioned sandbox and a durable journal.
func toolSetHasTools(ts domain.ToolSet) bool {
	return ts.Builtin != nil || len(ts.Custom) > 0 || len(ts.MCP) > 0
}
