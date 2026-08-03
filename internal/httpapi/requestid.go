package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// contextKey is unexported so no other package can collide with, or forge, the
// values Mango stores on a request context.
type contextKey int

const requestIDContextKey contextKey = iota

// maxRequestIDLength bounds an accepted client-supplied request id. Generated
// ids are "req_" plus 24 hex characters; the bound leaves room for a caller's
// own correlation token while keeping the value small enough to log and to
// echo in a response header.
const maxRequestIDLength = 64

// RequestIDFromContext returns the request id resolved by requestIDMiddleware,
// or "" when the context did not pass through it. Handlers, services, and
// stores use it as a correlation field; it is never part of a CMA payload
// beyond the existing error envelope's request_id.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey).(string)
	return id
}

// ContextWithRequestID attaches a request id for callers that construct their
// own context (background jobs, tests).
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

// clientRequestID returns a caller-supplied request-id header when it is
// well-formed, otherwise "". Honoring an inbound id lets a client stitch its
// own traces to ours; validating it keeps an attacker-controlled string out of
// a response header and out of log records. Accepted values keep the same
// "req_" prefix the server generates and are restricted to unreserved
// characters, so no header delimiter, whitespace, or control byte can be
// reflected.
func clientRequestID(r *http.Request) string {
	id := r.Header.Get("request-id")
	if len(id) <= len(requestIDPrefix) || len(id) > maxRequestIDLength {
		return ""
	}
	if !strings.HasPrefix(id, requestIDPrefix) {
		return ""
	}
	for i := len(requestIDPrefix); i < len(id); i++ {
		switch c := id[i]; {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '-', c == '_':
		default:
			return ""
		}
	}
	return id
}
