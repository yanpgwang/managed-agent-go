package temporal

import (
	"context"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
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

// TurnCompletionResult mirrors pg.TurnCompletion without importing the pg
// package into the workflow-facing types, keeping the domain boundary intact.
type TurnCompletionResult struct {
	Events  []domain.Event
	Applied bool
}

// historyLimit bounds how many prior events a turn projects into the model. It
// matches the app layer's generous ceiling.
const historyLimit = 10000

// Activities holds the I/O dependencies of the session Activities: the runtime
// that calls the model and the PostgreSQL event source. All non-deterministic
// work (SQL, model calls) lives here, never in the workflow.
type Activities struct {
	rt     agentruntime.AgentRuntime
	source EventSource
	ids    domain.IDGenerator
}

func NewActivities(rt agentruntime.AgentRuntime, source EventSource, ids domain.IDGenerator) *Activities {
	return &Activities{rt: rt, source: source, ids: ids}
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
// authoritative output through the idempotent PostgreSQL completion. Because a
// Temporal Activity may run more than once, correctness rests on CompleteTurn
// being idempotent: a retry after a committed turn replays the same events
// instead of appending a second copy.
func (a *Activities) RunTurn(ctx context.Context, in RunTurnInput) (RunTurnResult, error) {
	trigger, err := a.source.GetEvent(ctx, in.SessionID, in.TriggerEventID)
	if err != nil {
		return RunTurnResult{}, err
	}
	session, err := a.source.GetSession(ctx, in.SessionID)
	if err != nil {
		return RunTurnResult{}, err
	}
	history, err := a.source.HistoryThrough(ctx, in.SessionID, trigger.Sequence, historyLimit)
	if err != nil {
		return RunTurnResult{}, err
	}

	sink := newActivitySink(a.ids)
	// This slice routes a plain user.message with no toolset: the runtime runs
	// exactly one model round, needs no sandbox and no tool journal, and returns a
	// normal end_turn. Tools, parking, and interrupts are out of scope here.
	outcome, runErr := a.rt.Run(ctx, agentruntime.RunRequest{
		SessionID:     in.SessionID,
		Trigger:       trigger,
		Messages:      domain.ProjectMessages(history),
		AgentSnapshot: session.AgentSnapshot,
	}, sink)
	if runErr != nil {
		// Surface the error so Temporal's retry policy re-runs the Activity. The
		// completion has not committed, so the trigger stays unprocessed and a retry
		// is a clean re-execution.
		return RunTurnResult{}, runErr
	}

	drafts := sink.Drafts()
	// The workflow only drives user.message here, so the terminal is always a
	// normal end_turn idle. (RequiresAction is impossible with no toolset.)
	_ = outcome
	drafts = append(drafts, domain.EventDraft{
		Type: domain.EvSessionStatusIdle,
		Payload: map[string]any{
			"stop_reason": map[string]any{"type": "end_turn"},
		},
	})

	completion, err := a.source.CompleteTurn(ctx, in.SessionID, in.TriggerEventID, drafts, domain.StatusIdle)
	if err != nil {
		return RunTurnResult{}, err
	}

	// Report the highest receipt sequence after this turn so the workflow advances
	// its durable cursor past the turn's own output events.
	var maxSeq int64
	for _, e := range completion.Events {
		if e.Sequence > maxSeq {
			maxSeq = e.Sequence
		}
	}
	return RunTurnResult{MaxEventSeq: maxSeq}, nil
}
