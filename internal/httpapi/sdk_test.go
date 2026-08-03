package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// These tests drive the server through the official Anthropic Go SDK as a
// black-box compatibility client. The SDK's own request/response types are the
// contract; if the SDK can construct a request and decode our response without
// error, our wire shape matches what the SDK expects. The server runs in strict
// mode so the SDK's automatic anthropic-beta header and x-api-key are exercised.
//
// SDK-expressible JSON is asserted here; wire details the SDK cannot express
// (e.g. exact top-level event union flattening) are covered by the raw-HTTP
// golden tests in sdk_golden_test.go.

func sdkClientAndServer(t *testing.T) (anthropic.Client, *httptest.Server) {
	t.Helper()
	handler := newTestHandler(t, Config{
		RequireBeta: true, RequireAuth: true, RequireVersion: true, RequireContentType: true,
	}, false)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	client := anthropic.NewClient(
		option.WithBaseURL(ts.URL),
		option.WithAPIKey("sk-test"),
	)
	return client, ts
}

func TestSDK_AgentLifecycle(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()

	// Create.
	agent, err := client.Beta.Agents.New(ctx, anthropic.BetaAgentNewParams{
		Name: "SRE Agent",
		Model: anthropic.BetaManagedAgentsModelConfigParams{
			ID: anthropic.BetaManagedAgentsModelClaudeOpus4_8,
		},
		System:   anthropic.String("help"),
		Metadata: map[string]string{"team": "sre"},
		Tools: []anthropic.BetaAgentNewParamsToolUnion{{
			OfCustom: &anthropic.BetaManagedAgentsCustomToolParams{
				Description: "Look up the current service status.",
				InputSchema: anthropic.BetaManagedAgentsCustomToolInputSchemaParam{
					Properties: map[string]any{"service": map[string]any{"type": "string"}},
					Required:   []string{"service"},
				},
				Name: "get_service_status",
				Type: anthropic.BetaManagedAgentsCustomToolParamsTypeCustom,
			},
		}},
		Multiagent: anthropic.BetaManagedAgentsMultiagentParams{
			Type: anthropic.BetaManagedAgentsMultiagentParamsTypeCoordinator,
			Agents: []anthropic.BetaManagedAgentsMultiagentRosterEntryParamsUnion{{
				OfBetaManagedAgentsAgents: &anthropic.BetaManagedAgentsAgentParams{
					ID:      "agent_peer",
					Type:    anthropic.BetaManagedAgentsAgentParamsTypeAgent,
					Version: anthropic.Int(1),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if agent.ID == "" {
		t.Fatal("created agent has empty id")
	}
	if agent.Type != "agent" {
		t.Fatalf("agent type = %q, want agent", agent.Type)
	}
	if agent.Version != 1 {
		t.Fatalf("new agent version = %d, want 1", agent.Version)
	}
	if agent.Model.ID != anthropic.BetaManagedAgentsModelClaudeOpus4_8 {
		t.Fatalf("model id = %q", agent.Model.ID)
	}
	if agent.System != "help" {
		t.Fatalf("system = %q, want help", agent.System)
	}
	if agent.Metadata["team"] != "sre" {
		t.Fatalf("metadata = %#v, want team=sre", agent.Metadata)
	}
	if len(agent.Tools) != 1 || agent.Tools[0].Type != "custom" ||
		agent.Tools[0].Name != "get_service_status" ||
		agent.Tools[0].Description != "Look up the current service status." ||
		agent.Tools[0].InputSchema.Type != "object" {
		t.Fatalf("custom tool response = %#v", agent.Tools)
	}
	if agent.Multiagent.Type != anthropic.BetaManagedAgentsMultiagentTypeCoordinator ||
		len(agent.Multiagent.Agents) != 1 ||
		agent.Multiagent.Agents[0].ID != "agent_peer" ||
		agent.Multiagent.Agents[0].Version != 1 {
		t.Fatalf("multiagent response = %#v", agent.Multiagent)
	}
	assertRawObjectHasFields(t, agent.RawJSON(), "multiagent")
	if agent.JSON.Multiagent.Raw() == "" {
		t.Fatal("agent response omitted multiagent")
	}

	// Get.
	got, err := client.Beta.Agents.Get(ctx, agent.ID, anthropic.BetaAgentGetParams{})
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.ID != agent.ID || got.Version != 1 {
		t.Fatalf("get returned %s v%d", got.ID, got.Version)
	}

	// Update -> version increments to 2 (official `version` optimistic field).
	updated, err := client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
		Name:    anthropic.String("SRE Agent v2"),
		Version: anthropic.Int(1),
		Multiagent: anthropic.BetaManagedAgentsMultiagentParams{
			Type: anthropic.BetaManagedAgentsMultiagentParamsTypeCoordinator,
			Agents: []anthropic.BetaManagedAgentsMultiagentRosterEntryParamsUnion{{
				OfBetaManagedAgentsAgents: &anthropic.BetaManagedAgentsAgentParams{
					ID:      "agent_peer_v2",
					Type:    anthropic.BetaManagedAgentsAgentParamsTypeAgent,
					Version: anthropic.Int(2),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("update agent: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated version = %d, want 2", updated.Version)
	}
	if updated.Name != "SRE Agent v2" {
		t.Fatalf("updated name = %q", updated.Name)
	}
	if len(updated.Multiagent.Agents) != 1 ||
		updated.Multiagent.Agents[0].ID != "agent_peer_v2" ||
		updated.Multiagent.Agents[0].Version != 2 {
		t.Fatalf("updated multiagent = %#v", updated.Multiagent)
	}

	// Update with a stale version must conflict.
	_, err = client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
		Name:    anthropic.String("stale"),
		Version: anthropic.Int(1),
	})
	if err == nil {
		t.Fatal("expected version-conflict error on stale update, got nil")
	}
	assertAPIStatus(t, err, 409)

	// List.
	list, err := client.Beta.Agents.List(ctx, anthropic.BetaAgentListParams{})
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list returned %d agents, want 1", len(list.Data))
	}
	if list.Data[0].Version != 2 {
		t.Fatalf("listed agent version = %d, want latest 2", list.Data[0].Version)
	}

	// Archive.
	archived, err := client.Beta.Agents.Archive(ctx, agent.ID, anthropic.BetaAgentArchiveParams{})
	if err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if archived.ArchivedAt.IsZero() {
		t.Fatal("archived agent has zero archived_at")
	}
	if archived.Version != updated.Version {
		t.Fatalf("archive changed configuration version: got %d want %d", archived.Version, updated.Version)
	}
	_, err = client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
		Name: anthropic.String("must fail"),
	})
	if err == nil {
		t.Fatal("expected archived agent update to fail")
	}
	assertAPIStatus(t, err, 400)
}

func TestSDK_SessionLifecycleAndSnapshot(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "system prompt A")
	env := mustEnv(t, ts.URL)

	// Create with the string (latest) agent form.
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
		Title:         anthropic.String("Order #1234 inquiry"),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.Type != "session" {
		t.Fatalf("session type = %q", session.Type)
	}
	if session.Status != anthropic.BetaManagedAgentsSessionStatusIdle {
		t.Fatalf("new session status = %q, want idle", session.Status)
	}
	if session.Agent.ID != agent.ID || session.Agent.Version != 1 {
		t.Fatalf("session agent snapshot = %s v%d", session.Agent.ID, session.Agent.Version)
	}
	if session.Agent.System != "system prompt A" {
		t.Fatalf("snapshot system = %q", session.Agent.System)
	}
	if session.Title != "Order #1234 inquiry" {
		t.Fatalf("title = %q", session.Title)
	}
	assertRawObjectHasFields(t, session.RawJSON(),
		"outcome_evaluations", "resources", "stats", "usage", "vault_ids", "deployment_id")
	assertRawObjectHasFields(t, session.Agent.RawJSON(), "multiagent")
	if !session.JSON.OutcomeEvaluations.Valid() || !session.JSON.Resources.Valid() ||
		!session.JSON.Stats.Valid() || !session.JSON.Usage.Valid() || !session.JSON.VaultIDs.Valid() {
		t.Fatal("session response contains a missing or invalid required collection/stats field")
	}
	if session.JSON.DeploymentID.Raw() == "" {
		t.Fatal("session response omitted nullable deployment_id")
	}
	if session.Agent.JSON.Multiagent.Raw() == "" {
		t.Fatal("session agent snapshot omitted multiagent")
	}

	// Mutating the underlying agent must not change the existing snapshot.
	if _, err := client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
		System:  anthropic.String("system prompt B"),
		Version: anthropic.Int(1),
	}); err != nil {
		t.Fatalf("update agent: %v", err)
	}
	got, err := client.Beta.Sessions.Get(ctx, session.ID, anthropic.BetaSessionGetParams{})
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Agent.System != "system prompt A" {
		t.Fatalf("snapshot mutated after agent update: system = %q, want stable A", got.Agent.System)
	}
	if got.Agent.Version != 1 {
		t.Fatalf("snapshot version drifted to %d, want pinned 1", got.Agent.Version)
	}

	// List sessions.
	list, err := client.Beta.Sessions.List(ctx, anthropic.BetaSessionListParams{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != session.ID {
		t.Fatalf("list sessions returned %d rows", len(list.Data))
	}

	deleted, err := client.Beta.Sessions.Delete(ctx, session.ID, anthropic.BetaSessionDeleteParams{})
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if deleted.ID != session.ID || deleted.Type != anthropic.BetaManagedAgentsDeletedSessionTypeSessionDeleted {
		t.Fatalf("deleted session response = %+v", deleted)
	}
}

func TestSDK_SessionPinnedVersion(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "v1 system")
	// Bump to version 2 with a different system prompt.
	if _, err := client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
		System:  anthropic.String("v2 system"),
		Version: anthropic.Int(1),
	}); err != nil {
		t.Fatalf("update agent: %v", err)
	}
	env := mustEnv(t, ts.URL)

	// Pin the session to version 1.
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent: anthropic.BetaSessionNewParamsAgentUnion{
			OfBetaManagedAgentsAgents: &anthropic.BetaManagedAgentsAgentParams{
				Type:    anthropic.BetaManagedAgentsAgentParamsTypeAgent,
				ID:      agent.ID,
				Version: anthropic.Int(1),
			},
		},
		EnvironmentID: env,
	})
	if err != nil {
		t.Fatalf("create pinned session: %v", err)
	}
	if session.Agent.Version != 1 {
		t.Fatalf("pinned snapshot version = %d, want 1", session.Agent.Version)
	}
	if session.Agent.System != "v1 system" {
		t.Fatalf("pinned snapshot system = %q, want v1 system", session.Agent.System)
	}
}

func TestSDK_SessionTitleUpdateEmitsChangedFieldsEvent(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
		Title:         anthropic.String("before"),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	updated, err := client.Beta.Sessions.Update(ctx, session.ID, anthropic.BetaSessionUpdateParams{
		Title: anthropic.String("after"),
	})
	if err != nil {
		t.Fatalf("update session: %v", err)
	}
	if updated.Title != "after" {
		t.Fatalf("updated title = %q", updated.Title)
	}
	// A same-value request is a no-op and must not emit a second event.
	if _, err := client.Beta.Sessions.Update(ctx, session.ID, anthropic.BetaSessionUpdateParams{
		Title: anthropic.String("after"),
	}); err != nil {
		t.Fatalf("no-op update session: %v", err)
	}

	page, err := client.Beta.Sessions.Events.List(ctx, session.ID, anthropic.BetaSessionEventListParams{
		Types: []string{"session.updated"},
	})
	if err != nil {
		t.Fatalf("list update events: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("session.updated count = %d, want 1", len(page.Data))
	}
	event := page.Data[0].AsSessionUpdated()
	if event.Title != "after" || !event.JSON.Title.Valid() {
		t.Fatalf("session.updated title = %q, raw=%s", event.Title, event.RawJSON())
	}
}

func TestSDK_SessionAgentAndMetadataUpdate(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
		Metadata:      map[string]string{"keep": "yes"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	updated, err := client.Beta.Sessions.Update(ctx, session.ID, anthropic.BetaSessionUpdateParams{
		Metadata: map[string]string{"added": "new"},
		Agent: anthropic.BetaManagedAgentsSessionAgentUpdateParam{
			Tools: []anthropic.BetaManagedAgentsSessionAgentUpdateToolUnionParam{
				{
					OfAgentToolset20260401: &anthropic.BetaManagedAgentsAgentToolset20260401Params{
						Type: anthropic.BetaManagedAgentsAgentToolset20260401ParamsTypeAgentToolset20260401,
					},
				},
				{
					OfMCPToolset: &anthropic.BetaManagedAgentsMCPToolsetParams{
						Type:          anthropic.BetaManagedAgentsMCPToolsetParamsTypeMCPToolset,
						MCPServerName: "linear",
					},
				},
			},
			MCPServers: []anthropic.BetaManagedAgentsURLMCPServerParams{
				{
					Type: anthropic.BetaManagedAgentsURLMCPServerParamsTypeURL,
					Name: "linear",
					URL:  "https://mcp.example.com/sse",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("update session: %v", err)
	}
	if len(updated.Agent.Tools) != 2 || len(updated.Agent.MCPServers) != 1 {
		t.Fatalf("updated snapshot tools=%d servers=%d, raw=%s",
			len(updated.Agent.Tools), len(updated.Agent.MCPServers), updated.RawJSON())
	}
	if updated.Agent.Version != 1 {
		t.Fatalf("session-local update renumbered the agent: %d", updated.Agent.Version)
	}
	if updated.Metadata["keep"] != "yes" || updated.Metadata["added"] != "new" {
		t.Fatalf("metadata patch = %v", updated.Metadata)
	}

	// The underlying agent resource is unchanged.
	resource, err := client.Beta.Agents.Get(ctx, agent.ID, anthropic.BetaAgentGetParams{})
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if resource.Version != 1 || len(resource.Tools) != 0 {
		t.Fatalf("session update propagated to the agent: version=%d tools=%d",
			resource.Version, len(resource.Tools))
	}

	page, err := client.Beta.Sessions.Events.List(ctx, session.ID,
		anthropic.BetaSessionEventListParams{Types: []string{"session.updated"}})
	if err != nil {
		t.Fatalf("list update events: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("session.updated count = %d, want 1", len(page.Data))
	}
	event := page.Data[0].AsSessionUpdated()
	if !event.JSON.Agent.Valid() || event.Agent.ID != agent.ID {
		t.Fatalf("session.updated agent = %s", event.RawJSON())
	}
	if !event.JSON.Metadata.Valid() || event.Metadata["added"] != "new" {
		t.Fatalf("session.updated metadata = %s", event.RawJSON())
	}
	if event.JSON.Title.Valid() {
		t.Fatalf("session.updated carries an unchanged title: %s", event.RawJSON())
	}
}

func TestSDK_SessionUpdateRejectsVaultIDs(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err = client.Beta.Sessions.Update(ctx, session.ID, anthropic.BetaSessionUpdateParams{
		VaultIDs: []string{"vlt_1"},
	})
	if err == nil {
		t.Fatal("expected vault_ids to be rejected")
	}
	assertAPIStatus(t, err, 422)
}

func TestSDK_EventSendAndList(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Send a user.message with content[] blocks.
	sent, err := client.Beta.Sessions.Events.Send(ctx, session.ID, anthropic.BetaSessionEventSendParams{
		Events: []anthropic.BetaManagedAgentsEventParamsUnion{{
			OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
				Type: anthropic.BetaManagedAgentsUserMessageEventParamsTypeUserMessage,
				Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{
					OfText: &anthropic.BetaManagedAgentsTextBlockParam{
						Type: anthropic.BetaManagedAgentsTextBlockTypeText,
						Text: "Where is my order #1234?",
					},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("send event: %v", err)
	}
	if len(sent.Data) != 1 {
		t.Fatalf("send echoed %d events, want 1", len(sent.Data))
	}
	if sent.Data[0].Type != "user.message" || sent.Data[0].ID == "" {
		t.Fatalf("echoed event = %+v", sent.Data[0])
	}

	// Poll list until the fake runtime's agent.message + status_idle land.
	deadline := time.Now().Add(3 * time.Second)
	var userText string
	var sawIdle bool
	for time.Now().Before(deadline) {
		page, err := client.Beta.Sessions.Events.List(ctx, session.ID, anthropic.BetaSessionEventListParams{})
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		userText, sawIdle = "", false
		for _, ev := range page.Data {
			switch ev.Type {
			case "user.message":
				for _, block := range ev.AsUserMessage().Content {
					if block.Type == "text" {
						userText = block.AsText().Text
					}
				}
			case "session.status_idle":
				sawIdle = true
			}
		}
		if userText != "" && sawIdle {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if userText != "Where is my order #1234?" {
		t.Fatalf("listed user.message text = %q", userText)
	}
	if !sawIdle {
		t.Fatal("never observed session.status_idle in listed events")
	}
}

func TestSDK_EventStream(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	stream := client.Beta.Sessions.Events.StreamEvents(
		ctx, session.ID, anthropic.BetaSessionEventStreamParams{},
	)
	defer stream.Close()

	sent, err := client.Beta.Sessions.Events.Send(ctx, session.ID, anthropic.BetaSessionEventSendParams{
		Events: []anthropic.BetaManagedAgentsEventParamsUnion{{
			OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
				Type: anthropic.BetaManagedAgentsUserMessageEventParamsTypeUserMessage,
				Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{
					OfText: &anthropic.BetaManagedAgentsTextBlockParam{
						Type: anthropic.BetaManagedAgentsTextBlockTypeText,
						Text: "stream me",
					},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("send event: %v", err)
	}
	if len(sent.Data) != 1 {
		t.Fatalf("sent event count = %d, want 1", len(sent.Data))
	}

	if !stream.Next() {
		t.Fatalf("official SDK returned no streamed event: %v", stream.Err())
	}
	got := stream.Current()
	if got.Type != "user.message" || got.ID != sent.Data[0].ID {
		t.Fatalf("streamed event = %s %s, want user.message %s", got.Type, got.ID, sent.Data[0].ID)
	}
}

func TestSDK_EventListPaginationAndTypesFilter(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	agent := mustAgent(t, client, "opus", "sys")
	env := mustEnv(t, ts.URL)
	session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
		Agent:         anthropic.BetaSessionNewParamsAgentUnion{OfString: anthropic.String(agent.ID)},
		EnvironmentID: env,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Three user.message events, no runtime trigger side effects to reason about
	// (fake still runs, but we filter to user.message below).
	for i := 0; i < 3; i++ {
		if _, err := client.Beta.Sessions.Events.Send(ctx, session.ID, anthropic.BetaSessionEventSendParams{
			Events: []anthropic.BetaManagedAgentsEventParamsUnion{{
				OfUserMessage: &anthropic.BetaManagedAgentsUserMessageEventParams{
					Type: anthropic.BetaManagedAgentsUserMessageEventParamsTypeUserMessage,
					Content: []anthropic.BetaManagedAgentsUserMessageEventParamsContentUnion{{
						OfText: &anthropic.BetaManagedAgentsTextBlockParam{
							Type: anthropic.BetaManagedAgentsTextBlockTypeText,
							Text: "msg",
						},
					}},
				},
			}},
		}); err != nil {
			t.Fatalf("send event %d: %v", i, err)
		}
	}

	// Filter to user.message and page with limit=2: first page has 2 + a cursor.
	first, err := client.Beta.Sessions.Events.List(ctx, session.ID, anthropic.BetaSessionEventListParams{
		Types: []string{"user.message"},
		Limit: anthropic.Int(2),
	})
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if len(first.Data) != 2 {
		t.Fatalf("page 1 returned %d events, want 2", len(first.Data))
	}
	for _, ev := range first.Data {
		if ev.Type != "user.message" {
			t.Fatalf("types filter leaked %q", ev.Type)
		}
	}
	if first.NextPage == "" {
		t.Fatal("expected a next_page cursor on page 1")
	}

	// Second page: the remaining user.message.
	second, err := client.Beta.Sessions.Events.List(ctx, session.ID, anthropic.BetaSessionEventListParams{
		Types: []string{"user.message"},
		Limit: anthropic.Int(2),
		Page:  anthropic.String(first.NextPage),
	})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(second.Data) != 1 {
		t.Fatalf("page 2 returned %d events, want 1", len(second.Data))
	}
	if second.NextPage != "" {
		t.Fatalf("page 2 next_page = %q, want empty (last page)", second.NextPage)
	}
	// No overlap between pages.
	if first.Data[0].ID == second.Data[0].ID || first.Data[1].ID == second.Data[0].ID {
		t.Fatal("page 2 overlaps page 1")
	}
}

func TestSDK_AgentListParamsAndPaging(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()
	created := map[string]bool{}
	for range 3 {
		agent := mustAgent(t, client, "opus", "system")
		created[agent.ID] = true
	}

	first, err := client.Beta.Agents.List(ctx, anthropic.BetaAgentListParams{
		Limit:           anthropic.Int(2),
		IncludeArchived: anthropic.Bool(false),
		CreatedAtGte:    anthropic.Time(time.Unix(0, 0).UTC()),
		CreatedAtLte:    anthropic.Time(time.Unix(1<<31, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("list agents page 1: %v", err)
	}
	if len(first.Data) != 2 || first.NextPage == "" {
		t.Fatalf("page 1 = %d agents, next_page %q", len(first.Data), first.NextPage)
	}
	second, err := first.GetNextPage()
	if err != nil {
		t.Fatalf("follow agent next_page: %v", err)
	}
	if second == nil || len(second.Data) != 1 || second.NextPage != "" {
		t.Fatalf("page 2 = %+v, want one terminal row", second)
	}
	for _, agent := range append(first.Data, second.Data...) {
		if !created[agent.ID] {
			t.Fatalf("unexpected agent %s", agent.ID)
		}
		delete(created, agent.ID)
	}
	if len(created) != 0 {
		t.Fatalf("agents missing from paged SDK result: %v", created)
	}
	if _, err := client.Beta.Agents.List(ctx, anthropic.BetaAgentListParams{
		Limit: anthropic.Int(101),
	}); err == nil {
		t.Fatal("limit=101 was accepted")
	} else {
		assertAPIStatus(t, err, 400)
	}
}

func TestSDK_AgentVersionListParamsAndPaging(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()
	agent := mustAgent(t, client, "opus", "system")
	for _, name := range []string{"Agent v2", "Agent v3"} {
		updated, err := client.Beta.Agents.Update(ctx, agent.ID, anthropic.BetaAgentUpdateParams{
			Name: anthropic.String(name), Version: anthropic.Int(agent.Version),
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		agent = updated
	}

	first, err := client.Beta.Agents.Versions.List(
		ctx, agent.ID, anthropic.BetaAgentVersionListParams{Limit: anthropic.Int(2)},
	)
	if err != nil {
		t.Fatalf("list Agent versions page 1: %v", err)
	}
	if len(first.Data) != 2 || first.Data[0].Version != 1 ||
		first.Data[1].Version != 2 || first.NextPage == "" {
		t.Fatalf("page 1 = %+v, want versions 1,2 and next_page", first)
	}
	second, err := first.GetNextPage()
	if err != nil {
		t.Fatalf("follow Agent versions next_page: %v", err)
	}
	if second == nil || len(second.Data) != 1 || second.Data[0].Version != 3 ||
		second.NextPage != "" {
		t.Fatalf("page 2 = %+v, want terminal version 3", second)
	}
	if _, err := client.Beta.Agents.Versions.List(
		ctx, agent.ID, anthropic.BetaAgentVersionListParams{Limit: anthropic.Int(101)},
	); err == nil {
		t.Fatal("Agent Versions limit=101 was accepted")
	} else {
		assertAPIStatus(t, err, 400)
	}
}

func TestSDK_EnvironmentLifecycle(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()

	environment, err := client.Beta.Environments.New(ctx, anthropic.BetaEnvironmentNewParams{
		Name:        "SDK environment",
		Description: anthropic.String("created through the official SDK"),
		Metadata:    map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	if environment.ID == "" || environment.Type != "environment" ||
		environment.Name != "SDK environment" ||
		environment.Description != "created through the official SDK" ||
		environment.Metadata["team"] != "platform" ||
		environment.Config.Type != "cloud" || environment.Config.Networking.Type != "unrestricted" {
		t.Fatalf("created environment = %#v", environment)
	}
	assertRawObjectHasFields(t, environment.RawJSON(),
		"id", "archived_at", "config", "created_at", "description", "metadata", "name", "type", "updated_at")
	assertRawObjectHasFields(t, environment.Config.RawJSON(), "type", "networking", "packages")
	assertRawObjectHasFields(t, environment.Config.Packages.RawJSON(),
		"type", "apt", "cargo", "gem", "go", "npm", "pip")

	got, err := client.Beta.Environments.Get(ctx, environment.ID, anthropic.BetaEnvironmentGetParams{})
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	if got.ID != environment.ID || got.Description != environment.Description ||
		got.Metadata["team"] != "platform" || got.Config.Networking.Type != "unrestricted" {
		t.Fatalf("retrieved environment = %#v", got)
	}

	archived, err := client.Beta.Environments.Archive(
		ctx, environment.ID, anthropic.BetaEnvironmentArchiveParams{},
	)
	if err != nil {
		t.Fatalf("archive environment: %v", err)
	}
	if archived.ArchivedAt == "" || archived.Description != environment.Description ||
		archived.Metadata["team"] != "platform" {
		t.Fatalf("archived environment = %#v", archived)
	}

	deleted, err := client.Beta.Environments.Delete(
		ctx, environment.ID, anthropic.BetaEnvironmentDeleteParams{},
	)
	if err != nil {
		t.Fatalf("delete environment: %v", err)
	}
	if deleted.ID != environment.ID ||
		deleted.Type != anthropic.BetaEnvironmentDeleteResponseTypeEnvironmentDeleted {
		t.Fatalf("delete response = %#v", deleted)
	}
}

func TestSDK_EnvironmentExplicitCloudDefaults(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()
	networking := anthropic.NewBetaUnrestrictedNetworkParam()
	cloud := anthropic.BetaCloudConfigParams{
		Networking: anthropic.BetaCloudConfigParamsNetworkingUnion{
			OfUnrestricted: &networking,
		},
		Packages: anthropic.BetaPackagesParams{
			Type: anthropic.BetaPackagesParamsTypePackages,
		},
	}

	environment, err := client.Beta.Environments.New(ctx, anthropic.BetaEnvironmentNewParams{
		Name: "Explicit cloud defaults",
		Config: anthropic.BetaEnvironmentNewParamsConfigUnion{
			OfCloud: &cloud,
		},
	})
	if err != nil {
		t.Fatalf("create environment with explicit defaults: %v", err)
	}
	if environment.Config.Networking.Type != "unrestricted" ||
		len(environment.Config.Packages.Pip) != 0 {
		t.Fatalf("created environment config = %#v", environment.Config)
	}

	updated, err := client.Beta.Environments.Update(
		ctx,
		environment.ID,
		anthropic.BetaEnvironmentUpdateParams{
			Config: anthropic.BetaEnvironmentUpdateParamsConfigUnion{
				OfCloud: &cloud,
			},
		},
	)
	if err != nil {
		t.Fatalf("update environment with explicit defaults: %v", err)
	}
	if updated.Config.Networking.Type != "unrestricted" ||
		len(updated.Config.Packages.Pip) != 0 {
		t.Fatalf("updated environment config = %#v", updated.Config)
	}
}

func TestSDK_SelfHostedEnvironmentScope(t *testing.T) {
	client, _ := sdkClientAndServer(t)
	ctx := context.Background()
	config := anthropic.NewBetaSelfHostedConfigParams()

	environment, err := client.Beta.Environments.New(ctx, anthropic.BetaEnvironmentNewParams{
		Name:        "Self-hosted SDK environment",
		Description: anthropic.String("before update"),
		Metadata:    map[string]string{"keep": "old", "drop": "value"},
		Config: anthropic.BetaEnvironmentNewParamsConfigUnion{
			OfSelfHosted: &config,
		},
		Scope: anthropic.BetaEnvironmentNewParamsScopeAccount,
	})
	if err != nil {
		t.Fatalf("create self-hosted environment: %v", err)
	}
	if environment.Config.Type != "self_hosted" ||
		environment.Scope != anthropic.BetaEnvironmentScopeAccount {
		t.Fatalf("self-hosted environment = %#v", environment)
	}
	assertRawObjectHasFields(t, environment.RawJSON(), "scope")

	updated, err := client.Beta.Environments.Update(
		ctx,
		environment.ID,
		anthropic.BetaEnvironmentUpdateParams{
			Name:        anthropic.String("Updated SDK environment"),
			Description: anthropic.String("after update"),
			Metadata:    map[string]string{"keep": "updated", "drop": ""},
			Scope:       anthropic.BetaEnvironmentUpdateParamsScopeOrganization,
			Config: anthropic.BetaEnvironmentUpdateParamsConfigUnion{
				OfSelfHosted: &config,
			},
		},
	)
	if err != nil {
		t.Fatalf("update self-hosted environment: %v", err)
	}
	if updated.Name != "Updated SDK environment" || updated.Description != "after update" ||
		updated.Scope != anthropic.BetaEnvironmentScopeOrganization ||
		len(updated.Metadata) != 1 || updated.Metadata["keep"] != "updated" {
		t.Fatalf("updated self-hosted environment = %#v", updated)
	}

	got, err := client.Beta.Environments.Get(ctx, environment.ID, anthropic.BetaEnvironmentGetParams{})
	if err != nil {
		t.Fatalf("get self-hosted environment: %v", err)
	}
	if got.Config.Type != "self_hosted" || got.Name != updated.Name ||
		got.Scope != anthropic.BetaEnvironmentScopeOrganization || got.Metadata["keep"] != "updated" {
		t.Fatalf("retrieved self-hosted environment = %#v", got)
	}
}

func TestSDK_EnvironmentListParamsAndPaging(t *testing.T) {
	client, server := sdkClientAndServer(t)
	ctx := context.Background()
	created := map[string]bool{}
	for range 3 {
		id := mustEnv(t, server.URL)
		created[id] = true
	}

	first, err := client.Beta.Environments.List(ctx, anthropic.BetaEnvironmentListParams{
		Limit:           anthropic.Int(2),
		IncludeArchived: anthropic.Bool(false),
	})
	if err != nil {
		t.Fatalf("list environments page 1: %v", err)
	}
	if len(first.Data) != 2 || first.NextPage == "" {
		t.Fatalf("page 1 = %d environments, next_page %q", len(first.Data), first.NextPage)
	}
	second, err := first.GetNextPage()
	if err != nil {
		t.Fatalf("follow environment next_page: %v", err)
	}
	if second == nil || len(second.Data) != 1 || second.NextPage != "" {
		t.Fatalf("page 2 = %+v, want one terminal row", second)
	}
	for _, environment := range append(first.Data, second.Data...) {
		if !created[environment.ID] {
			t.Fatalf("unexpected environment %s", environment.ID)
		}
		delete(created, environment.ID)
	}
	if len(created) != 0 {
		t.Fatalf("environments missing from paged SDK result: %v", created)
	}
}

func mustAgent(t *testing.T, client anthropic.Client, _ string, system string) *anthropic.BetaManagedAgentsAgent {
	t.Helper()
	agent, err := client.Beta.Agents.New(context.Background(), anthropic.BetaAgentNewParams{
		Name: "Agent",
		Model: anthropic.BetaManagedAgentsModelConfigParams{
			ID: anthropic.BetaManagedAgentsModelClaudeOpus4_8,
		},
		System: anthropic.String(system),
	})
	if err != nil {
		t.Fatalf("mustAgent: %v", err)
	}
	return agent
}
