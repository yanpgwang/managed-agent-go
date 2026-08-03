package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type Config struct {
	RequireBeta bool
	// RequireAuth turns on API key authentication. When it is true every
	// request outside the liveness/readiness probes must present a key that
	// APIKeys accepts. A true RequireAuth with an empty APIKeys fails closed:
	// nothing authenticates.
	RequireAuth        bool
	RequireVersion     bool
	RequireContentType bool

	// APIKeys is the set of accepted API keys. Keys are stored hashed and
	// compared in constant time; see auth.go.
	APIKeys *APIKeySet

	// AllowAuthorizationHeader additionally accepts `authorization: Bearer
	// <key>`. This is a non-upstream extension — the official documentation
	// only describes `x-api-key` — so it is off by default and must be enabled
	// explicitly.
	AllowAuthorizationHeader bool
}

const betaValue = "managed-agents-2026-04-01"
const anthropicVersion = "2023-06-01"

// maxBodyBytes is the documented request-size limit for Sessions, Agents, and
// Environments: 32 MiB. Exceeding it yields a 413 request_too_large.
const maxBodyBytes = 32 << 20

func betaMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		betaHeaders := strings.Join(r.Header.Values("anthropic-beta"), ",")
		if cfg.RequireBeta && !headerHasToken(betaHeaders, betaValue) {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request_error",
				"missing or invalid anthropic-beta header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware validates the presented API key against the configured key
// set and attaches the resolved Principal to the request context.
//
// Presence of a header is never sufficient: an unknown key is rejected exactly
// like a missing one. Both rejections use 401 with the `authentication_error`
// type. That status is a Mango local choice — upstream documents the
// `authentication_error` type but binds no HTTP status code to it.
func authMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.RequireAuth || isOperationalProbe(r) {
			next.ServeHTTP(w, r)
			return
		}
		presented, ok := presentedAPIKey(r, cfg.AllowAuthorizationHeader)
		if !ok {
			writeErrorEnvelope(w, http.StatusUnauthorized, "authentication_error",
				"missing x-api-key")
			return
		}
		principal, ok := cfg.APIKeys.Lookup(presented)
		if !ok {
			// Deliberately identical wording for an unknown key and a revoked
			// one, and never an echo of the presented value.
			writeErrorEnvelope(w, http.StatusUnauthorized, "authentication_error",
				"invalid x-api-key")
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
	})
}

func versionMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.RequireVersion && r.Header.Get("anthropic-version") != anthropicVersion {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request_error",
				"missing or invalid anthropic-version header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func contentTypeMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.RequireContentType || r.Body == nil || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("content-type"))
		if err != nil || mediaType != "application/json" {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request_error",
				"content-type must be application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ensureRequestID(w)
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMiddleware rejects request bodies larger than the documented 32 MiB
// limit with a 413 request_too_large. It only inspects mutating methods that
// carry a body.
func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBodyBytes {
			writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large",
				"request body exceeds 32 MiB limit")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// decodeJSONBody centralizes JSON parsing so known-length and chunked bodies
// receive the same 32 MiB behavior. It also rejects trailing JSON values and
// unknown top-level fields instead of silently accepting a wider API.
func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return domain.TooLarge("request body exceeds 32 MiB limit")
		}
		return domain.Validation("invalid JSON body")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return domain.TooLarge("request body exceeds 32 MiB limit")
		}
		return domain.Validation("request body must contain exactly one JSON value")
	}
	return nil
}

func headerHasToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.TrimSpace(token) == want {
			return true
		}
	}
	return false
}
