package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"testing"
)

func createAgentVersions(t *testing.T, handler http.Handler, count int) string {
	t.Helper()
	recorder := do(handler, http.MethodPost, "/v1/agents",
		`{"name":"agent v1","model":"claude-opus-4-8"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create agent = %d: %s", recorder.Code, recorder.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created agent: %v", err)
	}
	id := created["id"].(string)
	for version := 2; version <= count; version++ {
		recorder = do(handler, http.MethodPost, "/v1/agents/"+id,
			fmt.Sprintf(`{"name":"agent v%d"}`, version))
		if recorder.Code != http.StatusOK {
			t.Fatalf("create version %d = %d: %s", version, recorder.Code, recorder.Body)
		}
	}
	return id
}

func listedAgentVersions(t *testing.T, body map[string]any) []int {
	t.Helper()
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("data is not an array: %#v", body)
	}
	versions := make([]int, 0, len(data))
	for _, item := range data {
		object, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("version is not an object: %#v", item)
		}
		versions = append(versions, int(object["version"].(float64)))
	}
	return versions
}

func TestListAgentVersions_DefaultLimitAndPaging(t *testing.T) {
	server := newTestServer(t)
	agentID := createAgentVersions(t, server, 21)

	first := decodeResourceList(t, server, "/v1/agents/"+agentID+"/versions")
	if len(first) != 2 {
		t.Fatalf("envelope = %#v, want data and next_page only", first)
	}
	versions := listedAgentVersions(t, first)
	if len(versions) != 20 || versions[0] != 1 || versions[19] != 20 {
		t.Fatalf("first page versions = %v, want 1 through 20", versions)
	}
	token := resourceNextPage(t, first)
	if token == "" {
		t.Fatal("first page has no next_page")
	}

	second := decodeResourceList(t, server, "/v1/agents/"+agentID+
		"/versions?page="+url.QueryEscape(token))
	versions = listedAgentVersions(t, second)
	if len(versions) != 1 || versions[0] != 21 {
		t.Fatalf("second page versions = %v, want [21]", versions)
	}
	if resourceNextPage(t, second) != "" {
		t.Fatal("terminal page has a next_page")
	}
}

func TestListAgentVersions_ValidatesLimitAndCursorScope(t *testing.T) {
	server := newTestServer(t)
	firstAgentID := createAgentVersions(t, server, 3)
	secondAgentID := createAgentVersions(t, server, 2)

	first := decodeResourceList(t, server, "/v1/agents/"+firstAgentID+"/versions?limit=1")
	token := resourceNextPage(t, first)
	if token == "" {
		t.Fatal("limit=1 has no cursor")
	}
	if recorder := do(server, http.MethodGet, "/v1/agents/"+firstAgentID+
		"/versions?limit=2&page="+url.QueryEscape(token), ""); recorder.Code != http.StatusOK {
		t.Fatalf("same-agent cursor = %d: %s", recorder.Code, recorder.Body)
	}
	if recorder := do(server, http.MethodGet, "/v1/agents/"+secondAgentID+
		"/versions?page="+url.QueryEscape(token), ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("cross-agent cursor = %d, want 400", recorder.Code)
	}

	topLevelCursor := resourceNextPage(t,
		decodeResourceList(t, server, "/v1/agents?limit=1"))
	if recorder := do(server, http.MethodGet, "/v1/agents/"+firstAgentID+
		"/versions?page="+url.QueryEscape(topLevelCursor), ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("top-level Agent cursor = %d, want 400", recorder.Code)
	}
	overflowCursor := encodeAgentVersionCursor(firstAgentID, math.MaxInt32+1)
	if recorder := do(server, http.MethodGet, "/v1/agents/"+firstAgentID+
		"/versions?page="+url.QueryEscape(overflowCursor), ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("overflowing version cursor = %d, want 400", recorder.Code)
	}

	for _, query := range []string{
		"limit=0", "limit=-1", "limit=101", "limit=many", "page=not-a-cursor",
	} {
		recorder := do(server, http.MethodGet,
			"/v1/agents/"+firstAgentID+"/versions?"+query, "")
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", query, recorder.Code, recorder.Body)
		}
	}
}
