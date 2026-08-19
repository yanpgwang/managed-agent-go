package agentruntime

import (
	"context"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/sandbox"
)

type EventSink interface {
	Emit(ctx context.Context, drafts []domain.EventDraft) ([]domain.Event, error)
}

// PreviewEmitter is an optional capability an EventSink may also implement to
// receive a live, incremental preview of an assistant text message before its
// full persisted agent.message is emitted. The agent core type-asserts its sink
// to PreviewEmitter and, when present, streams the reply: it announces the
// message with PreviewStart, feeds each text delta through PreviewDelta, and
// finally emits the complete agent.message draft carrying the same event id.
//
// The eventID passed to PreviewStart, every PreviewDelta, and the committed id
// of the persisted agent.message are identical: the preview and the persisted
// event are two views of one event, correlated by that shared id. A sink that
// does not implement PreviewEmitter receives only the non-streamed Emit
// (existing behavior).
type PreviewEmitter interface {
	PreviewStart(eventID, eventType string)
	PreviewDelta(eventID string, index int, text string)
}

// ToolExecutionJournal persists the uncertainty boundary around a built-in
// tool call. Prepare must commit the request before execution can begin, Start
// records that side effects may now occur, and Complete must commit the result
// before the runtime emits it or continues the model loop.
//
// The app owns the durable implementation. The runtime deliberately depends on
// this narrow interface rather than on the database package.
type ToolExecutionJournal interface {
	Prepare(
		ctx context.Context,
		ordinal int,
		toolUseEventID string,
		toolName string,
		input map[string]any,
	) (stepID string, err error)
	Start(ctx context.Context, stepID string) error
	Complete(ctx context.Context, stepID string, result domain.ToolStepResult) error
}

type RunRequest struct {
	SessionID string
	Trigger   domain.Event
	// Messages is the full conversation projected from the session's event log
	// (see domain.ProjectMessages), supplied by the app layer each turn. The
	// agent core replays it to the model; the core never reads the event log.
	Messages []domain.Message
	// AgentSnapshot is the immutable resolved agent definition for the session
	// (version pinning plus per-session overrides), captured at creation time.
	// Adapters read model/system from it; they must not mutate it.
	AgentSnapshot domain.Agent
	// ToolSet is the resolved tool configuration for the session. The agent core
	// derives the model-facing tool schemas from its enabled built-ins and
	// executes always_allow built-ins in Sandbox. The zero value (no toolset)
	// preserves the single-round S1 behavior.
	ToolSet domain.ToolSet
	// Sandbox is the provisioned execution environment for built-in tools. It may
	// be nil when the session has no tools; the core must not execute a tool
	// without one.
	Sandbox sandbox.Sandbox
	// ConfirmedToolUse is the original committed agent.tool_use event a
	// user.tool_confirmation trigger resolves, recovered by the app layer from
	// server-owned causal history (never from client-supplied tool name/input).
	// It is set only when Trigger is a user.tool_confirmation; nil otherwise. The
	// core recovers the tool name and input from its payload and re-validates the
	// built-in/toolset assumptions before executing (allow) or rejecting (deny).
	ConfirmedToolUse *domain.Event
	// ToolJournal is required whenever the core executes a built-in locally. It
	// makes the request and result durable across process loss without coupling
	// AgentCore to a concrete store.
	ToolJournal ToolExecutionJournal
}

// RunOutcome is what a single Run reports back to the app layer so the app can
// choose the terminal event. The agent core never emits the terminal
// session.status_idle itself. RequiresAction is set when the run parked because
// the model called a tool the app or client must act on (a custom tool, or an
// always_ask built-in): ActionEventIDs holds the committed ids of the
// agent.custom_tool_use / agent.tool_use events the app must reference in the
// stop_reason. The zero value means a normal end_turn.
type RunOutcome struct {
	RequiresAction bool
	ActionEventIDs []string
}

type AgentRuntime interface {
	Run(ctx context.Context, req RunRequest, sink EventSink) (RunOutcome, error)
}
