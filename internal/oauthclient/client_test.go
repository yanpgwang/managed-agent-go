package oauthclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/credentialruntime"
)

func TestRefreshUsesConfiguredClientAuthenticationAndScrubsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		clientID, clientSecret, basic := request.BasicAuth()
		if !basic || clientID != "client-id" || clientSecret != "client-secret" {
			t.Fatalf("basic auth = %q, %q, %v", clientID, clientSecret, basic)
		}
		if request.Form.Get("grant_type") != "refresh_token" ||
			request.Form.Get("refresh_token") != "old-refresh" ||
			request.Form.Get("resource") != "https://resource.example" ||
			request.Form.Get("scope") != "read write" {
			t.Fatalf("refresh form = %#v", request.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
            "access_token":"new-access",
            "refresh_token":"new-refresh",
            "expires_in":3600,
            "debug":{"client_secret":"client-secret"}
        }`))
	}))
	t.Cleanup(server.Close)

	result, err := New(server.Client()).Refresh(t.Context(), credentialruntime.OAuthRefreshRequest{
		TokenEndpoint: server.URL, ClientID: "client-id", RefreshToken: "old-refresh",
		TokenEndpointAuth: "client_secret_basic", ClientSecret: "client-secret",
		Resource: "https://resource.example", Scope: "read write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != credentialruntime.OAuthRefreshSucceeded ||
		result.Verdict != credentialruntime.VerdictValid || result.AccessToken != "new-access" ||
		result.RefreshToken == nil || *result.RefreshToken != "new-refresh" ||
		result.ExpiresIn == nil || *result.ExpiresIn != time.Hour {
		t.Fatalf("refresh result = %#v", result)
	}
	for _, secret := range []string{"new-access", "new-refresh", "old-refresh", "client-secret"} {
		if strings.Contains(result.HTTPResponse.Body, secret) {
			t.Fatalf("captured response leaked %q: %s", secret, result.HTTPResponse.Body)
		}
	}
	if !strings.Contains(result.HTTPResponse.Body, "[REDACTED]") {
		t.Fatalf("captured response was not redacted: %s", result.HTTPResponse.Body)
	}
}

func TestRefreshClassifiesOAuthFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		status      int
		wantVerdict credentialruntime.Verdict
	}{
		{name: "invalid grant", status: http.StatusBadRequest, wantVerdict: credentialruntime.VerdictInvalid},
		{name: "rate limited", status: http.StatusTooManyRequests, wantVerdict: credentialruntime.VerdictUnknown},
		{name: "server error", status: http.StatusBadGateway, wantVerdict: credentialruntime.VerdictUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			}))
			t.Cleanup(server.Close)
			result, err := New(server.Client()).Refresh(t.Context(), credentialruntime.OAuthRefreshRequest{
				TokenEndpoint: server.URL, ClientID: "client", RefreshToken: "refresh",
				TokenEndpointAuth: "none",
			})
			if err != nil || result.Status != credentialruntime.OAuthRefreshFailed ||
				result.Verdict != test.wantVerdict || result.HTTPResponse.StatusCode != test.status {
				t.Fatalf("refresh result = %#v, %v", result, err)
			}
		})
	}
}

func TestRefreshClassifiesConnectionFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	result, err := New(client).Refresh(context.Background(), credentialruntime.OAuthRefreshRequest{
		TokenEndpoint: "https://auth.example/token", ClientID: "client",
		RefreshToken: "refresh", TokenEndpointAuth: "none",
	})
	if err != nil || result.Status != credentialruntime.OAuthRefreshConnectError ||
		result.Verdict != credentialruntime.VerdictUnknown || result.HTTPResponse != nil {
		t.Fatalf("refresh result = %#v, %v", result, err)
	}
}

func TestRefreshDoesNotReplaySecretsThroughRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls++
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	result, err := New(source.Client()).Refresh(t.Context(), credentialruntime.OAuthRefreshRequest{
		TokenEndpoint: source.URL, ClientID: "client", RefreshToken: "refresh",
		TokenEndpointAuth: "client_secret_post", ClientSecret: "secret",
	})
	if err != nil || targetCalls != 0 || result.HTTPResponse == nil ||
		result.HTTPResponse.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect result = %#v, target calls=%d, err=%v", result, targetCalls, err)
	}
}

func TestRefreshDropsOversizedDiagnosticBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("x", maxOAuthResponseBody+1)))
	}))
	t.Cleanup(server.Close)
	result, err := New(server.Client()).Refresh(t.Context(), credentialruntime.OAuthRefreshRequest{
		TokenEndpoint: server.URL, ClientID: "client", RefreshToken: "refresh",
		TokenEndpointAuth: "none",
	})
	if err != nil || result.HTTPResponse == nil || !result.HTTPResponse.BodyTruncated ||
		result.HTTPResponse.Body != "" || result.Verdict != credentialruntime.VerdictUnknown {
		t.Fatalf("oversized response result = %#v, %v", result, err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
