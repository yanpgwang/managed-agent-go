package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestWriteError_MapsKinds(t *testing.T) {
	cases := map[error]int{
		domain.Validation("x"):  400,
		domain.Conflict("x"):    409,
		domain.NotFound("x"):    404,
		domain.Unsupported("x"): 422,
		domain.TooLarge("x"):    413,
	}
	for err, want := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, err)
		if rec.Code != want {
			t.Errorf("err %v: got %d want %d", err, rec.Code, want)
		}
		env := decodeErrorEnvelope(t, rec.Body.Bytes())
		if env["message"] == "" || env["type"] == "" {
			t.Errorf("missing envelope fields: %v", env)
		}
		if rec.Header().Get("request-id") == "" {
			t.Errorf("missing request-id response header")
		}
	}
}

// decodeErrorEnvelope unwraps the standard {"type":"error","error":{...}}
// envelope into the inner {type,message} object.
func decodeErrorEnvelope(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var outer struct {
		Type  string            `json:"type"`
		Error map[string]string `json:"error"`
	}
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Fatalf("unmarshal error envelope: %v (%s)", err, body)
	}
	if outer.Type != "error" {
		t.Fatalf("expected top-level type=error, got %q", outer.Type)
	}
	return outer.Error
}

func TestBetaMiddleware_Strict(t *testing.T) {
	h := betaMiddleware(Config{RequireBeta: true}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 without beta header, got %d", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env["type"] == "" || env["message"] == "" {
		t.Errorf("expected non-empty type and message envelope, got %v", env)
	}
	req.Header.Set("anthropic-beta", "managed-agents-2026-04-01")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 with beta header, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Add("anthropic-beta", "another-beta")
	req.Header.Add("anthropic-beta", "managed-agents-2026-04-01")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 with beta token among multiple values, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("anthropic-beta", "prefix-managed-agents-2026-04-01-suffix")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for beta-token substring, got %d", rec.Code)
	}
}

// TestAuthMiddleware_Strict covers the authentication contract: a request must
// present a key the server actually accepts. Header presence is not
// authentication, so an unknown key is rejected exactly like a missing one.
//
// 401 with the `authentication_error` type is a Mango local choice: upstream
// documents the error type but binds no HTTP status code to an authentication
// failure and draws no missing-versus-invalid distinction.
func TestAuthMiddleware_Strict(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	keys, err := ParseAPIKeys("key-a:secret-a,key-b:secret-b")
	if err != nil {
		t.Fatal(err)
	}
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys}, ok)

	// Missing key -> 401 with the authentication_error envelope.
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("expected 401 without x-api-key, got %d", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env["type"] != "authentication_error" || env["message"] == "" {
		t.Errorf("expected authentication_error envelope with a message, got %v", env)
	}

	// Unknown key -> 401. This is the case the old presence-only check let
	// through: any non-empty string authenticated.
	reqUnknown := httptest.NewRequest("GET", "/v1/agents", nil)
	reqUnknown.Header.Set("x-api-key", "sk-not-configured")
	recUnknown := httptest.NewRecorder()
	h.ServeHTTP(recUnknown, reqUnknown)
	if recUnknown.Code != 401 {
		t.Fatalf("expected 401 for an unknown x-api-key, got %d", recUnknown.Code)
	}
	envUnknown := decodeErrorEnvelope(t, recUnknown.Body.Bytes())
	if envUnknown["type"] != "authentication_error" {
		t.Errorf("error type = %q, want authentication_error", envUnknown["type"])
	}
	if strings.Contains(recUnknown.Body.String(), "sk-not-configured") {
		t.Errorf("rejection echoed the presented key: %s", recUnknown.Body)
	}

	// A configured key -> 200 for every configured key, so rotation works.
	for id, secret := range map[string]string{"key-a": "secret-a", "key-b": "secret-b"} {
		reqOK := httptest.NewRequest("GET", "/v1/agents", nil)
		reqOK.Header.Set("x-api-key", secret)
		recOK := httptest.NewRecorder()
		h.ServeHTTP(recOK, reqOK)
		if recOK.Code != 200 {
			t.Fatalf("expected 200 with configured key %s, got %d", id, recOK.Code)
		}
	}

	// RequireAuth: false — missing key → 200 (passes through). This is the
	// documented zero-config local development path.
	hNoAuth := authMiddleware(Config{RequireAuth: false}, ok)
	req3 := httptest.NewRequest("GET", "/v1/agents", nil)
	rec3 := httptest.NewRecorder()
	hNoAuth.ServeHTTP(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("expected 200 when RequireAuth=false and no key, got %d", rec3.Code)
	}
}

// TestAuthMiddleware_FailsClosedWithoutConfiguredKeys asserts that requiring
// auth without any key material rejects everything rather than accepting
// anything.
func TestAuthMiddleware_FailsClosedWithoutConfiguredKeys(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	for _, cfg := range []Config{
		{RequireAuth: true},
		{RequireAuth: true, APIKeys: &APIKeySet{}},
	} {
		h := authMiddleware(cfg, ok)
		req := httptest.NewRequest("GET", "/v1/agents", nil)
		req.Header.Set("x-api-key", "anything")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("RequireAuth with no keys accepted a request: got %d", rec.Code)
		}
	}
}

// TestAuthMiddleware_PrincipalReachableFromHandler proves the context seam a
// future Memory-store `api_key_id` actor attribution will read from: the
// downstream handler can resolve the authenticated principal, and it carries
// the non-secret key id rather than the key.
func TestAuthMiddleware_PrincipalReachableFromHandler(t *testing.T) {
	keys, err := ParseAPIKeys("ops-key:secret-value")
	if err != nil {
		t.Fatal(err)
	}
	var (
		seen  Principal
		found bool
	)
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, found = PrincipalFromContext(r.Context())
			w.WriteHeader(200)
		}))
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("x-api-key", "secret-value")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !found {
		t.Fatal("handler could not resolve a Principal from the request context")
	}
	if seen.KeyID != "ops-key" {
		t.Fatalf("Principal.KeyID = %q, want ops-key", seen.KeyID)
	}
	if strings.Contains(seen.KeyID, "secret-value") {
		t.Fatal("Principal carried key material")
	}
}

// TestAuthMiddleware_NoPrincipalWhenAuthDisabled asserts that the zero-config
// path leaves the context empty, so a caller can never mistake "authentication
// disabled" for "known caller".
func TestAuthMiddleware_NoPrincipalWhenAuthDisabled(t *testing.T) {
	var found bool
	h := authMiddleware(Config{RequireAuth: false},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, found = PrincipalFromContext(r.Context())
			w.WriteHeader(200)
		}))
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("x-api-key", "whatever")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if found {
		t.Fatal("a Principal was published even though authentication is disabled")
	}
}

// TestAuthMiddleware_AuthorizationHeaderIsOptIn asserts that `authorization:
// Bearer` is rejected by default. Upstream documents only x-api-key; accepting
// Bearer is a non-upstream extension and must be requested explicitly.
func TestAuthMiddleware_AuthorizationHeaderIsOptIn(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}

	defaultHandler := authMiddleware(Config{RequireAuth: true, APIKeys: keys}, ok)
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("authorization", "Bearer secret-a")
	rec := httptest.NewRecorder()
	defaultHandler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("authorization: Bearer accepted by default, got %d", rec.Code)
	}

	optIn := authMiddleware(Config{
		RequireAuth: true, APIKeys: keys, AllowAuthorizationHeader: true,
	}, ok)
	reqOptIn := httptest.NewRequest("GET", "/v1/agents", nil)
	reqOptIn.Header.Set("authorization", "Bearer secret-a")
	recOptIn := httptest.NewRecorder()
	optIn.ServeHTTP(recOptIn, reqOptIn)
	if recOptIn.Code != 200 {
		t.Fatalf("opt-in authorization: Bearer rejected, got %d", recOptIn.Code)
	}

	// The opt-in still rejects a Bearer value that is not a configured key.
	reqBad := httptest.NewRequest("GET", "/v1/agents", nil)
	reqBad.Header.Set("authorization", "Bearer not-a-key")
	recBad := httptest.NewRecorder()
	optIn.ServeHTTP(recBad, reqBad)
	if recBad.Code != 401 {
		t.Fatalf("opt-in accepted an unknown Bearer value, got %d", recBad.Code)
	}
}

// TestAuthMiddleware_ProbesStayUnauthenticated asserts the local choice that
// liveness and readiness probes do not need a credential, so an orchestrator
// can schedule the process without one.
func TestAuthMiddleware_ProbesStayUnauthenticated(t *testing.T) {
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s required authentication: got %d", path, rec.Code)
		}
	}
	// Everything else still does.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/agents", nil))
	if rec.Code != 401 {
		t.Fatalf("resource route did not require authentication: got %d", rec.Code)
	}
}

func TestVersionAndContentTypeMiddleware_Strict(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	h := versionMiddleware(Config{RequireVersion: true},
		contentTypeMiddleware(Config{RequireContentType: true}, ok))

	req := httptest.NewRequest("POST", "/v1/agents", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 without anthropic-version, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/v1/agents", strings.NewReader(`{}`))
	req.Header.Set("anthropic-version", anthropicVersion)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 without application/json, got %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/v1/agents", strings.NewReader(`{}`))
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json; charset=utf-8")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 with required headers, got %d", rec.Code)
	}
}

func TestRequestIDMiddleware_AllResponses(t *testing.T) {
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, domain.Validation("bad"))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	headerID := rec.Header().Get("request-id")
	if headerID == "" {
		t.Fatal("missing request-id response header")
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != headerID {
		t.Fatalf("body request_id %q != header %q", body.RequestID, headerID)
	}
}

func TestUnknownRouteUsesJSONErrorEnvelope(t *testing.T) {
	h := NewTestHandler(t)
	rec := do(h, "GET", "/v1/not-a-resource", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env["type"] != "not_found_error" {
		t.Fatalf("error type = %q, want not_found_error", env["type"])
	}
	if rec.Header().Get("request-id") == "" {
		t.Fatal("missing request-id header")
	}
}
