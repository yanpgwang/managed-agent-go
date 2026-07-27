package agentruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

type captureSink struct {
	drafts []domain.EventDraft
	events []domain.Event
	n      int
}

func (s *captureSink) Emit(_ context.Context, d []domain.EventDraft) ([]domain.Event, error) {
	s.drafts = append(s.drafts, d...)
	out := make([]domain.Event, len(d))
	for i, dr := range d {
		s.n++
		out[i] = domain.Event{ID: fmt.Sprintf("evt_%d", s.n), Type: dr.Type, Payload: dr.Payload}
	}
	s.events = append(s.events, out...)
	return out, nil
}

// draftTypes returns the ordered list of emitted event types.
func draftTypes(s *captureSink) []string {
	types := make([]string, len(s.drafts))
	for i, d := range s.drafts {
		types[i] = d.Type
	}
	return types
}

// hasSeq reports whether want appears as an ordered (not necessarily
// contiguous) subsequence of got.
func hasSeq(got []string, want ...string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestAgentCore_EmitsAgentMessageFromModel(t *testing.T) {
	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &captureSink{}
	sys := "be helpful"
	_, err := core.Run(context.Background(), RunRequest{
		SessionID:     "sesn_1",
		Trigger:       domain.Event{Type: domain.EvUserMessage},
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "ping"}}}},
		AgentSnapshot: domain.Agent{Model: domain.Model{ID: "m"}, System: &sys},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.drafts) != 1 || sink.drafts[0].Type != domain.EvAgentMessage {
		t.Fatalf("drafts = %#v, want one agent.message", sink.drafts)
	}
	content, _ := sink.drafts[0].Payload["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one block", sink.drafts[0].Payload["content"])
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "echo: ping" {
		t.Fatalf("block = %#v, want text 'echo: ping'", block)
	}
}

// previewCaptureSink implements both EventSink and PreviewEmitter. It records
// the preview start id, the number of deltas, the concatenated delta text, and
// every emitted draft (with its committed id) so a test can assert the preview
// stream and the persisted agent.message share one id.
type previewCaptureSink struct {
	startID    string
	startType  string
	deltaCount int
	deltaText  string
	emitted    []domain.EventDraft
}

func (s *previewCaptureSink) Emit(_ context.Context, drafts []domain.EventDraft) ([]domain.Event, error) {
	out := make([]domain.Event, len(drafts))
	for i, d := range drafts {
		s.emitted = append(s.emitted, d)
		out[i] = domain.Event{ID: d.ID, Type: d.Type, Payload: d.Payload}
	}
	return out, nil
}

func (s *previewCaptureSink) PreviewStart(eventID, eventType string) {
	s.startID = eventID
	s.startType = eventType
}

func (s *previewCaptureSink) PreviewDelta(_ string, _ int, text string) {
	s.deltaCount++
	s.deltaText += text
}

func TestAgentCore_EmitsPreviewThenPersistedMessage(t *testing.T) {
	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &previewCaptureSink{}
	_, err := core.Run(context.Background(), RunRequest{
		Trigger:       domain.Event{Type: domain.EvUserMessage},
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "hi"}}}},
		AgentSnapshot: domain.Agent{Model: domain.Model{ID: "m"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	// one PreviewStart, >=1 PreviewDelta, then a persisted agent.message with the SAME id
	if sink.startID == "" || sink.deltaCount < 1 {
		t.Fatalf("preview: startID=%q deltas=%d", sink.startID, sink.deltaCount)
	}
	if sink.startType != domain.EvAgentMessage {
		t.Fatalf("preview start type = %q, want %q", sink.startType, domain.EvAgentMessage)
	}
	if len(sink.emitted) != 1 || sink.emitted[0].Type != domain.EvAgentMessage {
		t.Fatalf("emitted = %#v, want one agent.message", sink.emitted)
	}
	if sink.emitted[0].ID != sink.startID {
		t.Fatalf("persisted id %q != preview start id %q", sink.emitted[0].ID, sink.startID)
	}
	// delta text concatenates to the persisted message text
	if sink.deltaText != "echo: hi" {
		t.Fatalf("delta text = %q, want 'echo: hi'", sink.deltaText)
	}
}

func TestAgentCore_EmptyResponseEmitsNothing(t *testing.T) {
	core := NewAgentCore(emptyClient{}, domain.NewSeqIDGen())
	sink := &captureSink{}
	_, err := core.Run(context.Background(), RunRequest{
		Trigger:  domain.Event{Type: domain.EvUserMessage},
		Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "x"}}}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.drafts) != 0 {
		t.Fatalf("drafts = %#v, want none for empty model response", sink.drafts)
	}
}

type emptyClient struct{}

func (emptyClient) CreateMessage(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{StopReason: "end_turn"}, nil
}

func (c emptyClient) CreateMessageStream(ctx context.Context, req model.Request, _ func(index int, text string)) (model.Response, error) {
	return c.CreateMessage(ctx, req)
}

func TestAgentCore_NonUserTriggerIsNoop(t *testing.T) {
	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &captureSink{}
	if _, err := core.Run(context.Background(), RunRequest{
		Trigger: domain.Event{Type: domain.EvUserInterrupt},
	}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.drafts) != 0 {
		t.Fatalf("drafts = %#v, want none for non-user trigger", sink.drafts)
	}
}

// TestEnabledBuiltinSchemas_AllOfferedSchemasAreObjects guards C1: every
// built-in schema offered to the model must be a non-nil JSON Schema object.
// With the default toolset all eight built-ins are enabled, including
// glob/grep/web_fetch/web_search whose executors are not implemented; the real
// Anthropic API rejects a tool declared with "input_schema":null (400), so the
// declared schema must still be a legal object even when the executor is a stub.
func TestEnabledBuiltinSchemas_AllOfferedSchemasAreObjects(t *testing.T) {
	// Default toolset: {"type":"agent_toolset_20260401"} with no configs →
	// DefaultEnabled=true → all eight built-ins offered.
	ts := domain.ToolSet{Builtin: &domain.BuiltinToolset{
		DefaultEnabled: true,
		DefaultPolicy:  domain.PermissionPolicy{Type: "always_allow"},
	}}
	schemas := enabledBuiltinSchemas(ts)
	if len(schemas) != len(domain.BuiltinToolNames) {
		t.Fatalf("offered %d builtin schemas, want %d", len(schemas), len(domain.BuiltinToolNames))
	}
	for _, s := range schemas {
		if s.InputSchema == nil {
			t.Fatalf("tool %q offered with nil InputSchema (serializes to input_schema:null → 400)", s.Name)
		}
		if typ, _ := s.InputSchema["type"].(string); typ != "object" {
			t.Fatalf("tool %q InputSchema type = %v, want object", s.Name, s.InputSchema["type"])
		}
	}
}

func TestAgentCore_ExecutesBuiltinToolLoop(t *testing.T) {
	sb, err := sandbox.NewLocalProvider().Provision(context.Background(), sandbox.Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy(context.Background())
	if err := sb.WriteFile(context.Background(), "note.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}

	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &captureSink{}
	enabled := true
	ts := domain.ToolSet{Builtin: &domain.BuiltinToolset{
		DefaultEnabled: true,
		DefaultPolicy:  domain.PermissionPolicy{Type: "always_allow"},
		Configs:        []domain.BuiltinConfig{{Name: "bash", Enabled: &enabled}},
	}}
	_, err = core.Run(context.Background(), RunRequest{
		Trigger:       domain.Event{Type: domain.EvUserMessage},
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "cat note.txt"}}}},
		ToolSet:       ts,
		Sandbox:       sb,
		AgentSnapshot: domain.Agent{Model: domain.Model{ID: "m"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: agent.tool_use (bash), agent.tool_result, then agent.message (fake ends turn).
	types := draftTypes(sink)
	if !hasSeq(types, domain.EvAgentToolUse, domain.EvAgentToolResult, domain.EvAgentMessage) {
		t.Fatalf("draft types = %v", types)
	}

	// The tool_result must correlate to the committed id of the tool_use event.
	var useID, resultFor string
	for _, e := range sink.events {
		switch e.Type {
		case domain.EvAgentToolUse:
			useID = e.ID
		case domain.EvAgentToolResult:
			resultFor, _ = e.Payload["tool_use_id"].(string)
		}
	}
	if useID == "" || useID != resultFor {
		t.Fatalf("tool_result tool_use_id = %q, want committed use id %q", resultFor, useID)
	}
}

// TestAgentCore_CustomToolParksWithRequiresAction verifies that when the model
// calls a custom tool (one the core cannot execute), the run emits
// agent.custom_tool_use and returns a RunOutcome parked on requires_action with
// that committed event id. The core never emits a terminal status itself.
func TestAgentCore_CustomToolParksWithRequiresAction(t *testing.T) {
	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &captureSink{}
	// A custom-only toolset: model.NewFake requests the first offered tool on the
	// first turn (no tool_result yet), so it calls the custom tool get_metrics.
	ts := domain.ToolSet{Custom: []domain.CustomTool{{
		Name: "get_metrics", InputSchema: map[string]any{"type": "object"},
	}}}
	outcome, err := core.Run(context.Background(), RunRequest{
		Trigger:       domain.Event{Type: domain.EvUserMessage},
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "metrics?"}}}},
		ToolSet:       ts,
		AgentSnapshot: domain.Agent{Model: domain.Model{ID: "m"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.RequiresAction {
		t.Fatalf("outcome = %#v, want RequiresAction", outcome)
	}

	// The parked event must be an agent.custom_tool_use and its committed id must
	// be exactly the id reported in ActionEventIDs.
	var useID string
	for _, e := range sink.events {
		if e.Type == domain.EvAgentCustomToolUse {
			useID = e.ID
		}
	}
	if useID == "" {
		t.Fatalf("no agent.custom_tool_use emitted; drafts = %v", draftTypes(sink))
	}
	if len(outcome.ActionEventIDs) != 1 || outcome.ActionEventIDs[0] != useID {
		t.Fatalf("ActionEventIDs = %v, want [%s]", outcome.ActionEventIDs, useID)
	}
}

// TestAgentCore_AlwaysAskBuiltinParks verifies that an enabled built-in whose
// permission policy is always_ask parks the run: it emits agent.tool_use with
// evaluated_permission "ask" and returns requires_action carrying that id.
func TestAgentCore_AlwaysAskBuiltinParks(t *testing.T) {
	core := NewAgentCore(model.NewFake(), domain.NewSeqIDGen())
	sink := &captureSink{}
	enabled := true
	ts := domain.ToolSet{Builtin: &domain.BuiltinToolset{
		DefaultEnabled: true,
		DefaultPolicy:  domain.PermissionPolicy{Type: "always_allow"},
		Configs: []domain.BuiltinConfig{{
			Name: "bash", Enabled: &enabled,
			Policy: &domain.PermissionPolicy{Type: "always_ask"},
		}},
	}}
	// Only bash is offered so model.NewFake requests it first.
	ts.Builtin.DefaultEnabled = false
	outcome, err := core.Run(context.Background(), RunRequest{
		Trigger:       domain.Event{Type: domain.EvUserMessage},
		Messages:      []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: "text", Text: "run ls"}}}},
		ToolSet:       ts,
		AgentSnapshot: domain.Agent{Model: domain.Model{ID: "m"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.RequiresAction || len(outcome.ActionEventIDs) != 1 {
		t.Fatalf("outcome = %#v, want RequiresAction with one id", outcome)
	}
	var evt domain.Event
	for _, e := range sink.events {
		if e.Type == domain.EvAgentToolUse {
			evt = e
		}
	}
	if evt.ID == "" {
		t.Fatalf("no agent.tool_use emitted; drafts = %v", draftTypes(sink))
	}
	if evt.Payload["evaluated_permission"] != "ask" {
		t.Fatalf("evaluated_permission = %v, want ask", evt.Payload["evaluated_permission"])
	}
	if evt.ID != outcome.ActionEventIDs[0] {
		t.Fatalf("ActionEventIDs = %v, want [%s]", outcome.ActionEventIDs, evt.ID)
	}
}
