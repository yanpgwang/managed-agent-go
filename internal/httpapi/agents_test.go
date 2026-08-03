package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgents_CreateGetVersionArchive(t *testing.T) {
	srv := newTestServer(t)
	// create
	body := `{"name":"SRE Agent","model":"claude-opus-4-8","system":"help"}`
	rec := do(srv, "POST", "/v1/agents", body)
	if rec.Code != 200 {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created["version"].(float64) != 1 || created["type"] != "agent" {
		t.Fatalf("bad create body: %v", created)
	}
	id := created["id"].(string)
	// update -> version 2
	rec = do(srv, "POST", "/v1/agents/"+id, `{"name":"SRE v2"}`)
	var up map[string]any
	json.Unmarshal(rec.Body.Bytes(), &up)
	if up["version"].(float64) != 2 {
		t.Fatalf("expected v2, got %v", up["version"])
	}
	// versions list has 2
	rec = do(srv, "GET", "/v1/agents/"+id+"/versions", "")
	var vs map[string]any
	json.Unmarshal(rec.Body.Bytes(), &vs)
	if len(vs["data"].([]any)) != 2 {
		t.Fatalf("expected 2 versions, got %v", vs["data"])
	}
	// archive
	rec = do(srv, "POST", "/v1/agents/"+id+"/archive", "")
	if rec.Code != 200 {
		t.Fatalf("archive status %d", rec.Code)
	}
	var archived map[string]any
	json.Unmarshal(rec.Body.Bytes(), &archived)
	if archived["version"].(float64) != 2 {
		t.Fatalf("archive created a configuration version: %v", archived["version"])
	}
	rec = do(srv, "GET", "/v1/agents/"+id+"/versions", "")
	json.Unmarshal(rec.Body.Bytes(), &vs)
	if len(vs["data"].([]any)) != 2 {
		t.Fatalf("archive appended version history: %v", vs["data"])
	}
	rec = do(srv, "POST", "/v1/agents/"+id, `{"name":"must fail"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("archived agent update status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestAgents_ClearSystemWithNull verifies that sending {"system":null} clears the
// system field, that the version bumps, and that a subsequent update without the
// system key does NOT resurrect the field.
func TestAgents_ClearSystemWithNull(t *testing.T) {
	srv := newTestServer(t)

	// create agent with system="help"
	rec := do(srv, "POST", "/v1/agents", `{"name":"Agent","model":"claude-opus-4-8","system":"help"}`)
	if rec.Code != 200 {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// update with explicit null -> should clear system
	rec = do(srv, "POST", "/v1/agents/"+id, `{"system":null}`)
	if rec.Code != 200 {
		t.Fatalf("update (null system) status %d: %s", rec.Code, rec.Body)
	}
	var up1 map[string]any
	json.Unmarshal(rec.Body.Bytes(), &up1)
	if up1["version"].(float64) != 2 {
		t.Fatalf("expected version 2 after clearing system, got %v", up1["version"])
	}
	if up1["system"] != nil {
		t.Fatalf("expected system=null after clearing, got %v", up1["system"])
	}

	// GET to confirm persisted state
	rec = do(srv, "GET", "/v1/agents/"+id, "")
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["system"] != nil {
		t.Fatalf("GET: expected system=null, got %v", got["system"])
	}

	// update without system key -> system must stay null, no resurrection
	rec = do(srv, "POST", "/v1/agents/"+id, `{"name":"renamed"}`)
	if rec.Code != 200 {
		t.Fatalf("name-only update status %d: %s", rec.Code, rec.Body)
	}
	var up2 map[string]any
	json.Unmarshal(rec.Body.Bytes(), &up2)
	if up2["system"] != nil {
		t.Fatalf("absent system key resurrected system field; got %v", up2["system"])
	}
}

// TestAgents_UpdateModelNullRejected verifies that model, unlike nullable
// system/description, cannot be cleared.
func TestAgents_UpdateModelNullRejected(t *testing.T) {
	srv := newTestServer(t)

	// create agent with a named model
	rec := do(srv, "POST", "/v1/agents", `{"name":"Agent","model":"claude-opus-4-8"}`)
	if rec.Code != 200 {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	// update with model:null must be rejected
	rec = do(srv, "POST", "/v1/agents/"+id, `{"model":null,"name":"y"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update status %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestAgents_UpdateArrayNullClears(t *testing.T) {
	srv := newTestServer(t)
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8",`+
			`"tools":[{"type":"custom","name":"x"},{"type":"mcp_toolset","mcp_server_name":"m"}],`+
			`"mcp_servers":[{"type":"url","name":"m","url":"https://example.com"}],`+
			`"skills":[{"type":"anthropic","skill_id":"xlsx","version":"1"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec = do(srv, "POST", "/v1/agents/"+id,
		`{"tools":null,"mcp_servers":null,"skills":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d: %s", rec.Code, rec.Body)
	}
	var updated map[string]any
	json.Unmarshal(rec.Body.Bytes(), &updated)
	for _, field := range []string{"tools", "mcp_servers", "skills"} {
		values, ok := updated[field].([]any)
		if !ok || len(values) != 0 {
			t.Errorf("%s was not cleared: %#v", field, updated[field])
		}
	}

	rec = do(srv, "POST", "/v1/agents/"+id, `{"tools":"not-an-array"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid tools status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestAgents_MultiagentObjectPersistsAndReplaces(t *testing.T) {
	srv := newTestServer(t)
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Coordinator","model":"claude-opus-4-8","multiagent":{"type":"coordinator","agents":["agent_one"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)
	multiagent, ok := created["multiagent"].(map[string]any)
	if !ok || multiagent["type"] != "coordinator" {
		t.Fatalf("create response lost multiagent: %#v", created["multiagent"])
	}

	rec = do(srv, "POST", "/v1/agents/"+id,
		`{"multiagent":{"type":"coordinator","agents":["agent_two"]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", rec.Code, rec.Body)
	}
	var updated map[string]any
	json.Unmarshal(rec.Body.Bytes(), &updated)
	updatedMultiagent := updated["multiagent"].(map[string]any)
	agents := updatedMultiagent["agents"].([]any)
	if len(agents) != 1 || agents[0] != "agent_two" {
		t.Fatalf("multiagent was not replaced: %#v", updatedMultiagent)
	}

	rec = do(srv, "POST", "/v1/agents/"+id, `{"name":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("name-only update status %d: %s", rec.Code, rec.Body)
	}
	rec = do(srv, "GET", "/v1/agents/"+id, "")
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	gotAgents := got["multiagent"].(map[string]any)["agents"].([]any)
	if len(gotAgents) != 1 || gotAgents[0] != "agent_two" {
		t.Fatalf("omitted multiagent did not preserve stored value: %#v", got["multiagent"])
	}

	rec = do(srv, "POST", "/v1/agents/"+id, `{"multiagent":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status %d: %s", rec.Code, rec.Body)
	}
	var cleared map[string]any
	json.Unmarshal(rec.Body.Bytes(), &cleared)
	if cleared["multiagent"] != nil {
		t.Fatalf("explicit null did not clear multiagent: %#v", cleared["multiagent"])
	}
	rec = do(srv, "GET", "/v1/agents/"+id, "")
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["multiagent"] != nil {
		t.Fatalf("cleared multiagent was not persisted: %#v", got["multiagent"])
	}
}

func TestAgents_MultiagentNullAndInvalidShapes(t *testing.T) {
	for _, invalid := range []string{`[]`, `"coordinator"`, "1", "true"} {
		t.Run(invalid, func(t *testing.T) {
			srv := newTestServer(t)
			body := `{"name":"Agent","model":"claude-opus-4-8","multiagent":` + invalid + `}`
			rec := do(srv, "POST", "/v1/agents", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}

	srv := newTestServer(t)
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8","multiagent":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create null status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created["multiagent"] != nil {
		t.Fatalf("create null should leave multiagent unset: %#v", created["multiagent"])
	}
}

func TestAgents_ModelEffortAcceptsOfficialInputShapes(t *testing.T) {
	srv := newTestServer(t)
	asString := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":{"id":"claude-opus-4-8","effort":"high"}}`)
	if asString.Code != http.StatusOK {
		t.Fatalf("string effort status = %d, want 200: %s", asString.Code, asString.Body)
	}
	var stringResult map[string]any
	if err := json.Unmarshal(asString.Body.Bytes(), &stringResult); err != nil {
		t.Fatal(err)
	}
	effort := stringResult["model"].(map[string]any)["effort"].(map[string]any)
	if effort["type"] != "high" {
		t.Fatalf("canonical effort response = %#v", effort)
	}

	asObject := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":{"id":"claude-opus-4-8","effort":{"type":"high"},"speed":"standard"}}`)
	if asObject.Code != http.StatusOK {
		t.Fatalf("tagged effort status = %d, want 200: %s", asObject.Code, asObject.Body)
	}

	invalid := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":{"id":"claude-opus-4-8","effort":{"type":"high","extra":true}}}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid effort object status = %d, want 400: %s", invalid.Code, invalid.Body)
	}
}

func TestAgents_MetadataValidationUsesResultingBag(t *testing.T) {
	srv := newTestServer(t)
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8","metadata":{"bad":1}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-string metadata status = %d, want 400: %s", rec.Code, rec.Body)
	}

	metadata := make(map[string]string, 16)
	for i := 0; i < 16; i++ {
		metadata[string(rune('a'+i))] = "v"
	}
	body, err := json.Marshal(map[string]any{
		"name": "Agent", "model": "claude-opus-4-8", "metadata": metadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = do(srv, "POST", "/v1/agents", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("create at metadata limit status %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)

	rec = do(srv, "POST", "/v1/agents/"+id, `{"metadata":{"overflow":"v"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("overflow patch status = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(srv, "POST", "/v1/agents/"+id, `{"metadata":{"a":null,"replacement":"v"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete-and-add patch status %d: %s", rec.Code, rec.Body)
	}
	var updated map[string]any
	json.Unmarshal(rec.Body.Bytes(), &updated)
	gotMetadata := updated["metadata"].(map[string]any)
	if len(gotMetadata) != 16 || gotMetadata["replacement"] != "v" {
		t.Fatalf("metadata patch result = %#v", gotMetadata)
	}
}

// A client that sends an upstream-shaped but undocumented nested field must
// get a 400 rather than have it stored in the immutable agent version and
// echoed back. The MCP connector guide is explicit that no authentication
// material is supplied at configuration time, so `authorization_token` is the
// canonical credential-leak case.
func TestAgents_RejectsUndocumentedNestedConfigOnCreate(t *testing.T) {
	srv := newTestServer(t)
	const secret = "sk-leaked-authorization-token"
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8",`+
			`"tools":[{"type":"mcp_toolset","mcp_server_name":"x"}],`+
			`"mcp_servers":[{"name":"x","type":"url","url":"https://e.com",`+
			`"authorization_token":"`+secret+`"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var failure map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errorBody, _ := failure["error"].(map[string]any)
	if errorBody["type"] != "invalid_request_error" {
		t.Fatalf("error type = %#v, want invalid_request_error", failure)
	}
	assertNoSecret(t, "create response", rec.Body.String(), secret)

	// The rejected agent must not exist at all, and no listing may leak it.
	for _, path := range []string{"/v1/agents"} {
		listed := do(srv, "GET", path, "")
		assertNoSecret(t, path, listed.Body.String(), secret)
	}
}

func TestAgents_RejectsUndocumentedNestedConfigOnUpdate(t *testing.T) {
	srv := newTestServer(t)
	const secret = "sk-leaked-authorization-token"
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8",`+
			`"tools":[{"type":"mcp_toolset","mcp_server_name":"x"}],`+
			`"mcp_servers":[{"name":"x","type":"url","url":"https://e.com"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	id := created["id"].(string)

	for name, patch := range map[string]string{
		"mcp_servers": `{"mcp_servers":[{"name":"x","type":"url",` +
			`"url":"https://e.com","authorization_token":"` + secret + `"}]}`,
		"tools": `{"tools":[{"type":"mcp_toolset","mcp_server_name":"x",` +
			`"authorization_token":"` + secret + `"}]}`,
		"skills": `{"skills":[{"type":"anthropic","skill_id":"xlsx",` +
			`"authorization_token":"` + secret + `"}]}`,
		"multiagent": `{"multiagent":{"type":"coordinator",` +
			`"agents":["agent_01ABC"],"authorization_token":"` + secret + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(srv, "POST", "/v1/agents/"+id, patch)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("update status = %d, want 400: %s", rec.Code, rec.Body)
			}
			assertNoSecret(t, "update response", rec.Body.String(), secret)
		})
	}

	// Every read path that echoes stored agent configuration must stay clean.
	for _, path := range []string{
		"/v1/agents/" + id,
		"/v1/agents/" + id + "/versions",
		"/v1/agents",
	} {
		read := do(srv, "GET", path, "")
		if read.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d: %s", path, read.Code, read.Body)
		}
		assertNoSecret(t, path, read.Body.String(), secret)
	}

	// The rejected updates must not have produced a new version either.
	versions := do(srv, "GET", "/v1/agents/"+id+"/versions", "")
	var page map[string]any
	if err := json.Unmarshal(versions.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode versions response: %v", err)
	}
	if data, _ := page["data"].([]any); len(data) != 1 {
		t.Fatalf("rejected updates created versions: %#v", page["data"])
	}
}

func TestAgents_AcceptsFullyDocumentedNestedConfig(t *testing.T) {
	srv := newTestServer(t)
	rec := do(srv, "POST", "/v1/agents",
		`{"name":"Agent","model":"claude-opus-4-8",`+
			`"tools":[`+
			`{"type":"agent_toolset_20260401","default_config":{"enabled":true,`+
			`"permission_policy":{"type":"always_allow"}},`+
			`"configs":[{"name":"bash","enabled":true,`+
			`"permission_policy":{"type":"always_ask"}}]},`+
			`{"type":"custom","name":"get_weather","description":"d",`+
			`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},`+
			`"required":["city"]}},`+
			`{"type":"mcp_toolset","mcp_server_name":"x",`+
			`"default_config":{"enabled":true},`+
			`"configs":[{"name":"list_issues","enabled":false}]}],`+
			`"mcp_servers":[{"name":"x","type":"url","url":"https://e.com"}],`+
			`"skills":[{"type":"anthropic","skill_id":"xlsx","version":"latest"}],`+
			`"multiagent":{"type":"coordinator","agents":["agent_01ABC",`+
			`{"type":"agent","id":"agent_01DEF","version":2},{"type":"self"}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("documented configuration status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func assertNoSecret(t *testing.T, where, body, secret string) {
	t.Helper()
	if strings.Contains(body, secret) {
		t.Fatalf("%s leaked the rejected credential: %s", where, body)
	}
}

// helpers used across httpapi black-box tests
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return NewTestHandler(t)
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
