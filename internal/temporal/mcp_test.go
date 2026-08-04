package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/mcpclient"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

type fakeMCPClient struct {
	tools  []mcpclient.Tool
	result mcpclient.Result
	server domain.MCPServer
	name   string
	input  map[string]any
	err    error
}

func (f *fakeMCPClient) Discover(
	context.Context,
	domain.MCPServer,
) ([]mcpclient.Tool, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]mcpclient.Tool(nil), f.tools...), nil
}

func (f *fakeMCPClient) Call(
	_ context.Context,
	server domain.MCPServer,
	name string,
	input map[string]any,
) (mcpclient.Result, error) {
	f.server = server
	f.name = name
	f.input = input
	return f.result, nil
}

type mcpPrepareSource struct {
	*fakeSource
	session domain.Session
}

func (s *mcpPrepareSource) GetSession(
	context.Context,
	string,
) (domain.Session, error) {
	return s.session, nil
}

func TestPrepareTurn_DiscoversAndPinsMCPTools(t *testing.T) {
	source := &mcpPrepareSource{
		fakeSource: newFakeSource([]domain.Event{{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "check github"},
			}},
		}}),
		session: domain.Session{
			ID:     "sess_mcp",
			Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				MCPServers: []any{map[string]any{
					"type": "url", "name": "github",
					"url": "https://mcp.example.com",
				}},
				Tools: []any{map[string]any{
					"type":            "mcp_toolset",
					"mcp_server_name": "github",
				}},
			},
		},
	}
	client := &fakeMCPClient{tools: []mcpclient.Tool{{
		Name:        "list_issues",
		Description: "List repository issues",
		InputSchema: map[string]any{"type": "object"},
	}}}
	activities := NewActivities(
		nil,
		source,
		nil,
		nil,
		&testIDGen{},
	).WithMCPClient(client)
	prepared, err := activities.PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID:      "sess_mcp",
			TriggerEventID: "sevt_user",
		},
	)
	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.Equal(t, []modelToolSummary{{
		Name: "mcp__github__list_issues",
		Type: "",
	}}, summarizeModelTools(prepared.Request.Tools))
	require.Equal(t, []TurnTool{{
		Name: "mcp__github__list_issues",
		Kind: TurnToolMCP,
		Permission: domain.PermissionPolicy{
			Type: "always_ask",
		},
		MCPServer: domain.MCPServer{
			Name: "github", URL: "https://mcp.example.com",
		},
		MCPToolName: "list_issues",
	}}, prepared.Tools)
}

func TestPrepareTurn_MCPDiscoveryFailureIsRecoverable(t *testing.T) {
	source := &mcpPrepareSource{
		fakeSource: newFakeSource([]domain.Event{{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "continue without github"},
			}},
		}}),
		session: domain.Session{
			ID:     "sess_mcp",
			Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				MCPServers: []any{map[string]any{
					"type": "url", "name": "github",
					"url": "https://mcp.example.com",
				}},
				Tools: []any{map[string]any{
					"type":            "mcp_toolset",
					"mcp_server_name": "github",
				}},
			},
		},
	}
	activities := NewActivities(
		nil,
		source,
		nil,
		nil,
		&testIDGen{},
	).WithMCPClient(&fakeMCPClient{err: errors.New("dial failed")})

	prepared, err := activities.PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID:      "sess_mcp",
			TriggerEventID: "sevt_user",
		},
	)
	require.NoError(t, err)
	require.Empty(t, prepared.FatalError)
	require.Empty(t, prepared.Request.Tools)
	require.Empty(t, prepared.Tools)
	require.Len(t, prepared.PreludeEvents, 1)
	require.Equal(t, domain.EvSessionError, prepared.PreludeEvents[0].Type)
	errorPayload := prepared.PreludeEvents[0].Payload["error"].(map[string]any)
	require.Equal(t, "mcp_connection_failed_error", errorPayload["type"])
	require.Equal(t, "github", errorPayload["mcp_server_name"])
	require.Equal(t, "exhausted", errorPayload["retry_status"].(map[string]any)["type"])
}

func TestPrepareTurn_MCPAliasCollisionIsFatalNotRetryable(t *testing.T) {
	source := &mcpPrepareSource{
		fakeSource: newFakeSource([]domain.Event{{
			ID: "sevt_user", Sequence: 1, Type: domain.EvUserMessage,
			Payload: map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "use MCP"},
			}},
		}}),
		session: domain.Session{
			ID: "sess_collision", Status: domain.StatusRunning,
			AgentSnapshot: domain.Agent{
				Model: domain.Model{ID: "model"},
				MCPServers: []any{map[string]any{
					"type": "url", "name": "server",
					"url": "https://mcp.example.com",
				}},
				Tools: []any{map[string]any{
					"type": "mcp_toolset", "mcp_server_name": "server",
				}},
			},
		},
	}
	client := &fakeMCPClient{tools: []mcpclient.Tool{
		{Name: "get.user", InputSchema: map[string]any{"type": "object"}},
		{Name: "get user", InputSchema: map[string]any{"type": "object"}},
	}}

	prepared, err := NewActivities(
		nil,
		source,
		nil,
		nil,
		&testIDGen{},
	).WithMCPClient(client).PrepareTurn(
		context.Background(),
		PrepareTurnInput{
			SessionID: "sess_collision", TriggerEventID: "sevt_user",
		},
	)

	require.NoError(t, err)
	require.Contains(t, prepared.FatalError, "MCP model tool name collision")
}

type modelToolSummary struct {
	Name string
	Type string
}

func summarizeModelTools(tools []model.ToolSchema) []modelToolSummary {
	out := make([]modelToolSummary, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modelToolSummary{Name: tool.Name, Type: tool.Type})
	}
	return out
}

type memoryMCPJournal struct {
	mu     sync.Mutex
	step   domain.ToolStep
	result domain.ToolStepResult
}

func (*memoryMCPJournal) EnsureAttempt(
	context.Context,
	string,
	string,
	string,
) error {
	return nil
}

func (j *memoryMCPJournal) EnsureToolStep(
	_ context.Context,
	attemptID string,
	stepID string,
	ordinal int,
	eventID string,
	name string,
	input map[string]any,
) (domain.ToolStep, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.step.ID == "" {
		j.step = domain.ToolStep{
			ID: stepID, AttemptID: attemptID, Ordinal: ordinal,
			ToolUseEventID: eventID, ToolName: name, Input: input,
			State: domain.ToolStepPrepared,
		}
	}
	return j.step, nil
}

func (j *memoryMCPJournal) StartToolStep(context.Context, string) error {
	j.mu.Lock()
	j.step.State = domain.ToolStepStarted
	j.mu.Unlock()
	return nil
}

func (j *memoryMCPJournal) CompleteToolStep(
	_ context.Context,
	_ string,
	result domain.ToolStepResult,
) error {
	j.mu.Lock()
	j.step.State = domain.ToolStepCompleted
	j.step.Result = &result
	j.result = result
	j.mu.Unlock()
	return nil
}

func (j *memoryMCPJournal) MarkToolStepAmbiguous(
	context.Context,
	string,
) error {
	j.mu.Lock()
	j.step.State = domain.ToolStepAmbiguous
	j.mu.Unlock()
	return nil
}

type fixedSandboxLease struct {
	box  sandbox.Sandbox
	spec sandbox.Spec
}

func (l *fixedSandboxLease) Acquire(
	_ context.Context,
	_ string,
	spec sandbox.Spec,
) (sandbox.Sandbox, error) {
	l.spec = spec
	return l.box, nil
}

func (*fixedSandboxLease) Release(context.Context, string) error { return nil }

type skillExecutionSource struct {
	*mcpPrepareSource
	skills []domain.SkillVersion
}

func (s *skillExecutionSource) SessionSkillsForRuntime(
	context.Context,
	string,
) ([]domain.SkillVersion, error) {
	return append([]domain.SkillVersion(nil), s.skills...), nil
}

func TestExecuteTool_RuntimeSkillLoadsFullInstructionsWithoutReadTool(t *testing.T) {
	ctx := context.Background()
	_, box, err := sandbox.NewLocalProvider().Create(ctx, t.Name(), sandbox.Spec{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })

	const body = "---\nname: report-tools\ndescription: Analyze reports\n---\n\nFollow the complete report workflow.\n"
	require.NoError(t, box.WriteFile(
		ctx,
		"skills/report-tools/SKILL.md",
		[]byte(body),
	))

	journal := &memoryMCPJournal{}
	source := &skillExecutionSource{
		mcpPrepareSource: &mcpPrepareSource{
			fakeSource: newFakeSource(nil),
			session: domain.Session{
				ID: "sess_skill_execute", AgentSnapshot: domain.Agent{
					Skills: []domain.SkillReference{{
						Type: "custom", SkillID: "skill_reports", Version: "100",
					}},
				},
			},
		},
		skills: []domain.SkillVersion{{
			SkillID: "skill_reports", Version: "100", Name: "report-tools",
		}},
	}
	activities := NewActivities(
		nil,
		source,
		journal,
		&fixedSandboxLease{box: box},
		&testIDGen{},
	)

	result, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID: "sess_skill_execute", TriggerEventID: "sevt_trigger",
		AttemptID: "ratm_skill", Ordinal: 0,
		ToolUseEventID: "sevt_skill_use", ToolStepID: "tstep_skill",
		ToolName: agentruntime.RuntimeSkillToolName,
		ToolKind: TurnToolRuntimeSkill,
		Input:    map[string]any{"skill": "report-tools"},
	})
	require.NoError(t, err)
	require.False(t, result.Ambiguous)
	require.False(t, result.Result.IsError)
	require.Equal(t, "Launching skill: report-tools", result.Result.Content[0].(map[string]any)["text"])
	require.Len(t, result.Result.InjectedContent, 1)
	require.Contains(t, result.Result.InjectedContent[0].Text, body)
	require.Equal(t, result.Result, journal.result)

	// A completed Activity retry recovers the exact injected body from the
	// durable journal without reacquiring or re-reading the sandbox.
	recovered, err := activities.ExecuteTool(ctx, ExecuteToolInput{
		SessionID: "sess_skill_execute", TriggerEventID: "sevt_trigger",
		AttemptID: "ratm_skill", Ordinal: 0,
		ToolUseEventID: "sevt_skill_use", ToolStepID: "tstep_skill",
		ToolName: agentruntime.RuntimeSkillToolName,
		ToolKind: TurnToolRuntimeSkill,
		Input:    map[string]any{"skill": "report-tools"},
	})
	require.NoError(t, err)
	require.Equal(t, result.Result, recovered.Result)
}

func TestExecuteTool_RuntimeSkillStartedStepIsSafelyReloaded(t *testing.T) {
	ctx := context.Background()
	_, box, err := sandbox.NewLocalProvider().Create(ctx, t.Name(), sandbox.Spec{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })
	require.NoError(t, box.WriteFile(
		ctx,
		"skills/report-tools/SKILL.md",
		[]byte("---\nname: report-tools\ndescription: Analyze reports\n---\nbody\n"),
	))

	journal := &memoryMCPJournal{step: domain.ToolStep{
		ID: "tstep_started_skill", AttemptID: "ratm_started_skill",
		Ordinal: 0, ToolUseEventID: "sevt_started_skill",
		ToolName: agentruntime.RuntimeSkillToolName,
		Input:    map[string]any{"skill": "report-tools"},
		State:    domain.ToolStepStarted,
	}}
	source := &skillExecutionSource{
		mcpPrepareSource: &mcpPrepareSource{
			fakeSource: newFakeSource(nil),
			session:    domain.Session{ID: "sess_started_skill"},
		},
		skills: []domain.SkillVersion{{Name: "report-tools"}},
	}
	result, err := NewActivities(
		nil, source, journal, &fixedSandboxLease{box: box}, &testIDGen{},
	).ExecuteTool(ctx, ExecuteToolInput{
		SessionID: "sess_started_skill", TriggerEventID: "sevt_trigger",
		AttemptID: "ratm_started_skill", Ordinal: 0,
		ToolUseEventID: "sevt_started_skill", ToolStepID: "tstep_started_skill",
		ToolName: agentruntime.RuntimeSkillToolName,
		ToolKind: TurnToolRuntimeSkill,
		Input:    map[string]any{"skill": "report-tools"},
	})
	require.NoError(t, err)
	require.False(t, result.Ambiguous)
	require.False(t, result.Result.IsError)
	require.Len(t, result.Result.InjectedContent, 1)
}

func TestExecuteTool_MCPJournalsRawAndProjectsModelContent(t *testing.T) {
	_, box, err := sandbox.NewLocalProvider().Create(
		context.Background(),
		t.Name(),
		sandbox.Spec{},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })
	journal := &memoryMCPJournal{}
	lease := &fixedSandboxLease{box: box}
	client := &fakeMCPClient{result: mcpclient.Result{
		Raw: json.RawMessage(`{
			"_meta":{"trace":"private"},
			"content":[{"type":"text","text":"42"}]
		}`),
	}}
	activities := NewActivities(
		nil,
		&mcpPrepareSource{session: domain.Session{ID: "sess_mcp"}},
		journal,
		lease,
		&testIDGen{},
	).WithMCPClient(client)
	result, err := activities.ExecuteTool(
		context.Background(),
		ExecuteToolInput{
			SessionID:      "sess_mcp",
			TriggerEventID: "sevt_trigger",
			AttemptID:      "ratm_1",
			ToolUseEventID: "sevt_tool",
			ToolStepID:     "tstep_1",
			ToolName:       "mcp__github__answer",
			ToolKind:       TurnToolMCP,
			MCPServer: domain.MCPServer{
				Name: "github", URL: "https://mcp.example.com",
			},
			MCPToolName: "answer",
			Input:       map[string]any{"question": "life"},
		},
	)
	require.NoError(t, err)
	require.False(t, result.Result.IsError)
	require.Equal(t, "github", client.server.Name)
	require.Equal(t, "answer", client.name)
	require.Contains(
		t,
		result.Result.Content[0].(map[string]any)["text"],
		"42",
	)
	require.Contains(t, string(journal.result.Raw), `"trace":"private"`)
	require.Empty(t, result.Result.Raw)
	require.Empty(t, result.Result.RawPath)
	require.Equal(t, defaultCloudSandboxNetwork, lease.spec.Network)
	require.NotContains(
		t,
		result.Result.Content[0].(map[string]any)["text"],
		"private",
	)
}
