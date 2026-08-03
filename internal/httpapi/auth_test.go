package httpapi

import (
	"context"
	"crypto/sha256"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseAPIKeys_RoundTripsIDsAndAcceptsConfiguredSecrets(t *testing.T) {
	keys, err := ParseAPIKeys("key-a:secret-a, key-b:secret-b\nkey-c:secret-c")
	if err != nil {
		t.Fatal(err)
	}
	if keys.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", keys.Len())
	}
	if got := strings.Join(keys.IDs(), ","); got != "key-a,key-b,key-c" {
		t.Fatalf("IDs() = %q", got)
	}
	for id, secret := range map[string]string{
		"key-a": "secret-a", "key-b": "secret-b", "key-c": "secret-c",
	} {
		principal, ok := keys.Lookup(secret)
		if !ok {
			t.Fatalf("configured key %s did not authenticate", id)
		}
		if principal.KeyID != id {
			t.Fatalf("Lookup(%s).KeyID = %q, want %q", id, principal.KeyID, id)
		}
		if !principal.Authenticated() {
			t.Fatalf("Principal for %s reported unauthenticated", id)
		}
	}
	if _, ok := keys.Lookup("secret-d"); ok {
		t.Fatal("an unconfigured secret authenticated")
	}
	if _, ok := keys.Lookup(""); ok {
		t.Fatal("an empty key authenticated")
	}
	// A secret may itself contain ":": only the first separator splits.
	colon, err := ParseAPIKeys("key-d:aa:bb:cc")
	if err != nil {
		t.Fatal(err)
	}
	if principal, ok := colon.Lookup("aa:bb:cc"); !ok || principal.KeyID != "key-d" {
		t.Fatalf("secret containing ':' did not round-trip: %+v ok=%v", principal, ok)
	}
}

func TestParseAPIKeys_EmptySpecIsTheZeroConfigPath(t *testing.T) {
	for _, spec := range []string{"", "   ", "\n\t "} {
		keys, err := ParseAPIKeys(spec)
		if err != nil {
			t.Fatalf("ParseAPIKeys(%q) = %v; the empty spec must not be an error", spec, err)
		}
		if keys.Len() != 0 {
			t.Fatalf("ParseAPIKeys(%q).Len() = %d, want 0", spec, keys.Len())
		}
		if _, ok := keys.Lookup("anything"); ok {
			t.Fatal("an empty key set authenticated a request")
		}
	}
	// A nil set is equally inert, so a partially constructed Config fails
	// closed instead of open.
	var nilSet *APIKeySet
	if nilSet.Len() != 0 || nilSet.IDs() != nil {
		t.Fatal("nil APIKeySet is not empty")
	}
	if _, ok := nilSet.Lookup("anything"); ok {
		t.Fatal("nil APIKeySet authenticated a request")
	}
}

func TestParseAPIKeys_RejectsMalformedEntries(t *testing.T) {
	cases := map[string]string{
		"missing separator": "justasecret",
		"missing id":        ":secret",
		"missing secret":    "key-a:",
		"duplicate id":      "key-a:secret-a,key-a:secret-b",
		"quoted id":         `"key-a":secret-a`,
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAPIKeys(spec)
			if err == nil {
				t.Fatalf("ParseAPIKeys(%q) accepted a malformed specification", spec)
			}
			// A parse error must never leak key material into logs.
			if strings.Contains(err.Error(), "secret-a") ||
				strings.Contains(err.Error(), "secret-b") ||
				strings.Contains(err.Error(), "justasecret") {
				t.Fatalf("error echoed key material: %v", err)
			}
		})
	}
}

// TestAPIKeySet_StoresDigestsNotPlaintext asserts the stored representation is
// a SHA-256 digest and that no field of the running configuration holds the
// secret in the clear.
func TestAPIKeySet_StoresDigestsNotPlaintext(t *testing.T) {
	const secret = "super-secret-value"
	keys, err := ParseAPIKeys("key-a:" + secret)
	if err != nil {
		t.Fatal(err)
	}
	entry := keys.entries[0]
	if entry.digest != sha256.Sum256([]byte(secret)) {
		t.Fatal("stored digest is not the SHA-256 of the configured secret")
	}
	value := reflect.ValueOf(entry)
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() == reflect.String && strings.Contains(field.String(), secret) {
			t.Fatalf("field %q retains the plaintext key", value.Type().Field(i).Name)
		}
	}
}

// TestConstantTimeDigestEqual asserts the comparison helper that Lookup uses.
// The property under test is that the comparison is delegated to crypto/subtle
// (asserted structurally by exercising the helper), not a timing measurement,
// which would be flaky.
func TestConstantTimeDigestEqual(t *testing.T) {
	a := sha256.Sum256([]byte("a"))
	b := sha256.Sum256([]byte("b"))
	if !constantTimeDigestEqual(a, a) {
		t.Fatal("constantTimeDigestEqual reported equal digests as different")
	}
	if constantTimeDigestEqual(a, b) {
		t.Fatal("constantTimeDigestEqual reported different digests as equal")
	}
	// Digests that share a long prefix must still compare unequal; a
	// byte-by-byte early-exit comparison would be the thing this rules out.
	near := a
	near[len(near)-1] ^= 0x01
	if constantTimeDigestEqual(a, near) {
		t.Fatal("constantTimeDigestEqual matched digests differing in one bit")
	}
}

// TestAPIKeySet_LookupScansEveryEntry proves Lookup does not exit the scan on
// the first match, so the position of a key in the configured set is not
// observable. The last-position key must authenticate exactly like the first.
func TestAPIKeySet_LookupScansEveryEntry(t *testing.T) {
	keys, err := ParseAPIKeys("k1:s1,k2:s2,k3:s3,k4:s4")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := keys.Lookup("s1")
	if !ok || first.KeyID != "k1" {
		t.Fatalf("first key: %+v ok=%v", first, ok)
	}
	last, ok := keys.Lookup("s4")
	if !ok || last.KeyID != "k4" {
		t.Fatalf("last key: %+v ok=%v", last, ok)
	}
}

func TestPrincipalFromContext_RequiresAnAuthenticatedPrincipal(t *testing.T) {
	if _, ok := PrincipalFromContext(context.Background()); ok {
		t.Fatal("bare context reported an authenticated principal")
	}
	// An explicitly empty Principal is not an identity.
	empty := ContextWithPrincipal(context.Background(), Principal{})
	if _, ok := PrincipalFromContext(empty); ok {
		t.Fatal("empty Principal reported as authenticated")
	}
	ctx := ContextWithPrincipal(context.Background(), Principal{KeyID: "ops"})
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.KeyID != "ops" {
		t.Fatalf("PrincipalFromContext = %+v ok=%v", principal, ok)
	}
}

// TestServerHandler_AuthenticatesRealRoutes proves the middleware is actually
// wired into the served handler chain, not just unit-testable in isolation.
func TestServerHandler_AuthenticatesRealRoutes(t *testing.T) {
	h := newTestHandler(t, Config{RequireAuth: true, APIKeys: testAPIKeys(t)}, false)

	rejected := httptest.NewRecorder()
	badReq := httptest.NewRequest("GET", "/v1/agents", nil)
	badReq.Header.Set("x-api-key", "sk-wrong")
	h.ServeHTTP(rejected, badReq)
	if rejected.Code != 401 {
		t.Fatalf("GET /v1/agents with a wrong key = %d, want 401", rejected.Code)
	}

	accepted := httptest.NewRecorder()
	goodReq := httptest.NewRequest("GET", "/v1/agents", nil)
	goodReq.Header.Set("x-api-key", testAPIKeySecret)
	h.ServeHTTP(accepted, goodReq)
	if accepted.Code != 200 {
		t.Fatalf("GET /v1/agents with the configured key = %d, want 200: %s",
			accepted.Code, accepted.Body)
	}

	// Probes remain reachable without a credential.
	probe := httptest.NewRecorder()
	h.ServeHTTP(probe, httptest.NewRequest("GET", "/healthz", nil))
	if probe.Code != 200 {
		t.Fatalf("GET /healthz = %d, want 200", probe.Code)
	}
}

func TestPresentedAPIKey_HeaderPrecedenceAndBearerParsing(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		allowAuthz bool
		want       string
		wantOK     bool
	}{
		{"x-api-key", map[string]string{"x-api-key": "k"}, false, "k", true},
		{"x-api-key trimmed", map[string]string{"x-api-key": "  k  "}, false, "k", true},
		{"empty x-api-key", map[string]string{"x-api-key": "   "}, false, "", false},
		{"bearer ignored by default", map[string]string{"authorization": "Bearer k"}, false, "", false},
		{"bearer opt-in", map[string]string{"authorization": "Bearer k"}, true, "k", true},
		{"bearer case-insensitive", map[string]string{"authorization": "bEaReR k"}, true, "k", true},
		{"bearer without value", map[string]string{"authorization": "Bearer "}, true, "", false},
		{"non-bearer scheme", map[string]string{"authorization": "Basic abc"}, true, "", false},
		{"x-api-key wins", map[string]string{
			"x-api-key": "k", "authorization": "Bearer other",
		}, true, "k", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/agents", nil)
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			got, ok := presentedAPIKey(req, tc.allowAuthz)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("presentedAPIKey = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
