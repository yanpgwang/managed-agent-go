package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// Config is server configuration. The Require* flags gate Claude API wire
// header validation; the remaining fields are operational transport settings
// with no documented CMA contract.
type Config struct {
	RequireBeta        bool
	RequireAuth        bool
	RequireVersion     bool
	RequireContentType bool

	// SSEKeepAlive is the idle interval between SSE comment-frame keepalives on
	// the event stream. Zero selects defaultSSEKeepAlive; a negative value
	// disables keepalives entirely.
	SSEKeepAlive time.Duration
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

func authMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.RequireAuth && r.Header.Get("x-api-key") == "" && r.Header.Get("authorization") == "" {
			writeErrorEnvelope(w, http.StatusUnauthorized, "authentication_error",
				"missing x-api-key")
			return
		}
		next.ServeHTTP(w, r)
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

// requestIDMiddleware resolves the request id exactly once per request. The id
// is echoed in the response header (unchanged behavior), placed on the request
// context so handlers, application services, PostgreSQL, and Temporal call
// sites can correlate against it, and logged by logMiddleware.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := clientRequestID(r)
		if id != "" {
			w.Header().Set("request-id", id)
		} else {
			id = ensureRequestID(w)
		}
		next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
	})
}

// statusRecorder captures the response status for the access log without
// buffering the body, so SSE streams keep flushing through it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps the SSE handler's http.Flusher assertion satisfied through this
// wrapper. Without it the event stream would fall back to the buffered path
// and the stream endpoint would reject the request.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// logMiddleware emits one structured record per completed request. Successful
// requests log at debug so a default (info) deployment stays quiet; server
// failures log at error. Only method, path, status, duration, and correlation
// ids are recorded — never headers, bodies, or query values, which can carry
// caller credentials.
func logMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logger == nil {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Duration("duration", time.Since(started)),
			slog.String("request_id", RequestIDFromContext(r.Context())),
		}
		if sessionID := sessionIDFromPath(r.URL.Path); sessionID != "" {
			attrs = append(attrs, slog.String("session_id", sessionID))
		}
		level := slog.LevelDebug
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		logger.Log(r.Context(), level, "http request", attrs...)
	})
}

// sessionIDFromPath extracts the session id from a session-scoped route so log
// records carry the same correlation key the durable layers use. It returns ""
// for every other route.
func sessionIDFromPath(path string) string {
	rest, ok := strings.CutPrefix(path, "/v1/sessions/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if !strings.HasPrefix(id, domain.PrefixSession) {
		return ""
	}
	return id
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
