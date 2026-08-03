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

// Authentication in Mango is a local choice, not a reproduction of a documented
// contract. The official Claude Managed Agents documentation describes the
// `x-api-key` request header but binds no HTTP status code to an authentication
// failure and draws no missing-versus-invalid distinction. Mango therefore
// answers an unauthenticated request with 401 and the documented
// `authentication_error` error type, and says so in docs/api/overview.md.
//
// `authorization: Bearer ...` is NOT documented upstream as an alternative to
// `x-api-key` (every upstream "Bearer" reference belongs to the unrelated
// vault-credential feature), so it is off by default and available only as an
// explicitly opt-in, clearly labelled non-upstream extension.

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

// presentedAPIKey extracts the candidate key from a request.
//
// `x-api-key` is the only documented header upstream and is always accepted.
// `authorization: Bearer <key>` is a non-upstream convenience extension and is
// read only when the operator opts in.
func presentedAPIKey(r *http.Request, allowAuthorizationHeader bool) (string, bool) {
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key, true
	}
	if !allowAuthorizationHeader {
		return "", false
	}
	const bearer = "bearer "
	value := strings.TrimSpace(r.Header.Get("authorization"))
	if len(value) <= len(bearer) || !strings.EqualFold(value[:len(bearer)], bearer) {
		return "", false
	}
	if key := strings.TrimSpace(value[len(bearer):]); key != "" {
		return key, true
	}
	return "", false
}

// isOperationalProbe reports whether a request targets a liveness/readiness
// probe. Local choice: probes carry no session data and stay unauthenticated so
// a load balancer or orchestrator does not need a credential to schedule the
// process.
func isOperationalProbe(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}
