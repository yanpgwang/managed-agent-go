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
// `401` with the `authentication_error` type is a documented contract: the
// Claude API errors page binds status codes to error types explicitly,
// including "401 - `authentication_error`". Only the identical wording for a
// missing versus an invalid credential is Mango's own choice.
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

// TestAuthMiddleware_AcceptsDocumentedBearerCredential asserts that
// `authorization: Bearer <token>` authenticates by default. The Claude API
// overview lists `x-api-key` and `Authorization` side by side, each marked "One
// of `x-api-key` or `Authorization`", so rejecting the bearer form would fail a
// caller authenticating the documented way.
//
// Mango runs no token service: it validates the presented bearer value against
// the same configured key set as `x-api-key`, so presence is still never
// sufficient.
func TestAuthMiddleware_AcceptsDocumentedBearerCredential(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys}, ok)

	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("authorization", "Bearer secret-a")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("documented `authorization: Bearer` was rejected, got %d", rec.Code)
	}

	// The presence-only vulnerability must stay fixed on this path too: an
	// unconfigured bearer value is rejected exactly like a missing credential.
	reqBad := httptest.NewRequest("GET", "/v1/agents", nil)
	reqBad.Header.Set("authorization", "Bearer not-a-key")
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, reqBad)
	if recBad.Code != 401 {
		t.Fatalf("an unknown bearer value authenticated, got %d", recBad.Code)
	}
	if env := decodeErrorEnvelope(t, recBad.Body.Bytes()); env["type"] != "authentication_error" {
		t.Fatalf("error type = %q, want authentication_error", env["type"])
	}
	if strings.Contains(recBad.Body.String(), "not-a-key") {
		t.Fatal("the rejection echoed the presented credential")
	}
}

// TestAuthMiddleware_BothHeadersAreTried covers a request that presents both
// documented credential headers with different values.
//
// This is not a hypothetical: `anthropic.NewClient` reads ANTHROPIC_AUTH_TOKEN
// from the environment into an `Authorization: Bearer` header, and an explicit
// `option.WithAPIKey` adds `X-Api-Key` alongside it, so any developer with that
// variable exported sends both on every request. Each candidate is tried, and
// one accepted credential is enough.
func TestAuthMiddleware_BothHeadersAreTried(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys}, ok)

	cases := []struct {
		name   string
		apiKey string
		bearer string
		want   int
	}{
		{"valid x-api-key, unrelated ambient bearer", "secret-a", "sk-ant-unrelated", 200},
		{"unrelated ambient x-api-key, valid bearer", "sk-ant-unrelated", "secret-a", 200},
		{"same credential in both headers", "secret-a", "secret-a", 200},
		{"neither header carries a configured key", "nope-1", "nope-2", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/agents", nil)
			req.Header.Set("x-api-key", tc.apiKey)
			req.Header.Set("authorization", "Bearer "+tc.bearer)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == 401 {
				env := decodeErrorEnvelope(t, rec.Body.Bytes())
				if env["type"] != "authentication_error" {
					t.Fatalf("error type = %q, want authentication_error", env["type"])
				}
				for _, secret := range []string{tc.apiKey, tc.bearer} {
					if strings.Contains(rec.Body.String(), secret) {
						t.Fatalf("the rejection echoed a presented credential %q", secret)
					}
				}
			}
		})
	}
}

// TestAuthMiddleware_ResolvesPrincipalFromWhicheverHeaderMatched asserts the
// Principal follows the credential that actually authenticated, so actor
// attribution stays correct when the other header holds an unrelated value.
func TestAuthMiddleware_ResolvesPrincipalFromWhicheverHeaderMatched(t *testing.T) {
	keys, err := ParseAPIKeys("key-a:secret-a,key-b:secret-b")
	if err != nil {
		t.Fatal(err)
	}
	var seen Principal
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, _ = PrincipalFromContext(r.Context())
			w.WriteHeader(200)
		}))

	// Only the bearer header carries a configured key.
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("x-api-key", "sk-ant-unrelated")
	req.Header.Set("authorization", "Bearer secret-b")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen.KeyID != "key-b" {
		t.Fatalf("Principal.KeyID = %q, want key-b", seen.KeyID)
	}

	// When both are configured keys, x-api-key is tried first and wins.
	both := httptest.NewRequest("GET", "/v1/agents", nil)
	both.Header.Set("x-api-key", "secret-a")
	both.Header.Set("authorization", "Bearer secret-b")
	h.ServeHTTP(httptest.NewRecorder(), both)
	if seen.KeyID != "key-a" {
		t.Fatalf("Principal.KeyID = %q, want key-a (x-api-key is tried first)", seen.KeyID)
	}
}

// TestAuthMiddleware_AuthorizationHeaderCanBeDisabled asserts the knob narrows
// rather than widens: it removes a documented header for a deployment whose
// ingress already uses `Authorization`, and the default leaves it accepted.
func TestAuthMiddleware_AuthorizationHeaderCanBeDisabled(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}
	disabled := authMiddleware(Config{
		RequireAuth: true, APIKeys: keys, DisableAuthorizationHeader: true,
	}, ok)

	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("authorization", "Bearer secret-a")
	rec := httptest.NewRecorder()
	disabled.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("bearer accepted after being disabled, got %d", rec.Code)
	}
	// The rejection must not advertise a header the server would refuse.
	if msg := decodeErrorEnvelope(t, rec.Body.Bytes())["message"]; strings.Contains(msg, "authorization") {
		t.Fatalf("message advertises a disabled header: %q", msg)
	}

	// x-api-key still works with the bearer form disabled.
	reqKey := httptest.NewRequest("GET", "/v1/agents", nil)
	reqKey.Header.Set("x-api-key", "secret-a")
	recKey := httptest.NewRecorder()
	disabled.ServeHTTP(recKey, reqKey)
	if recKey.Code != 200 {
		t.Fatalf("x-api-key = %d, want 200", recKey.Code)
	}
}

// TestAuthMiddleware_MissingCredentialIs401AuthenticationError pins the
// documented status/type pair. The Claude API errors page binds "401 -
// `authentication_error`", so this is a reproduced contract, not a local
// choice.
func TestAuthMiddleware_MissingCredentialIs401AuthenticationError(t *testing.T) {
	keys, err := ParseAPIKeys("key-a:secret-a")
	if err != nil {
		t.Fatal(err)
	}
	h := authMiddleware(Config{RequireAuth: true, APIKeys: keys},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/agents", nil))
	if rec.Code != 401 {
		t.Fatalf("missing credential = %d, want 401", rec.Code)
	}
	env := decodeErrorEnvelope(t, rec.Body.Bytes())
	if env["type"] != "authentication_error" {
		t.Fatalf("error type = %q, want authentication_error", env["type"])
	}
	for _, want := range []string{"x-api-key", "authorization"} {
		if !strings.Contains(env["message"], want) {
			t.Errorf("message %q does not name the %s header", env["message"], want)
		}
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
		for _, method := range []string{"GET", "HEAD"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != 200 {
				t.Errorf("%s %s required authentication: got %d", method, path, rec.Code)
			}
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
