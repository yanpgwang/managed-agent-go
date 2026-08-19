package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
	temporalpkg "github.com/yanpgwang/mango/internal/temporal"
)

// TestPostgresSessionUpdateAppliesToTheNextTurn covers the durable half of the
// documented mid-session agent update: the replacement lands in the session's
// resolved snapshot, the next turn's PrepareTurn reads it, and the idle
// precondition rejects the same request once a turn is in flight.
//
// Sources: guides/session-operations.md ("Updating the agent configuration"),
// api-reference/sessions/update.md.
func TestPostgresSessionUpdateAppliesToTheNextTurn(t *testing.T) {
	handler, fixture := postgresHandlerWithFixture(t)
	ctx := context.Background()

	agentID := createResource(t, handler, "/v1/agents",
		`{"name":"coder","model":"claude-test",`+
			`"tools":[{"type":"agent_toolset_20260401"}]}`)
	environmentID := createResource(t, handler, "/v1/environments",
		`{"name":"cloud","config":{"type":"cloud"}}`)
	sessionID := createResource(t, handler, "/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`"}`)

	// Narrow the session's tool surface to read only. Full replacement, applied
	// while the session is idle.
	response := request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID,
		`{"agent":{"tools":[{"type":"agent_toolset_20260401",`+
			`"default_config":{"enabled":false},`+
			`"configs":[{"name":"read","enabled":true}]}]}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update session agent -> %d: %s", response.Code, response.Body.String())
	}

	// The underlying agent resource keeps its own configuration and version.
	response = request(t, handler, http.MethodGet, "/v1/agents/"+agentID, "")
	var agentResource map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &agentResource); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	if version, _ := agentResource["version"].(float64); version != 1 {
		t.Fatalf("session update renumbered the agent: %v", agentResource["version"])
	}
	agentTools, _ := agentResource["tools"].([]any)
	if len(agentTools) != 1 {
		t.Fatalf("agent tools = %#v", agentResource["tools"])
	}
	if configured, _ := agentTools[0].(map[string]any); configured["default_config"] != nil {
		t.Fatalf("session update propagated to the agent resource: %#v", agentTools[0])
	}

	// The session.updated event carries the resolved snapshot and nothing else.
	events, err := NewEventService(fixture.store).Query(ctx, sessionID, app.EventQuery{
		Types: []string{domain.EvSessionUpdated}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query session.updated: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("session.updated count = %d, want 1", len(events))
	}
	if _, present := events[0].Payload["title"]; present {
		t.Fatalf("session.updated carries an unchanged title: %#v", events[0].Payload)
	}
	if _, present := events[0].Payload["agent"]; !present {
		t.Fatalf("session.updated is missing the agent: %#v", events[0].Payload)
	}

	// The next turn must see the narrowed tool surface.
	response = request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID+"/events",
		`{"events":[{"type":"user.message","content":[{"type":"text","text":"hi"}]}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("send message -> %d: %s", response.Code, response.Body.String())
	}
	var sent struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &sent); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	triggerID := ""
	for _, event := range sent.Data {
		if event.Type == domain.EvUserMessage {
			triggerID = event.ID
		}
	}
	if triggerID == "" {
		t.Fatalf("no user.message id in %s", response.Body.String())
	}

	prepared, err := temporalpkg.NewActivities(
		nil, temporalpkg.NewStoreSource(fixture.store), nil, nil, fixture.ids,
	).PrepareTurn(ctx, temporalpkg.PrepareTurnInput{
		SessionID: sessionID, TriggerEventID: triggerID,
	})
	if err != nil {
		t.Fatalf("prepare turn: %v", err)
	}
	if prepared.FatalError != "" {
		t.Fatalf("prepare turn fatal: %s", prepared.FatalError)
	}
	if len(prepared.Request.Tools) != 1 || prepared.Request.Tools[0].Name != "read" {
		names := make([]string, 0, len(prepared.Request.Tools))
		for _, tool := range prepared.Request.Tools {
			names = append(names, tool.Name)
		}
		t.Fatalf("next turn tools = %v, want [read]", names)
	}

	// The message admission left the session running, so the same agent update
	// is now rejected as a conflict.
	response = request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID,
		`{"agent":{"tools":[]}}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("agent update while running -> %d, want 409: %s",
			response.Code, response.Body.String())
	}
	// Title and metadata remain updatable mid-turn.
	response = request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID,
		`{"title":"mid-turn","metadata":{"phase":"running"}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("title update while running -> %d: %s",
			response.Code, response.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	metadata, _ := updated["metadata"].(map[string]any)
	if updated["title"] != "mid-turn" || metadata["phase"] != "running" {
		t.Fatalf("mid-turn update = %#v", updated)
	}
}

func TestPostgresSessionSnapshotsEnvironmentPackages(t *testing.T) {
	handler, fixture := postgresHandlerWithFixture(t)
	ctx := context.Background()

	agentID := createResource(t, handler, "/v1/agents",
		`{"name":"coder","model":"claude-test"}`)
	environmentID := createResource(t, handler, "/v1/environments",
		`{"name":"cloud","config":{"type":"cloud","packages":{"pip":["httpx==0.28.1"]}}}`)
	firstSessionID := createResource(t, handler, "/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`"}`)

	response := request(t, handler, http.MethodPost, "/v1/environments/"+environmentID,
		`{"config":{"type":"cloud","packages":{"pip":["httpx==0.29.0"]}}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update environment -> %d: %s", response.Code, response.Body.String())
	}
	secondSessionID := createResource(t, handler, "/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`"}`)

	first, err := fixture.store.GetSession(ctx, firstSessionID)
	if err != nil {
		t.Fatalf("load first session: %v", err)
	}
	second, err := fixture.store.GetSession(ctx, secondSessionID)
	if err != nil {
		t.Fatalf("load second session: %v", err)
	}
	firstPip := first.EnvironmentConfig["packages"].(map[string]any)["pip"].([]any)
	secondPip := second.EnvironmentConfig["packages"].(map[string]any)["pip"].([]any)
	if firstPip[0] != "httpx==0.28.1" || secondPip[0] != "httpx==0.29.0" {
		t.Fatalf("session package snapshots = first %#v, second %#v", firstPip, secondPip)
	}
}
