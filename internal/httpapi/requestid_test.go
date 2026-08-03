package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRequestIDMiddleware_PropagatesToContext proves the resolved request id
// reaches the application layer, not just the response header. Before this the
// id existed only on the way out and nothing downstream could correlate to it.
func TestRequestIDMiddleware_PropagatesToContext(t *testing.T) {
	var observed string
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/agents", nil))

	header := rec.Header().Get("request-id")
	if header == "" {
		t.Fatal("missing request-id response header")
	}
	if !strings.HasPrefix(header, "req_") {
		t.Fatalf("generated request-id = %q, want the req_ prefix", header)
	}
	if observed != header {
		t.Fatalf("context request id = %q, want response header %q", observed, header)
	}
}

// TestRequestIDMiddleware_HonorsInboundHeader proves a well-formed client id is
// adopted for both the context and the echoed header, so a caller can stitch
// its own traces to ours.
func TestRequestIDMiddleware_HonorsInboundHeader(t *testing.T) {
	const clientID = "req_client-supplied_01"
	var observed string
	h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = RequestIDFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	req.Header.Set("request-id", clientID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if observed != clientID {
		t.Fatalf("context request id = %q, want inbound %q", observed, clientID)
	}
	if got := rec.Header().Get("request-id"); got != clientID {
		t.Fatalf("response request-id = %q, want inbound %q", got, clientID)
	}
}

// TestRequestIDMiddleware_RejectsMalformedInboundHeader proves an
// attacker-controlled or off-format value is replaced by a generated id, so a
// header delimiter, control byte, or foreign format never reaches a response
// header, a log record, or a downstream correlation field.
func TestRequestIDMiddleware_RejectsMalformedInboundHeader(t *testing.T) {
	cases := map[string]string{
		"no prefix":       "abc123",
		"prefix only":     "req_",
		"space":           "req_has space",
		"header injected": "req_a\r\nx-injected: 1",
		"too long":        "req_" + strings.Repeat("a", 200),
		"wrong charset":   "req_a/b",
	}
	for name, inbound := range cases {
		t.Run(name, func(t *testing.T) {
			var observed string
			h := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed = RequestIDFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/v1/agents", nil)
			// Set the raw value directly: http.Header.Set would not carry a CRLF
			// through httptest otherwise.
			req.Header["Request-Id"] = []string{inbound}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if observed == inbound {
				t.Fatalf("malformed inbound request id %q was adopted", inbound)
			}
			if !strings.HasPrefix(observed, "req_") {
				t.Fatalf("replacement request id = %q, want the req_ prefix", observed)
			}
			if got := rec.Header().Get("request-id"); got != observed {
				t.Fatalf("response request-id = %q, want %q", got, observed)
			}
		})
	}
}

// TestRequestIDMiddleware_ErrorEnvelopeMatchesContext proves the id surfaced in
// the error envelope, the response header, and the context are one value.
func TestRequestIDMiddleware_ErrorEnvelopeMatchesContext(t *testing.T) {
	h := NewTestHandler(t)
	req := httptest.NewRequest("GET", "/v1/not-a-resource", nil)
	req.Header.Set("request-id", "req_trace0001")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_trace0001" {
		t.Fatalf("envelope request_id = %q, want req_trace0001", body.RequestID)
	}
	if got := rec.Header().Get("request-id"); got != "req_trace0001" {
		t.Fatalf("header request-id = %q, want req_trace0001", got)
	}
}

// TestLogMiddleware_LogsRequestID proves the propagated id actually reaches a
// log record, which is the whole point of putting it on the context.
func TestLogMiddleware_LogsRequestID(t *testing.T) {
	var sink bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := requestIDMiddleware(logMiddleware(logger,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
	req := httptest.NewRequest("GET", "/v1/sessions/sesn_abc/events", nil)
	req.Header.Set("request-id", "req_logged01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(sink.Bytes()), &record); err != nil {
		t.Fatalf("unmarshal log record: %v (%s)", err, sink.String())
	}
	if record["request_id"] != "req_logged01" {
		t.Fatalf("log request_id = %v, want req_logged01", record["request_id"])
	}
	if record["session_id"] != "sesn_abc" {
		t.Fatalf("log session_id = %v, want sesn_abc", record["session_id"])
	}
	if record["status"] != float64(http.StatusOK) {
		t.Fatalf("log status = %v, want 200", record["status"])
	}
}

// TestLogMiddleware_DoesNotLogHeadersOrQuery guards the secret boundary: an
// access record must never carry an api key, an authorization header, or query
// values that may hold caller-supplied tokens.
func TestLogMiddleware_DoesNotLogHeadersOrQuery(t *testing.T) {
	var sink bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := requestIDMiddleware(logMiddleware(logger,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))
	req := httptest.NewRequest("GET", "/v1/agents?token=super-secret-query", nil)
	req.Header.Set("x-api-key", "sk-super-secret-key")
	req.Header.Set("authorization", "Bearer super-secret-bearer")
	h.ServeHTTP(httptest.NewRecorder(), req)

	for _, secret := range []string{
		"super-secret-query", "sk-super-secret-key", "super-secret-bearer",
	} {
		if strings.Contains(sink.String(), secret) {
			t.Fatalf("access log leaked %q: %s", secret, sink.String())
		}
	}
}
