package model

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		provider   string
		wantKind   ErrorKind
		retryable  bool
		retryAfter time.Duration
	}{
		{name: "invalid request", status: 400, provider: "invalid_request_error", wantKind: ErrorInvalidRequest},
		{name: "authentication", status: 401, provider: "authentication_error", wantKind: ErrorAuthentication},
		{name: "billing", status: 402, provider: "billing_error", wantKind: ErrorBilling},
		{name: "permission", status: 403, provider: "permission_error", wantKind: ErrorPermission},
		{name: "not found", status: 404, provider: "not_found_error", wantKind: ErrorNotFound},
		{name: "request timeout", status: 408, provider: "timeout_error", wantKind: ErrorTimeout, retryable: true},
		{name: "conflict", status: 409, provider: "conflict_error", wantKind: ErrorConflict, retryable: true},
		{name: "too large", status: 413, provider: "request_too_large", wantKind: ErrorRequestTooLarge},
		{name: "rate limit", status: 429, provider: "rate_limit_error", wantKind: ErrorRateLimit, retryable: true, retryAfter: 7 * time.Second},
		{name: "server", status: 500, provider: "api_error", wantKind: ErrorServer, retryable: true},
		{name: "gateway timeout", status: 504, provider: "timeout_error", wantKind: ErrorTimeout, retryable: true},
		{name: "overloaded", status: 529, provider: "overloaded_error", wantKind: ErrorOverloaded, retryable: true},
		{name: "future client error", status: 499, provider: "future_error", wantKind: ErrorInvalidRequest},
		{name: "future server error", status: 599, provider: "future_error", wantKind: ErrorServer, retryable: true},
		{name: "type refines status", status: 500, provider: "overloaded_error", wantKind: ErrorOverloaded, retryable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make(http.Header)
			header.Set("request-id", "req_test")
			if tt.retryAfter > 0 {
				header.Set("Retry-After", "7")
			}
			raw := []byte(`{"type":"error","error":{"type":"` + tt.provider + `","message":"provider failed"},"request_id":"req_body"}`)
			got := classifyHTTPError(tt.status, raw, header)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.Retryable() != tt.retryable {
				t.Fatalf("Retryable = %v, want %v", got.Retryable(), tt.retryable)
			}
			if got.RetryAfter != tt.retryAfter {
				t.Fatalf("RetryAfter = %v, want %v", got.RetryAfter, tt.retryAfter)
			}
			if got.RequestID != "req_test" {
				t.Fatalf("RequestID = %q, want header value", got.RequestID)
			}
			if got.Type != tt.provider || got.Message != "provider failed" {
				t.Fatalf("provider detail = %q/%q", got.Type, got.Message)
			}
		})
	}
}

func TestClassifyHTTPError_MalformedBodyIsBoundedPermanentFailure(t *testing.T) {
	raw := []byte("bad\n" + strings.Repeat("x", maxUpstreamErrorLen+100))
	got := classifyHTTPError(http.StatusBadRequest, raw, nil)
	if got.Kind != ErrorInvalidRequest || got.Retryable() {
		t.Fatalf("classification = %q retryable=%v", got.Kind, got.Retryable())
	}
	if len(got.Message) > maxUpstreamErrorLen+len("…(truncated)") {
		t.Fatalf("message len = %d, want bounded", len(got.Message))
	}
}

func TestClassifyRequestError(t *testing.T) {
	if got := classifyRequestError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", got)
	}

	timeout := classifyRequestError(context.DeadlineExceeded)
	var timeoutAPI *APIError
	if !errors.As(timeout, &timeoutAPI) || timeoutAPI.Kind != ErrorTimeout || !timeoutAPI.Retryable() {
		t.Fatalf("timeout classification = %#v", timeout)
	}

	transportCause := errors.New("connection reset")
	transport := classifyRequestError(transportCause)
	var transportAPI *APIError
	if !errors.As(transport, &transportAPI) || transportAPI.Kind != ErrorTransport ||
		!transportAPI.Retryable() || !errors.Is(transport, transportCause) {
		t.Fatalf("transport classification = %#v", transport)
	}
}
