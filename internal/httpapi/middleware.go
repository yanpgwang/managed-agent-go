package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type Config struct {
	RequireBeta        bool
	RequireAuth        bool
	RequireVersion     bool
	RequireContentType bool
}

const betaValue = "managed-agents-2026-04-01"
const filesBetaValue = "files-api-2025-04-14"
const skillsBetaValue = "skills-2025-10-02"
const anthropicVersion = "2023-06-01"

// maxBodyBytes is the documented request-size limit for Sessions, Agents, and
// Environments: 32 MiB. Exceeding it yields a 413 request_too_large.
const maxBodyBytes = 32 << 20
const maxFileRequestBytes = app.MaxFileBytes + (1 << 20)
const maxSkillRequestBytes = app.MaxSkillUploadBytes + (1 << 20)

func betaMiddleware(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		betaHeaders := strings.Join(r.Header.Values("anthropic-beta"), ",")
		requiredBeta := betaValue
		if isFilePath(r.URL.Path) {
			requiredBeta = filesBetaValue
		} else if isSkillPath(r.URL.Path) {
			requiredBeta = skillsBetaValue
		}
		if cfg.RequireBeta && !headerHasToken(betaHeaders, requiredBeta) {
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
		requiredType := "application/json"
		if isMultipartUpload(r) {
			requiredType = "multipart/form-data"
		}
		if err != nil || mediaType != requiredType {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request_error",
				"content-type must be "+requiredType)
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
		limit := int64(maxBodyBytes)
		message := "request body exceeds 32 MiB limit"
		if isFileUpload(r) {
			limit = maxFileRequestBytes
			message = "file request exceeds 500 MB limit"
		} else if isSkillUpload(r) {
			limit = maxSkillRequestBytes
			message = "Skill upload must be smaller than 30 MB"
		}
		if r.ContentLength > limit {
			writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large",
				message)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func isFilePath(path string) bool {
	return path == "/v1/files" || strings.HasPrefix(path, "/v1/files/")
}

func isFileUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/files"
}

func isSkillPath(path string) bool {
	return path == "/v1/skills" || strings.HasPrefix(path, "/v1/skills/")
}

func isSkillUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && isSkillPath(r.URL.Path) &&
		(r.URL.Path == "/v1/skills" || strings.HasSuffix(r.URL.Path, "/versions"))
}

func isMultipartUpload(r *http.Request) bool {
	return isFileUpload(r) || isSkillUpload(r)
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
