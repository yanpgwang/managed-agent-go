package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	ensureRequestID(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders the standard Claude API error envelope:
//
//	{"type":"error","error":{"type":"invalid_request_error","message":"..."}}
//
// The official Go SDK extracts the error type from error.type; matching this
// shape lets the SDK surface typed errors against our server.
//
// The status/type pairs below reproduce the documented table on the Claude API
// errors page: 400 `invalid_request_error`, 404 `not_found_error`, 409
// `conflict_error`, 413 `request_too_large`, and 500 `api_error`. The
// documented statuses Mango never produces are 402 `billing_error` (no
// billing), 403 `permission_error` (no authorization or per-key scoping), 429
// `rate_limit_error` (no inbound rate limiting), 504 `timeout_error`, and 529
// `overloaded_error`.
//
// KindUnsupported is the one pair not in that table: 422 does not appear in the
// documented status list. It is Mango's local choice for "this documented
// capability is not implemented here", kept distinct from a malformed request.
// The error *type* stays documented — the errors page says
// `invalid_request_error` "may also be used for other 4XX status codes not
// listed in this section" — so a client that branches on the type is
// unaffected, while one that branches on the exact status sees a 4XX Mango
// invented. Recorded in docs/api/overview.md#errors.
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	typ := "api_error"
	var de *domain.DomainError
	if errors.As(err, &de) {
		switch de.Kind {
		case domain.KindValidation:
			status, typ = http.StatusBadRequest, "invalid_request_error"
		case domain.KindConflict:
			// Documented pair: the errors page defines 409 `conflict_error` for
			// a request that conflicts with a resource's current state, which is
			// exactly the optimistic-concurrency and lifecycle case here.
			status, typ = http.StatusConflict, "conflict_error"
		case domain.KindNotFound:
			status, typ = http.StatusNotFound, "not_found_error"
		case domain.KindUnsupported:
			status, typ = http.StatusUnprocessableEntity, "invalid_request_error"
		case domain.KindTooLarge:
			status, typ = http.StatusRequestEntityTooLarge, "request_too_large"
		}
	}
	writeErrorEnvelope(w, status, typ, err.Error())
}

func writeErrorEnvelope(w http.ResponseWriter, status int, typ, message string) {
	requestID := ensureRequestID(w)
	writeJSON(w, status, map[string]any{
		"type":       "error",
		"error":      map[string]any{"type": typ, "message": message},
		"request_id": requestID,
	})
}

func ensureRequestID(w http.ResponseWriter) string {
	if id := w.Header().Get("request-id"); id != "" {
		return id
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is extraordinarily rare. Keep the contract intact
		// without surfacing implementation details to the caller.
		id := "req_unavailable"
		w.Header().Set("request-id", id)
		return id
	}
	id := fmt.Sprintf("req_%x", b[:])
	w.Header().Set("request-id", id)
	return id
}
