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

// TestSDK_AgentListParamsAndPaging drives the documented List Agents query
// surface through the SDK's own BetaAgentListParams, which exposes exactly
// created_at[gte], created_at[lte], include_archived, limit, and page.
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
	if len(first.Data) != 2 {
		t.Fatalf("page 1 returned %d agents, want 2", len(first.Data))
	}
	if first.NextPage == "" {
		t.Fatal("page 1 has no next_page cursor")
	}
	assertRawObjectHasFields(t, first.RawJSON(), "data", "next_page")

	// The SDK's own pager re-sends the identical filter set with the cursor, so
	// following next_page must be accepted by the filter-fingerprint check.
	second, err := first.GetNextPage()
	if err != nil {
		t.Fatalf("follow next_page: %v", err)
	}
	if second == nil || len(second.Data) != 1 {
		t.Fatalf("page 2 = %+v, want 1 agent", second)
	}
	if second.NextPage != "" {
		t.Fatalf("page 2 next_page = %q, want empty", second.NextPage)
	}
	for _, agent := range append(append([]anthropic.BetaManagedAgentsAgent{}, first.Data...), second.Data...) {
		if !created[agent.ID] {
			t.Fatalf("unexpected agent %s in results", agent.ID)
		}
		delete(created, agent.ID)
	}
	if len(created) != 0 {
		t.Fatalf("agents missing from the paged result: %v", created)
	}

	// Documented maximum is 100; over-maximum is an explicit error, not a clamp.
	if _, err := client.Beta.Agents.List(ctx, anthropic.BetaAgentListParams{
		Limit: anthropic.Int(101),
	}); err == nil {
		t.Fatal("limit=101 was accepted")
	} else {
		assertAPIStatus(t, err, 400)
	}
}

// TestSDK_EnvironmentListAndUpdate exercises List Environments and Update
// Environment through the SDK. The SDK types the update metadata as
// map[string]string, so empty-string deletion is the only deletion it can
// express — which is why the endpoint documents it alongside null.
func TestSDK_EnvironmentListAndUpdate(t *testing.T) {
	client, ts := sdkClientAndServer(t)
	ctx := context.Background()

	for range 3 {
		mustEnv(t, ts.URL)
	}

	page, err := client.Beta.Environments.List(ctx, anthropic.BetaEnvironmentListParams{
		Limit:           anthropic.Int(2),
		IncludeArchived: anthropic.Bool(false),
	})
	if err != nil {
		t.Fatalf("list environments: %v", err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("page 1 returned %d environments, want 2", len(page.Data))
	}
	if page.NextPage == "" {
		t.Fatal("page 1 has no next_page cursor")
	}
	assertRawObjectHasFields(t, page.RawJSON(), "data", "next_page")
	assertRawObjectHasFields(t, page.Data[0].RawJSON(),
		"id", "archived_at", "config", "created_at", "description", "metadata",
		"name", "type", "updated_at")

	rest, err := page.GetNextPage()
	if err != nil {
		t.Fatalf("follow next_page: %v", err)
	}
	if rest == nil || len(rest.Data) != 1 {
		t.Fatalf("page 2 = %+v, want 1 environment", rest)
	}

	target := page.Data[0].ID
	updated, err := client.Beta.Environments.Update(ctx, target, anthropic.BetaEnvironmentUpdateParams{
		Description: anthropic.String("data analysis"),
		Metadata:    map[string]string{"team": "ml"},
		Scope:       anthropic.BetaEnvironmentUpdateParamsScopeOrganization,
		Config: anthropic.BetaEnvironmentUpdateParamsConfigUnion{
			OfCloud: &anthropic.BetaCloudConfigParams{
				Packages: anthropic.BetaPackagesParams{Pip: []string{"pandas", "numpy"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("update environment: %v", err)
	}
	if updated.Description != "data analysis" {
		t.Fatalf("description = %q", updated.Description)
	}
	if updated.Metadata["team"] != "ml" {
		t.Fatalf("metadata = %v", updated.Metadata)
	}
	if updated.Scope != anthropic.BetaEnvironmentScopeOrganization {
		t.Fatalf("scope = %q", updated.Scope)
	}
	if pip := updated.Config.Packages.Pip; len(pip) != 2 {
		t.Fatalf("packages.pip = %v", pip)
	}

	// Omitting a field preserves it; an empty-string metadata value deletes it.
	preserved, err := client.Beta.Environments.Update(ctx, target, anthropic.BetaEnvironmentUpdateParams{
		Metadata: map[string]string{"team": ""},
	})
	if err != nil {
		t.Fatalf("metadata delete update: %v", err)
	}
	if _, ok := preserved.Metadata["team"]; ok {
		t.Fatalf("empty-string metadata value did not delete the key: %v", preserved.Metadata)
	}
	if preserved.Description != "data analysis" {
		t.Fatalf("omitted description was not preserved: %q", preserved.Description)
	}
	if pip := preserved.Config.Packages.Pip; len(pip) != 2 {
		t.Fatalf("omitted config was not preserved: %v", preserved.Config)
	}
}
