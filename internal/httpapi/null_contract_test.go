package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentCreateRejectsNullForNonNullableFields(t *testing.T) {
	h := NewTestHandler(t)
	for _, field := range []string{"tools", "mcp_servers", "skills", "metadata"} {
		t.Run(field, func(t *testing.T) {
			response := do(
				h,
				http.MethodPost,
				"/v1/agents",
				`{"name":"agent","model":"claude-opus-4-8","`+field+`":null}`,
			)
			assertInvalidRequest(t, response)
		})
	}
}

func TestAgentUpdateRejectsNullWithoutCreatingVersion(t *testing.T) {
	h := NewTestHandler(t)
	agentID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/agents",
		`{"name":"stable","model":"claude-opus-4-8","metadata":{"team":"core"}}`,
	)

	for _, field := range []string{"name", "version", "metadata"} {
		t.Run(field, func(t *testing.T) {
			response := do(
				h,
				http.MethodPost,
				"/v1/agents/"+agentID,
				`{"`+field+`":null}`,
			)
			assertInvalidRequest(t, response)
		})
	}

	response := do(h, http.MethodGet, "/v1/agents/"+agentID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", response.Code, response.Body)
	}
	agent := decodeBody(t, response.Body.Bytes())
	if agent["version"] != float64(1) || agent["name"] != "stable" ||
		agent["metadata"].(map[string]any)["team"] != "core" {
		t.Fatalf("rejected update changed Agent: %#v", agent)
	}
}

func TestSessionCreateRejectsNullForNonNullableFields(t *testing.T) {
	h := NewTestHandler(t)
	agentID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/agents",
		`{"name":"agent","model":"claude-opus-4-8"}`,
	)
	environmentID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/environments",
		`{"name":"environment"}`,
	)
	base := `{"agent":"` + agentID + `","environment_id":"` + environmentID + `","%s":null}`

	for _, field := range []string{
		"title", "metadata", "initial_events", "resources", "vault_ids",
	} {
		t.Run(field, func(t *testing.T) {
			response := do(
				h,
				http.MethodPost,
				"/v1/sessions",
				fmt.Sprintf(base, field),
			)
			assertInvalidRequest(t, response)
		})
	}
}

func TestSessionUpdateRejectsNullTitleWithoutMutation(t *testing.T) {
	h := NewTestHandler(t)
	agentID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/agents",
		`{"name":"agent","model":"claude-opus-4-8"}`,
	)
	environmentID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/environments",
		`{"name":"environment"}`,
	)
	sessionID := createID(
		t,
		h,
		http.MethodPost,
		"/v1/sessions",
		`{"agent":"`+agentID+`","environment_id":"`+environmentID+`","title":"stable"}`,
	)

	response := do(h, http.MethodPost, "/v1/sessions/"+sessionID, `{"title":null}`)
	assertInvalidRequest(t, response)

	response = do(h, http.MethodGet, "/v1/sessions/"+sessionID, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", response.Code, response.Body)
	}
	if session := decodeBody(t, response.Body.Bytes()); session["title"] != "stable" {
		t.Fatalf("rejected update changed Session: %#v", session)
	}
}

func assertInvalidRequest(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body)
	}
	var body struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Error     struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Type != "error" || body.Error.Type != "invalid_request_error" ||
		body.Error.Message == "" || body.RequestID == "" {
		t.Fatalf("invalid error envelope: %#v", body)
	}
}
