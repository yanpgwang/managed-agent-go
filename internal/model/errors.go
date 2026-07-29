package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorKind classifies failures at the model-provider boundary. The values are
// provider-neutral control-plane categories: provider-specific wire error types
// remain available on APIError.Type for diagnostics.
type ErrorKind string

const (
	ErrorUnknown         ErrorKind = "unknown"
	ErrorInvalidRequest  ErrorKind = "invalid_request"
	ErrorAuthentication  ErrorKind = "authentication"
	ErrorBilling         ErrorKind = "billing"
	ErrorPermission      ErrorKind = "permission"
	ErrorNotFound        ErrorKind = "not_found"
	ErrorConflict        ErrorKind = "conflict"
	ErrorRequestTooLarge ErrorKind = "request_too_large"
	ErrorRateLimit       ErrorKind = "rate_limit"
	ErrorServer          ErrorKind = "server"
	ErrorTimeout         ErrorKind = "timeout"
	ErrorOverloaded      ErrorKind = "overloaded"
	ErrorTransport       ErrorKind = "transport"
)

// APIError is a typed, sanitized failure returned by a model provider. Message
// is bounded and contains response-body text only; RequestID is safe provider
// correlation metadata. Cause is set for transport failures so errors.Is/As
// continue to work without exposing request headers or credentials.
type APIError struct {
	Kind       ErrorKind
	StatusCode int
	Type       string
	Message    string
	RequestID  string
	RetryAfter time.Duration
	Cause      error
}

func (e *APIError) Error() string {
	if e == nil {
		return "model: unknown API error"
	}
	if e.StatusCode == 0 {
		if e.Message == "" {
			return "model: request failed"
		}
		return sanitizeErrorText("model: request failed: " + e.Message)
	}

	detail := e.Message
	if e.Type != "" {
		if detail == "" {
			detail = e.Type
		} else {
			detail = e.Type + ": " + detail
		}
	}
	if detail == "" {
		detail = "(empty body)"
	}
	message := fmt.Sprintf("model: unexpected status %d: %s", e.StatusCode, detail)
	if e.RequestID != "" {
		message += " (request_id: " + e.RequestID + ")"
	}
	return sanitizeErrorText(message)
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Retryable reports whether repeating the same logical model request can
// reasonably succeed without changing caller input or operator configuration.
// Unknown transport/server failures retain the previous retry-by-default
// behavior; known permanent 4xx failures terminate honestly instead of leaving
// a Temporal Activity retrying forever.
func (e *APIError) Retryable() bool {
	if e == nil {
		return true
	}
	switch e.Kind {
	case ErrorConflict, ErrorRateLimit, ErrorServer, ErrorTimeout, ErrorOverloaded, ErrorTransport:
		return true
	case ErrorInvalidRequest, ErrorAuthentication, ErrorBilling, ErrorPermission,
		ErrorNotFound, ErrorRequestTooLarge:
		return false
	}
	if e.StatusCode >= 500 || e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode == http.StatusConflict || e.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if e.StatusCode >= 400 && e.StatusCode < 500 {
		return false
	}
	return true
}

type apiErrorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

func classifyHTTPError(status int, raw []byte, header http.Header) *APIError {
	var envelope apiErrorEnvelope
	_ = json.Unmarshal(raw, &envelope)

	providerType := strings.TrimSpace(envelope.Error.Type)
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if message == "" {
		message = "(empty body)"
	}

	requestID := strings.TrimSpace(header.Get("request-id"))
	if requestID == "" {
		requestID = strings.TrimSpace(envelope.RequestID)
	}

	return &APIError{
		Kind:       classifyHTTPErrorKind(status, providerType),
		StatusCode: status,
		Type:       sanitizeErrorText(providerType),
		Message:    sanitizeErrorText(message),
		RequestID:  sanitizeErrorText(requestID),
		RetryAfter: parseRetryAfter(header.Get("Retry-After")),
	}
}

func classifyHTTPErrorKind(status int, providerType string) ErrorKind {
	// Retry semantics are primarily status-defined. Protect those statuses from
	// an absent, proxy-generated, or future provider type before refining the
	// remaining statuses from the error envelope.
	switch status {
	case http.StatusRequestTimeout:
		return ErrorTimeout
	case http.StatusConflict:
		return ErrorConflict
	case http.StatusRequestEntityTooLarge:
		return ErrorRequestTooLarge
	case http.StatusTooManyRequests:
		return ErrorRateLimit
	case http.StatusGatewayTimeout:
		return ErrorTimeout
	case 529:
		return ErrorOverloaded
	}
	if status >= 500 {
		switch providerType {
		case "timeout_error":
			return ErrorTimeout
		case "overloaded_error":
			return ErrorOverloaded
		default:
			return ErrorServer
		}
	}

	switch providerType {
	case "invalid_request_error":
		return ErrorInvalidRequest
	case "authentication_error":
		return ErrorAuthentication
	case "billing_error":
		return ErrorBilling
	case "permission_error":
		return ErrorPermission
	case "not_found_error":
		return ErrorNotFound
	case "conflict_error":
		return ErrorConflict
	case "request_too_large":
		return ErrorRequestTooLarge
	case "rate_limit_error":
		return ErrorRateLimit
	case "api_error":
		return ErrorServer
	case "timeout_error":
		return ErrorTimeout
	case "overloaded_error":
		return ErrorOverloaded
	}

	switch status {
	case http.StatusBadRequest:
		return ErrorInvalidRequest
	case http.StatusUnauthorized:
		return ErrorAuthentication
	case http.StatusPaymentRequired:
		return ErrorBilling
	case http.StatusForbidden:
		return ErrorPermission
	case http.StatusNotFound:
		return ErrorNotFound
	}
	if status >= 400 {
		// The provider may add new 4xx types. Unknown client errors are still
		// permanent for an unchanged request, even when their type is unfamiliar.
		return ErrorInvalidRequest
	}
	return ErrorUnknown
}

func classifyRequestError(err error) error {
	if err == nil {
		return nil
	}
	// Preserve cancellation identity for the current and future interrupt paths.
	// Turning it into an ordinary retryable application error would allow a
	// deliberately cancelled model call to be scheduled again.
	if errors.Is(err, context.Canceled) {
		return err
	}

	kind := ErrorTransport
	if errors.Is(err, context.DeadlineExceeded) {
		kind = ErrorTimeout
	} else {
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			kind = ErrorTimeout
		}
	}
	return &APIError{
		Kind:    kind,
		Message: sanitizeErrorText(err.Error()),
		Cause:   err,
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay > 0 {
			return delay
		}
	}
	return 0
}
