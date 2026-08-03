package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// POST /v1/environments/{environment_id} documents exactly five body fields:
// config, description, metadata, name, and scope. Every omitted field preserves
// the stored value, `type` is required inside a supplied config, and a metadata
// key is deleted by null OR by the empty string — a rule that differs from
// Update Session, which deletes only on null.

func createEnvironment(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	rec := do(h, "POST", "/v1/environments", body)
	if rec.Code != 200 {
		t.Fatalf("create environment: %d %s", rec.Code, rec.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created environment: %v", err)
	}
	return created
}

func updateEnvironment(t *testing.T, h http.Handler, id, body string) map[string]any {
	t.Helper()
	rec := do(h, "POST", "/v1/environments/"+id, body)
	if rec.Code != 200 {
		t.Fatalf("update environment %s with %s: %d %s", id, body, rec.Code, rec.Body)
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated environment: %v", err)
	}
	return updated
}

func TestEnvironmentResponseCarriesDocumentedFields(t *testing.T) {
	srv := newTestServer(t)
	created := createEnvironment(t, srv,
		`{"name":"python","description":"data analysis","config":{"type":"cloud"},
		  "metadata":{"team":"ml"},"scope":"organization"}`)
	for _, field := range []string{
		"id", "archived_at", "config", "created_at", "description",
		"metadata", "name", "type", "updated_at", "scope",
	} {
		if _, ok := created[field]; !ok {
			t.Errorf("environment response missing %q: %v", field, created)
		}
	}
	if created["description"] != "data analysis" || created["scope"] != "organization" {
		t.Fatalf("create did not round-trip description/scope: %v", created)
	}
}

func TestUpdateEnvironment_OmittedFieldsPreserveStoredValues(t *testing.T) {
	srv := newTestServer(t)
	created := createEnvironment(t, srv,
		`{"name":"python","description":"original","config":{"type":"cloud",
		  "packages":{"pip":["pandas"]}},"metadata":{"team":"ml"},"scope":"account"}`)
	id := created["id"].(string)

	updated := updateEnvironment(t, srv, id, `{"description":"updated"}`)
	if updated["description"] != "updated" {
		t.Fatalf("description = %v, want updated", updated["description"])
	}
	if updated["name"] != "python" {
		t.Fatalf("omitted name was not preserved: %v", updated["name"])
	}
	if updated["scope"] != "account" {
		t.Fatalf("omitted scope was not preserved: %v", updated["scope"])
	}
	metadata := updated["metadata"].(map[string]any)
	if metadata["team"] != "ml" {
		t.Fatalf("omitted metadata was not preserved: %v", metadata)
	}
	config := updated["config"].(map[string]any)
	packages := config["packages"].(map[string]any)
	pip := packages["pip"].([]any)
	if len(pip) != 1 || pip[0] != "pandas" {
		t.Fatalf("omitted config was not preserved: %v", config)
	}
	if updated["updated_at"] == nil || updated["created_at"] == nil {
		t.Fatalf("timestamps missing: %v", updated)
	}
}

func TestUpdateEnvironment_MetadataDeletesOnNullAndEmptyString(t *testing.T) {
	// Update Environment: "Set a value to null or empty string to delete the
	// key." Update Session deletes only on null, so the two rules must not be
	// implemented by a shared helper.
	srv := newTestServer(t)
	created := createEnvironment(t, srv,
		`{"name":"e","config":{"type":"cloud"},
		  "metadata":{"keep":"yes","byNull":"a","byEmpty":"b"}}`)
	id := created["id"].(string)

	updated := updateEnvironment(t, srv, id,
		`{"metadata":{"byNull":null,"byEmpty":"","added":"new"}}`)
	metadata := updated["metadata"].(map[string]any)
	if _, ok := metadata["byNull"]; ok {
		t.Errorf("null value did not delete the key: %v", metadata)
	}
	if _, ok := metadata["byEmpty"]; ok {
		t.Errorf("empty-string value did not delete the key: %v", metadata)
	}
	if metadata["keep"] != "yes" {
		t.Errorf("unmentioned key was not preserved: %v", metadata)
	}
	if metadata["added"] != "new" {
		t.Errorf("new key was not upserted: %v", metadata)
	}
}

func TestUpdateEnvironment_MetadataValidation(t *testing.T) {
	srv := newTestServer(t)
	id := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)
	for _, body := range []string{
		`{"metadata":{"n":5}}`,
		`{"metadata":{"n":true}}`,
		`{"metadata":{"n":{"nested":"x"}}}`,
		`{"metadata":[]}`,
	} {
		rec := do(srv, "POST", "/v1/environments/"+id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestUpdateEnvironment_ConfigRequiresTypeAndMergesOmittedFields(t *testing.T) {
	srv := newTestServer(t)
	created := createEnvironment(t, srv,
		`{"name":"e","config":{"type":"cloud",
		  "networking":{"type":"limited","allow_mcp_servers":true,
		                "allow_package_managers":true,"allowed_hosts":["a.example.com"]},
		  "packages":{"pip":["pandas"]}}}`)
	id := created["id"].(string)

	// `type` is required inside a supplied config object.
	if rec := do(srv, "POST", "/v1/environments/"+id, `{"config":{}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("config without type = %d, want 400: %s", rec.Code, rec.Body)
	}
	if rec := do(srv, "POST", "/v1/environments/"+id,
		`{"config":{"networking":{"type":"unrestricted"}}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("config without type = %d, want 400: %s", rec.Code, rec.Body)
	}

	// Supplying only `networking` preserves `packages`.
	updated := updateEnvironment(t, srv, id,
		`{"config":{"type":"cloud","networking":{"type":"limited","allowed_hosts":["b.example.com"]}}}`)
	config := updated["config"].(map[string]any)
	networking := config["networking"].(map[string]any)
	if networking["allow_mcp_servers"] != true || networking["allow_package_managers"] != true {
		t.Errorf("omitted limited-network fields were not preserved: %v", networking)
	}
	hosts := networking["allowed_hosts"].([]any)
	if len(hosts) != 1 || hosts[0] != "b.example.com" {
		t.Errorf("allowed_hosts = %v, want the supplied value", hosts)
	}
	pip := config["packages"].(map[string]any)["pip"].([]any)
	if len(pip) != 1 || pip[0] != "pandas" {
		t.Errorf("omitted packages were not preserved: %v", config["packages"])
	}

	// Supplying only `packages` preserves `networking` and replaces packages
	// wholesale: upstream attaches the preserve note to config and to the
	// limited-network params, not to the packages params.
	updated = updateEnvironment(t, srv, id,
		`{"config":{"type":"cloud","packages":{"npm":["typescript"]}}}`)
	config = updated["config"].(map[string]any)
	if _, ok := config["networking"].(map[string]any); !ok {
		t.Errorf("omitted networking was not preserved: %v", config)
	}
	packages := config["packages"].(map[string]any)
	if _, ok := packages["pip"]; ok {
		t.Errorf("packages were merged instead of replaced: %v", packages)
	}
	if packages["npm"].([]any)[0] != "typescript" {
		t.Errorf("packages npm = %v", packages["npm"])
	}
}

func TestUpdateEnvironment_NetworkingSwitchUsesDocumentedDefaults(t *testing.T) {
	srv := newTestServer(t)
	created := createEnvironment(t, srv,
		`{"name":"e","config":{"type":"cloud","networking":{"type":"unrestricted"}}}`)
	id := created["id"].(string)

	updated := updateEnvironment(t, srv, id,
		`{"config":{"type":"cloud","networking":{"type":"limited"}}}`)
	networking := updated["config"].(map[string]any)["networking"].(map[string]any)
	if networking["allow_mcp_servers"] != false || networking["allow_package_managers"] != false {
		t.Fatalf("switching from unrestricted must start from the documented false defaults: %v",
			networking)
	}
	if hosts := networking["allowed_hosts"].([]any); len(hosts) != 0 {
		t.Fatalf("allowed_hosts = %v, want empty", hosts)
	}

	// Switching back keeps only the unrestricted marker.
	updated = updateEnvironment(t, srv, id,
		`{"config":{"type":"cloud","networking":{"type":"unrestricted"}}}`)
	networking = updated["config"].(map[string]any)["networking"].(map[string]any)
	if len(networking) != 1 || networking["type"] != "unrestricted" {
		t.Fatalf("unrestricted networking = %v", networking)
	}
}

func TestUpdateEnvironment_ConfigValidation(t *testing.T) {
	srv := newTestServer(t)
	id := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)
	for _, body := range []string{
		`{"config":{"type":"docker"}}`,
		`{"config":{"type":"self_hosted"}}`, // local choice: no in-place type change
		`{"config":{"type":"cloud","unknown":1}}`,
		`{"config":{"type":"cloud","networking":{"type":"open"}}}`,
		`{"config":{"type":"cloud","networking":{"type":"limited","allow_mcp_servers":"yes"}}}`,
		`{"config":{"type":"cloud","networking":{"type":"limited","allowed_hosts":[1]}}}`,
		`{"config":{"type":"cloud","packages":{"pip":"pandas"}}}`,
		`{"config":{"type":"cloud","packages":{"type":"deps"}}}`,
		`{"config":[]}`,
	} {
		rec := do(srv, "POST", "/v1/environments/"+id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestUpdateEnvironment_RejectsUndocumentedBodyFields(t *testing.T) {
	// The documented body is exactly config, description, metadata, name, scope.
	srv := newTestServer(t)
	id := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)
	for _, body := range []string{
		`{"config_type":"cloud"}`,
		`{"archived_at":null}`,
		`{"id":"env_other"}`,
	} {
		rec := do(srv, "POST", "/v1/environments/"+id, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, rec.Code, rec.Body)
		}
	}
}

func TestUpdateEnvironment_ScopeValidation(t *testing.T) {
	srv := newTestServer(t)
	id := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)
	if updated := updateEnvironment(t, srv, id, `{"scope":"account"}`); updated["scope"] != "account" {
		t.Fatalf("scope = %v, want account", updated["scope"])
	}
	if updated := updateEnvironment(t, srv, id, `{"scope":"organization"}`); updated["scope"] != "organization" {
		t.Fatalf("scope = %v, want organization", updated["scope"])
	}
	if rec := do(srv, "POST", "/v1/environments/"+id, `{"scope":"team"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("scope=team = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestUpdateEnvironment_EmptyBodyIsANoOpAndUnknownIDIs404(t *testing.T) {
	srv := newTestServer(t)
	created := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)
	id := created["id"].(string)

	unchanged := updateEnvironment(t, srv, id, `{}`)
	if unchanged["name"] != created["name"] || unchanged["updated_at"] != created["updated_at"] {
		t.Fatalf("empty update mutated the environment: %v -> %v", created, unchanged)
	}
	if rec := do(srv, "POST", "/v1/environments/env_missing", `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown environment = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestUpdateEnvironment_ArchivedIsReadOnly(t *testing.T) {
	srv := newTestServer(t)
	id := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)
	if rec := do(srv, "POST", "/v1/environments/"+id+"/archive", ""); rec.Code != 200 {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body)
	}
	if rec := do(srv, "POST", "/v1/environments/"+id, `{"name":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("archived environment update = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestUpdateEnvironment_DoesNotDisturbRunningSessions(t *testing.T) {
	// Upstream does not document what an environment update does to sessions
	// that are already running against it, so Mango does not propagate: the
	// session keeps the environment type it was admitted with.
	srv := newTestServer(t)
	environmentID := createEnvironment(t, srv, `{"name":"e","config":{"type":"cloud"}}`)["id"].(string)

	rec := do(srv, "POST", "/v1/agents", `{"name":"a","model":"claude-opus-4-8"}`)
	if rec.Code != 200 {
		t.Fatalf("create agent: %d %s", rec.Code, rec.Body)
	}
	var agent map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &agent); err != nil {
		t.Fatalf("decode agent: %v", err)
	}

	rec = do(srv, "POST", "/v1/sessions",
		`{"agent":"`+agent["id"].(string)+`","environment_id":"`+environmentID+`"}`)
	if rec.Code != 200 {
		t.Fatalf("create session: %d %s", rec.Code, rec.Body)
	}
	var session map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	sessionID := session["id"].(string)

	updateEnvironment(t, srv, environmentID,
		`{"name":"renamed","config":{"type":"cloud","packages":{"pip":["numpy"]}}}`)

	rec = do(srv, "GET", "/v1/sessions/"+sessionID, "")
	if rec.Code != 200 {
		t.Fatalf("get session: %d %s", rec.Code, rec.Body)
	}
	var after map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode session after update: %v", err)
	}
	if after["environment_id"] != environmentID {
		t.Fatalf("session environment_id changed: %v", after["environment_id"])
	}
	if after["status"] != session["status"] || after["updated_at"] != session["updated_at"] {
		t.Fatalf("environment update disturbed the session: %v -> %v", session, after)
	}
}
