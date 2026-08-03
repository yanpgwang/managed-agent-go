package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Authentication reproduces a documented contract.
//
// The Claude API overview's authentication table lists two credential headers,
// each marked "One of `x-api-key` or `Authorization`", and the Managed Agents
// API inherits them:
//
//   - `x-api-key: <key>` — a Console API key.
//   - `Authorization: Bearer <token>` — a short-lived access token obtained
//     from `POST /v1/oauth/token` through Workload Identity Federation.
//
// The Claude API errors page binds status codes to error types explicitly,
// including `401 - authentication_error`.
//
// **documented contract**: both header shapes are first-class credentials, and
// a rejected credential is answered with `401` and `authentication_error`.
//
// **local choice**: Mango operates no token service. It implements neither
// `POST /v1/oauth/token` nor Workload Identity Federation, so it accepts only
// the *shape* of a bearer credential and validates the presented token against
// the same configured key set as `x-api-key`. The token is opaque to Mango: it
// is not parsed, it carries no independent expiry, and no federation trust is
// evaluated.
//
// **design inference**: when both headers are present they are tried in order
// and the request authenticates if *either* carries an accepted credential.
// The documented table marks the two headers mutually exclusive ("one of")
// without saying what a server does with both, but the official Go SDK settles
// it: `anthropic.NewClient` reads `ANTHROPIC_AUTH_TOKEN` from the environment
// as a default `Authorization: Bearer` header, and an explicit
// `option.WithAPIKey` adds `X-Api-Key` alongside it. A developer with that
// variable exported therefore sends both headers with *different* values on
// every request, so rejecting the combination — or blindly preferring the
// wrong one — would break the official client. Trying both grants nothing
// extra: a caller still has to present at least one configured key, exactly as
// if they had sent that header alone.
//
// See docs/api/overview.md#authentication.

// Principal identifies the caller a request was authenticated as.
//
// It deliberately carries only non-secret identity. KeyID is the stable,
// operator-chosen label of the API key that authenticated the request; neither
// the key nor any digest of it is ever placed here, logged, or echoed.
//
// Principal is the seam that a future Memory-store `api_key_id` actor
// attribution will read from. This package does not implement actor attribution
// today; it only makes the authenticated identity reachable.
type Principal struct {
	// KeyID is the stable, non-secret identifier of the API key that
	// authenticated the request.
	KeyID string
}

// Authenticated reports whether the Principal identifies a caller.
func (p Principal) Authenticated() bool { return p.KeyID != "" }

// principalContextKey is unexported so no other package can inject or forge a
// Principal through context.WithValue; ContextWithPrincipal is the only way in.
type principalContextKey struct{}

// ContextWithPrincipal returns a copy of ctx carrying the authenticated
// principal.
func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the principal a request was authenticated as. It
// reports false when authentication is disabled or the request was not
// authenticated, so callers cannot mistake "no auth configured" for "known
// caller".
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok || !principal.Authenticated() {
		return Principal{}, false
	}
	return principal, true
}

// apiKeyEntry holds one accepted key. Only the SHA-256 digest of the secret is
// retained: the plaintext key never lives in the running configuration, and the
// digest is never logged or returned on the wire.
type apiKeyEntry struct {
	id     string
	digest [sha256.Size]byte
}

// APIKeySet is an immutable set of accepted API keys, keyed by a stable
// non-secret key id. Multiple keys are supported so a key can be rotated
// without a window where no key is valid.
//
// The zero value and a nil *APIKeySet are valid and accept nothing, so a
// misconfigured server fails closed rather than open.
type APIKeySet struct {
	entries []apiKeyEntry
}

// ParseAPIKeys builds an APIKeySet from an operator-supplied specification.
//
// The format is a comma-, newline-, or whitespace-separated list of
// `<key-id>:<secret>` entries, for example:
//
//	key-a:s3cret-one,key-b:s3cret-two
//
// The key id is a stable, non-secret label used for log lines and for the
// Principal carried on the request context. Only the first ":" separates the id
// from the secret, so a secret may itself contain ":".
//
// An empty specification returns an empty set and no error: that is the
// zero-config local development path, where the caller is expected to leave
// authentication disabled and warn.
func ParseAPIKeys(spec string) (*APIKeySet, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	set := &APIKeySet{}
	seenIDs := make(map[string]bool, len(fields))
	for _, field := range fields {
		id, secret, ok := strings.Cut(field, ":")
		id = strings.TrimSpace(id)
		secret = strings.TrimSpace(secret)
		// Never include the offending value in an error: it may be the secret.
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf(
				"configuration: API keys must be a list of \"<key-id>:<secret>\" entries; " +
					"one entry was missing an id or a secret")
		}
		if strings.ContainsAny(id, "\"'") {
			return nil, fmt.Errorf("configuration: API key id %q must not contain quotes", id)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("configuration: duplicate API key id %q", id)
		}
		seenIDs[id] = true
		set.entries = append(set.entries, apiKeyEntry{id: id, digest: sha256.Sum256([]byte(secret))})
	}
	return set, nil
}

// Len returns the number of accepted keys.
func (s *APIKeySet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.entries)
}

// IDs returns the configured non-secret key ids in sorted order. It is safe to
// log; it never exposes key material.
func (s *APIKeySet) IDs() []string {
	if s == nil {
		return nil
	}
	ids := make([]string, 0, len(s.entries))
	for _, entry := range s.entries {
		ids = append(ids, entry.id)
	}
	sort.Strings(ids)
	return ids
}

// Lookup resolves a presented key to its Principal.
//
// The comparison is constant time with respect to the configured key material:
// the presented key is hashed first so length never varies, every entry is
// compared with crypto/subtle, and the scan never exits early, so neither the
// match position nor a partial prefix match is observable through timing.
func (s *APIKeySet) Lookup(presented string) (Principal, bool) {
	if s == nil || len(s.entries) == 0 || presented == "" {
		return Principal{}, false
	}
	digest := sha256.Sum256([]byte(presented))
	var matched Principal
	found := 0
	for _, entry := range s.entries {
		if constantTimeDigestEqual(digest, entry.digest) {
			matched = Principal{KeyID: entry.id}
			found = 1
		}
	}
	if found == 0 {
		return Principal{}, false
	}
	return matched, true
}

// constantTimeDigestEqual compares two key digests in constant time. It exists
// as a named helper so the constant-time property is directly assertable in a
// test instead of being inferred from timing measurements.
func constantTimeDigestEqual(a, b [sha256.Size]byte) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// credentialSource names the header a request presented a credential in.
type credentialSource int

const (
	// credentialAPIKeyHeader: `x-api-key`.
	credentialAPIKeyHeader credentialSource = iota
	// credentialAuthorizationHeader: `authorization: Bearer <token>`.
	credentialAuthorizationHeader
)

// headerName names the header a credential came from, for an error message. It
// describes the request the caller made and never reveals key material.
func (s credentialSource) headerName() string {
	if s == credentialAuthorizationHeader {
		return "authorization bearer token"
	}
	return "x-api-key"
}

// credential is one candidate the caller presented, tagged with its header.
type credential struct {
	value  string
	source credentialSource
}

// presentedCredentials returns every candidate credential on a request, in the
// order they should be tried: `x-api-key` first, then `authorization: Bearer`.
//
// Both are documented credential headers, so both are read. An `authorization`
// header carrying any other scheme is skipped rather than treated as a failed
// credential: an ingress proxy may add its own `Basic` header, and that is not
// a Claude API credential.
//
// acceptAuthorization is false only when an operator has explicitly disabled
// the bearer form.
func presentedCredentials(r *http.Request, acceptAuthorization bool) []credential {
	candidates := make([]credential, 0, 2)
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		candidates = append(candidates, credential{value: key, source: credentialAPIKeyHeader})
	}
	if !acceptAuthorization {
		return candidates
	}
	if token := bearerToken(r.Header.Get("authorization")); token != "" {
		candidates = append(candidates,
			credential{value: token, source: credentialAuthorizationHeader})
	}
	return candidates
}

// bearerToken returns the token from an `Authorization: Bearer <token>` header
// value, or "" when the header is absent, empty, or carries another scheme.
// The scheme is matched case-insensitively, and at least one space or tab must
// separate it from the token, so "bearertoken" is not read as a credential.
func bearerToken(value string) string {
	const scheme = "bearer"
	value = strings.TrimSpace(value)
	if len(value) <= len(scheme) || !strings.EqualFold(value[:len(scheme)], scheme) {
		return ""
	}
	rest := value[len(scheme):]
	if strings.TrimLeft(rest, " \t") == rest {
		return ""
	}
	return strings.TrimSpace(rest)
}

// isOperationalProbe reports whether a request targets a liveness/readiness
// probe. Local choice: probes carry no session data and stay unauthenticated so
// a load balancer or orchestrator does not need a credential to schedule the
// process.
//
// HEAD is accepted alongside GET because net/http.ServeMux routes a HEAD
// request to a "GET" pattern. Without it the mux would still dispatch
// `HEAD /healthz` to the probe handler while this predicate denied it, so the
// same probe answered 200 with authentication off and 401 with it on.
func isOperationalProbe(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}
