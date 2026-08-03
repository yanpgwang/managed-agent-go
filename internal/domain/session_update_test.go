package domain

import (
	"errors"
	"reflect"
	"testing"
)

func builtinToolset() map[string]any {
	return map[string]any{"type": BuiltinToolsetType}
}

func mcpToolset(server string) map[string]any {
	return map[string]any{"type": "mcp_toolset", "mcp_server_name": server}
}

func mcpServer(server, url string) map[string]any {
	return map[string]any{"type": "url", "name": server, "url": url}
}

func updatableSession() Session {
	return Session{
		ID:       "sesn_1",
		AgentID:  "agent_1",
		Status:   StatusIdle,
		Title:    "original",
		Metadata: map[string]any{"keep": "yes", "drop": "later"},
		AgentSnapshot: Agent{
			ID:      "agent_1",
			Version: 3,
			Name:    "a",
			Model:   Model{ID: "claude-opus-4-8"},
			Tools:   []any{builtinToolset()},
		},
	}
}

func TestSessionApplyUpdate_MetadataUpsertsDeletesAndPreserves(t *testing.T) {
	session := updatableSession()

	// Omitted metadata preserves the whole bag.
	preserved, change, err := session.ApplyUpdate(SessionUpdate{})
	if err != nil {
		t.Fatalf("preserve update: %v", err)
	}
	if change.Metadata || !reflect.DeepEqual(preserved.Metadata, session.Metadata) {
		t.Fatalf("omitted metadata changed the bag: %#v (change=%+v)", preserved.Metadata, change)
	}

	// A patch upserts named keys and deletes the ones set to null.
	patched, change, err := session.ApplyUpdate(SessionUpdate{Metadata: map[string]any{
		"keep":  "still-yes",
		"drop":  nil,
		"added": "new",
	}})
	if err != nil {
		t.Fatalf("patch update: %v", err)
	}
	if !change.Metadata {
		t.Fatal("metadata patch reported no change")
	}
	want := map[string]any{"keep": "still-yes", "added": "new"}
	if !reflect.DeepEqual(patched.Metadata, want) {
		t.Fatalf("patched metadata = %#v, want %#v", patched.Metadata, want)
	}
	// The patch must not mutate the receiver's bag in place.
	if !reflect.DeepEqual(session.Metadata, map[string]any{"keep": "yes", "drop": "later"}) {
		t.Fatalf("apply mutated the source metadata: %#v", session.Metadata)
	}

	// Deleting an absent key and re-setting an identical value is a no-op.
	unchanged, change, err := session.ApplyUpdate(SessionUpdate{Metadata: map[string]any{
		"absent": nil,
		"keep":   "yes",
	}})
	if err != nil {
		t.Fatalf("no-op patch: %v", err)
	}
	if change.Metadata {
		t.Fatalf("no-op metadata patch reported a change: %#v", unchanged.Metadata)
	}
}

func TestSessionApplyUpdate_MetadataLimitsApplyToTheMergedBag(t *testing.T) {
	session := updatableSession()
	patch := map[string]any{}
	for index := 0; index < 20; index++ {
		patch[string(rune('a'+index))] = "v"
	}
	if _, _, err := session.ApplyUpdate(SessionUpdate{Metadata: patch}); err == nil {
		t.Fatal("expected the 16-key metadata limit to reject the merged bag")
	}
}

func TestSessionApplyUpdate_ToolsReplaceClearAndPreserve(t *testing.T) {
	session := updatableSession()
	session.AgentSnapshot.MCPServers = []any{mcpServer("linear", "https://mcp.example.com/sse")}
	session.AgentSnapshot.Tools = []any{builtinToolset(), mcpToolset("linear")}

	// Omitted arrays preserve both lists.
	title := "renamed"
	preserved, change, err := session.ApplyUpdate(SessionUpdate{Title: &title})
	if err != nil {
		t.Fatalf("title-only update: %v", err)
	}
	if change.Agent {
		t.Fatal("title-only update reported an agent change")
	}
	if !reflect.DeepEqual(preserved.AgentSnapshot, session.AgentSnapshot) {
		t.Fatalf("title-only update changed the snapshot: %#v", preserved.AgentSnapshot)
	}

	// A provided array fully replaces rather than merges.
	replacementTools := []any{builtinToolset()}
	replacementServers := []any{}
	replaced, change, err := session.ApplyUpdate(SessionUpdate{
		AgentTools:      &replacementTools,
		AgentMCPServers: &replacementServers,
	})
	if err != nil {
		t.Fatalf("replacement update: %v", err)
	}
	if !change.Agent {
		t.Fatal("replacement update reported no agent change")
	}
	if len(replaced.AgentSnapshot.Tools) != 1 || len(replaced.AgentSnapshot.MCPServers) != 0 {
		t.Fatalf("replacement snapshot = %#v", replaced.AgentSnapshot)
	}

	// An empty array clears.
	cleared := []any{}
	emptied, change, err := session.ApplyUpdate(SessionUpdate{
		AgentTools:      &cleared,
		AgentMCPServers: &cleared,
	})
	if err != nil {
		t.Fatalf("clearing update: %v", err)
	}
	if !change.Agent || len(emptied.AgentSnapshot.Tools) != 0 {
		t.Fatalf("clearing update = %#v (change=%+v)", emptied.AgentSnapshot, change)
	}
}

func TestSessionApplyUpdate_KeepsAgentIdentityAndDoesNotMutateSource(t *testing.T) {
	session := updatableSession()
	tools := []any{}
	updated, change, err := session.ApplyUpdate(SessionUpdate{AgentTools: &tools})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !change.Agent {
		t.Fatal("expected an agent change")
	}
	if updated.AgentSnapshot.ID != "agent_1" || updated.AgentSnapshot.Version != 3 {
		t.Fatalf("session-local update renumbered the agent: %#v", updated.AgentSnapshot)
	}
	if len(session.AgentSnapshot.Tools) != 1 {
		t.Fatalf("apply mutated the source snapshot: %#v", session.AgentSnapshot.Tools)
	}
}

func TestSessionApplyUpdate_RejectsInconsistentToolConfiguration(t *testing.T) {
	session := updatableSession()
	tools := []any{builtinToolset(), mcpToolset("missing")}
	_, _, err := session.ApplyUpdate(SessionUpdate{AgentTools: &tools})
	if err == nil {
		t.Fatal("expected an mcp_toolset without a matching server to be rejected")
	}
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != KindValidation {
		t.Fatalf("error = %#v, want a validation error", err)
	}
}

func TestSessionApplyUpdate_IdenticalToolsReportNoChange(t *testing.T) {
	session := updatableSession()
	same := []any{builtinToolset()}
	_, change, err := session.ApplyUpdate(SessionUpdate{AgentTools: &same})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if change.Any() {
		t.Fatalf("resubmitting the same tools reported a change: %+v", change)
	}
}

func TestSessionUpdatedPayload_CarriesOnlyChangedFields(t *testing.T) {
	session := updatableSession()

	titleOnly := SessionUpdatedPayload(session, SessionChange{Title: true})
	if len(titleOnly) != 1 || titleOnly["title"] != "original" {
		t.Fatalf("title-only payload = %#v", titleOnly)
	}

	agentOnly := SessionUpdatedPayload(session, SessionChange{Agent: true})
	if len(agentOnly) != 1 {
		t.Fatalf("agent-only payload = %#v", agentOnly)
	}
	agent, ok := agentOnly["agent"].(map[string]any)
	if !ok || agent["id"] != "agent_1" || agent["type"] != "agent" {
		t.Fatalf("agent payload = %#v", agentOnly["agent"])
	}
	if _, present := agentOnly["vault_ids"]; present {
		t.Fatal("session.updated must not carry vault_ids")
	}

	metadataOnly := SessionUpdatedPayload(session, SessionChange{Metadata: true})
	if len(metadataOnly) != 1 {
		t.Fatalf("metadata-only payload = %#v", metadataOnly)
	}

	// Metadata cleared to empty is documented as absent from the event.
	emptied := session
	emptied.Metadata = map[string]any{}
	clearedPayload := SessionUpdatedPayload(emptied, SessionChange{Metadata: true})
	if len(clearedPayload) != 0 {
		t.Fatalf("cleared metadata payload = %#v, want no fields", clearedPayload)
	}
}
