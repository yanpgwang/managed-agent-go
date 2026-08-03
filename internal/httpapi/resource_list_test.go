package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The two list endpoints are asymmetric on purpose. List Agents documents five
// query parameters (created_at[gte], created_at[lte], include_archived, limit,
// page) with limit "Default 20, maximum 100". List Environments documents three
// (include_archived, limit, page), no created_at filters, and no limit default
// or maximum at all. These tests pin both surfaces separately so a future
// refactor cannot quietly merge them.

func listBody(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	rec := do(h, "GET", path, "")
	if rec.Code != 200 {
		t.Fatalf("GET %s status %d: %s", path, rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, rec.Body)
	}
	return body
}

func listIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	data, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("response data is not an array: %v", body)
	}
	ids := make([]string, 0, len(data))
	for _, item := range data {
		object, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("data item is not an object: %v", item)
		}
		id, _ := object["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func nextPage(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, present := body["next_page"]
	if !present {
		t.Fatalf("response envelope has no next_page key: %v", body)
	}
	if raw == nil {
		return ""
	}
	token, ok := raw.(string)
	if !ok {
		t.Fatalf("next_page is not a string: %v", raw)
	}
	return token
}

func mustCreateAgents(t *testing.T, h http.Handler, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for index := range count {
		rec := do(h, "POST", "/v1/agents",
			fmt.Sprintf(`{"name":"agent %d","model":"claude-opus-4-8"}`, index))
		if rec.Code != 200 {
			t.Fatalf("create agent %d: %d %s", index, rec.Code, rec.Body)
		}
		var created map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode agent %d: %v", index, err)
		}
		ids = append(ids, created["id"].(string))
	}
	return ids
}

func mustCreateEnvironments(t *testing.T, h http.Handler, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for index := range count {
		rec := do(h, "POST", "/v1/environments",
			fmt.Sprintf(`{"name":"env %d","config":{"type":"cloud"}}`, index))
		if rec.Code != 200 {
			t.Fatalf("create environment %d: %d %s", index, rec.Code, rec.Body)
		}
		var created map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode environment %d: %v", index, err)
		}
		ids = append(ids, created["id"].(string))
	}
	return ids
}

func TestListAgents_EnvelopeIsDataAndNextPageOnly(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 2)

	body := listBody(t, srv, "/v1/agents")
	if len(body) != 2 {
		t.Fatalf("envelope keys = %v, want exactly data and next_page", body)
	}
	if _, ok := body["data"]; !ok {
		t.Fatalf("envelope missing data: %v", body)
	}
	if raw, present := body["next_page"]; !present || raw != nil {
		t.Fatalf("next_page = %v (present=%v), want an explicit null", raw, present)
	}
	for _, unexpected := range []string{"prev_page", "has_more", "order"} {
		if _, ok := body[unexpected]; ok {
			t.Errorf("envelope contains undocumented key %q: %v", unexpected, body)
		}
	}
}

func TestListAgents_LimitIsHonored(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 5)

	body := listBody(t, srv, "/v1/agents?limit=2")
	if got := listIDs(t, body); len(got) != 2 {
		t.Fatalf("limit=2 returned %d agents: %v", len(got), got)
	}
	if nextPage(t, body) == "" {
		t.Fatal("limit=2 over 5 agents produced no next_page")
	}
}

func TestListAgents_LimitDefaultIsTwenty(t *testing.T) {
	// Documented as "Default 20, maximum 100". Assert the default through the
	// service boundary rather than the constant: 21 agents must page at 20.
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 21)

	body := listBody(t, srv, "/v1/agents")
	if got := listIDs(t, body); len(got) != 20 {
		t.Fatalf("default page returned %d agents, want 20", len(got))
	}
	if nextPage(t, body) == "" {
		t.Fatal("21 agents at the default limit produced no next_page")
	}
}

func TestListAgents_LimitBounds(t *testing.T) {
	srv := newTestServer(t)
	for _, tc := range []struct{ name, query string }{
		{"over maximum", "limit=101"},
		{"zero", "limit=0"},
		{"negative", "limit=-1"},
		{"not a number", "limit=many"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(srv, "GET", "/v1/agents?"+tc.query, "")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
	// The documented maximum itself must be accepted.
	if rec := do(srv, "GET", "/v1/agents?limit=100", ""); rec.Code != 200 {
		t.Fatalf("limit=100 status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestListAgents_CursorRoundTripCoversEveryAgentExactlyOnce(t *testing.T) {
	srv := newTestServer(t)
	created := mustCreateAgents(t, srv, 5)

	seen := map[string]int{}
	path := "/v1/agents?limit=2"
	pages := 0
	for {
		body := listBody(t, srv, path)
		for _, id := range listIDs(t, body) {
			seen[id]++
		}
		pages++
		token := nextPage(t, body)
		if token == "" {
			break
		}
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		path = "/v1/agents?limit=2&page=" + url.QueryEscape(token)
	}
	if pages != 3 {
		t.Fatalf("paged in %d requests, want 3", pages)
	}
	if len(seen) != len(created) {
		t.Fatalf("saw %d distinct agents, want %d", len(seen), len(created))
	}
	for _, id := range created {
		if seen[id] != 1 {
			t.Errorf("agent %s appeared %d times across pages", id, seen[id])
		}
	}
}

func TestListAgents_CursorIsOpaqueAndPrefixed(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 3)
	token := nextPage(t, listBody(t, srv, "/v1/agents?limit=1"))
	if token == "" {
		t.Fatal("no next_page cursor")
	}
	if !strings.HasPrefix(token, resourceCursorPrefix) {
		t.Fatalf("cursor %q is not prefixed with %q", token, resourceCursorPrefix)
	}
	// The cursor must not leak the row key it encodes in readable form.
	for _, leaked := range []string{"agent_", "created_at", "{"} {
		if strings.Contains(strings.TrimPrefix(token, resourceCursorPrefix), leaked) {
			t.Errorf("cursor %q leaks %q", token, leaked)
		}
	}
}

func TestListAgents_CursorRejectedWhenFiltersChange(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 4)
	token := nextPage(t, listBody(t, srv, "/v1/agents?limit=1"))
	if token == "" {
		t.Fatal("no next_page cursor")
	}

	// Same filters: accepted.
	if rec := do(srv, "GET", "/v1/agents?limit=1&page="+url.QueryEscape(token), ""); rec.Code != 200 {
		t.Fatalf("replaying the cursor with unchanged filters = %d: %s", rec.Code, rec.Body)
	}
	// Changed filters: rejected rather than silently paging a different set.
	for _, changed := range []string{
		"include_archived=true",
		"created_at[gte]=2020-01-01T00:00:00Z",
		"created_at[lte]=2030-01-01T00:00:00Z",
	} {
		rec := do(srv, "GET", "/v1/agents?limit=1&"+changed+"&page="+url.QueryEscape(token), "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cursor with %s = %d, want 400: %s", changed, rec.Code, rec.Body)
		}
	}
}

func TestListAgents_MalformedAndForeignCursorsRejected(t *testing.T) {
	srv := newTestServer(t)
	mustCreateEnvironments(t, srv, 3)
	mustCreateAgents(t, srv, 3)

	environmentToken := nextPage(t, listBody(t, srv, "/v1/environments?limit=1"))
	if environmentToken == "" {
		t.Fatal("no environment cursor to cross-feed")
	}
	for _, token := range []string{
		"not-a-cursor",
		"page_!!!!",
		environmentToken, // an environments cursor must not work on agents
	} {
		rec := do(srv, "GET", "/v1/agents?page="+url.QueryEscape(token), "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("page=%q = %d, want 400: %s", token, rec.Code, rec.Body)
		}
	}
}

func TestListAgents_IncludeArchived(t *testing.T) {
	srv := newTestServer(t)
	ids := mustCreateAgents(t, srv, 2)
	if rec := do(srv, "POST", "/v1/agents/"+ids[0]+"/archive", ""); rec.Code != 200 {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body)
	}

	if got := listIDs(t, listBody(t, srv, "/v1/agents")); len(got) != 1 || got[0] != ids[1] {
		t.Fatalf("default list = %v, want only the active agent %s", got, ids[1])
	}
	if got := listIDs(t, listBody(t, srv, "/v1/agents?include_archived=true")); len(got) != 2 {
		t.Fatalf("include_archived=true list = %v, want 2 agents", got)
	}
	if got := listIDs(t, listBody(t, srv, "/v1/agents?include_archived=false")); len(got) != 1 {
		t.Fatalf("include_archived=false list = %v, want 1 agent", got)
	}
	if rec := do(srv, "GET", "/v1/agents?include_archived=yes", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("include_archived=yes = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestListAgents_CreatedAtFilters(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 2)
	// The test clock is fixed at 1970-01-01T00:16:40Z.
	if got := listIDs(t, listBody(t, srv, "/v1/agents?created_at[gte]=1970-01-01T00:16:40Z")); len(got) != 2 {
		t.Fatalf("gte at the creation instant returned %v, want both agents", got)
	}
	if got := listIDs(t, listBody(t, srv, "/v1/agents?created_at[lte]=1970-01-01T00:16:40Z")); len(got) != 2 {
		t.Fatalf("lte at the creation instant returned %v, want both agents", got)
	}
	if got := listIDs(t, listBody(t, srv, "/v1/agents?created_at[gte]=2020-01-01T00:00:00Z")); len(got) != 0 {
		t.Fatalf("gte in the future returned %v, want none", got)
	}
	if rec := do(srv, "GET", "/v1/agents?created_at[gte]=yesterday", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unparsable created_at[gte] = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestListAgents_RejectsParametersItDoesNotDocument(t *testing.T) {
	// created_at[gt]/[lt] exist on List Sessions but not on List Agents, and
	// there is no `order`. Accepting them silently would advertise filtering
	// the endpoint does not perform.
	srv := newTestServer(t)
	for _, query := range []string{
		"created_at[gt]=2020-01-01T00:00:00Z",
		"created_at[lt]=2020-01-01T00:00:00Z",
		"order=asc",
	} {
		rec := do(srv, "GET", "/v1/agents?"+query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", query, rec.Code, rec.Body)
		}
	}
	// An unrecognized parameter is still tolerated: the official SDK appends
	// `beta=true` to these paths.
	if rec := do(srv, "GET", "/v1/agents?beta=true", ""); rec.Code != 200 {
		t.Fatalf("beta=true = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestListEnvironments_EnvelopeIncludesNextPage(t *testing.T) {
	srv := newTestServer(t)
	mustCreateEnvironments(t, srv, 2)

	body := listBody(t, srv, "/v1/environments")
	if raw, present := body["next_page"]; !present || raw != nil {
		t.Fatalf("next_page = %v (present=%v), want an explicit null", raw, present)
	}
	if _, ok := body["prev_page"]; ok {
		t.Fatalf("envelope contains prev_page: %v", body)
	}
}

func TestListEnvironments_LimitAndPagination(t *testing.T) {
	srv := newTestServer(t)
	created := mustCreateEnvironments(t, srv, 5)

	seen := map[string]int{}
	path := "/v1/environments?limit=2"
	pages := 0
	for {
		body := listBody(t, srv, path)
		if got := listIDs(t, body); len(got) > 2 {
			t.Fatalf("page returned %d environments, want at most 2", len(got))
		}
		for _, id := range listIDs(t, body) {
			seen[id]++
		}
		pages++
		token := nextPage(t, body)
		if token == "" {
			break
		}
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
		path = "/v1/environments?limit=2&page=" + url.QueryEscape(token)
	}
	if pages != 3 {
		t.Fatalf("paged in %d requests, want 3", pages)
	}
	for _, id := range created {
		if seen[id] != 1 {
			t.Errorf("environment %s appeared %d times across pages", id, seen[id])
		}
	}
}

func TestListEnvironments_LimitBounds(t *testing.T) {
	// Upstream documents no default and no maximum for this limit, so the bound
	// asserted here is Mango's own (max 1000) rather than the List Agents 100.
	srv := newTestServer(t)
	if rec := do(srv, "GET", "/v1/environments?limit=100", ""); rec.Code != 200 {
		t.Fatalf("limit=100 = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := do(srv, "GET", "/v1/environments?limit=1000", ""); rec.Code != 200 {
		t.Fatalf("limit=1000 = %d, want 200 (the agents maximum of 100 must not apply): %s",
			rec.Code, rec.Body)
	}
	for _, query := range []string{"limit=1001", "limit=0", "limit=-3", "limit=all"} {
		rec := do(srv, "GET", "/v1/environments?"+query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", query, rec.Code, rec.Body)
		}
	}
}

func TestListEnvironments_CursorRejectedWhenFiltersChange(t *testing.T) {
	srv := newTestServer(t)
	mustCreateEnvironments(t, srv, 4)
	token := nextPage(t, listBody(t, srv, "/v1/environments?limit=1"))
	if token == "" {
		t.Fatal("no next_page cursor")
	}
	if rec := do(srv, "GET", "/v1/environments?limit=1&page="+url.QueryEscape(token), ""); rec.Code != 200 {
		t.Fatalf("replaying the cursor with unchanged filters = %d: %s", rec.Code, rec.Body)
	}
	rec := do(srv, "GET",
		"/v1/environments?limit=1&include_archived=true&page="+url.QueryEscape(token), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cursor after include_archived change = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestListEnvironments_ForeignCursorRejected(t *testing.T) {
	srv := newTestServer(t)
	mustCreateAgents(t, srv, 3)
	mustCreateEnvironments(t, srv, 3)
	agentToken := nextPage(t, listBody(t, srv, "/v1/agents?limit=1"))
	if agentToken == "" {
		t.Fatal("no agent cursor to cross-feed")
	}
	rec := do(srv, "GET", "/v1/environments?page="+url.QueryEscape(agentToken), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("agents cursor on environments = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestListEnvironments_IncludeArchived(t *testing.T) {
	srv := newTestServer(t)
	ids := mustCreateEnvironments(t, srv, 2)
	if rec := do(srv, "POST", "/v1/environments/"+ids[0]+"/archive", ""); rec.Code != 200 {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body)
	}
	if got := listIDs(t, listBody(t, srv, "/v1/environments")); len(got) != 1 || got[0] != ids[1] {
		t.Fatalf("default list = %v, want only the active environment %s", got, ids[1])
	}
	if got := listIDs(t, listBody(t, srv, "/v1/environments?include_archived=true")); len(got) != 2 {
		t.Fatalf("include_archived=true list = %v, want 2", got)
	}
	if rec := do(srv, "GET", "/v1/environments?include_archived=maybe", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("include_archived=maybe = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestListEnvironments_RejectsCreatedAtFilters(t *testing.T) {
	// List Agents documents created_at[gte]/[lte]; List Environments documents
	// no created_at filtering at all. The asymmetry is enforced, not assumed.
	srv := newTestServer(t)
	for _, query := range []string{
		"created_at[gte]=2020-01-01T00:00:00Z",
		"created_at[lte]=2020-01-01T00:00:00Z",
		"created_at[gt]=2020-01-01T00:00:00Z",
		"created_at[lt]=2020-01-01T00:00:00Z",
		"order=desc",
	} {
		rec := do(srv, "GET", "/v1/environments?"+query, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", query, rec.Code, rec.Body)
		}
	}
	if rec := do(srv, "GET", "/v1/environments?beta=true", ""); rec.Code != 200 {
		t.Fatalf("beta=true = %d, want 200: %s", rec.Code, rec.Body)
	}
}
