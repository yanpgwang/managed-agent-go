package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type staticAuthSource struct {
	token   string
	matched bool
	err     error
}

func (s staticAuthSource) ResolveMCPBearer(
	context.Context,
	string,
	string,
) (BearerCredential, bool, error) {
	return BearerCredential{Token: s.token}, s.matched, s.err
}

func TestRemoteAuthenticatedDiscoveryInjectsBearerOnEveryRequest(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "auth-test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping"}, func(
		context.Context,
		*mcp.CallToolRequest,
		struct{},
	) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	requests := 0
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("Authorization"); got != "Bearer vault-token" {
			http.Error(w, "missing credential", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, request)
	}))
	t.Cleanup(httpServer.Close)
	remote := NewRemote(httpServer.Client())
	tools, err := remote.DiscoverAuthenticated(
		t.Context(), "sesn_1", domain.MCPServer{Name: "secure", URL: httpServer.URL},
		staticAuthSource{token: "vault-token", matched: true},
	)
	if err != nil || len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("authenticated discovery = %#v, %v", tools, err)
	}
	if requests < 2 {
		t.Fatalf("authenticated requests = %d, want initialize and discovery", requests)
	}
}

func TestRemoteClassifiesUnauthorizedAsAuthenticationFailure(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(httpServer.Close)
	remote := NewRemote(httpServer.Client())
	_, err := remote.DiscoverAuthenticated(
		t.Context(), "sesn_1", domain.MCPServer{Name: "secure", URL: httpServer.URL}, nil,
	)
	if !IsAuthenticationError(err) {
		t.Fatalf("unauthorized error = %v", err)
	}
}

type trackedBody struct{ closed bool }

func (*trackedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestBearerHandlerAuthorizeClosesRejectedResponse(t *testing.T) {
	body := &trackedBody{}
	err := (&bearerHandler{server: domain.MCPServer{Name: "secure"}}).Authorize(
		t.Context(), nil, &http.Response{Status: "401 Unauthorized", Body: body},
	)
	if !body.closed || !IsAuthenticationError(err) {
		t.Fatalf("Authorize() closed=%v, err=%v", body.closed, err)
	}
}

func TestAuthenticatedRedirectGuardRejectsEveryCrossOriginVariant(t *testing.T) {
	client := cloneHTTPClientWithRedirectGuard(&http.Client{})
	previousURL, _ := url.Parse("https://mcp.example/tools")
	previous := &http.Request{URL: previousURL, Header: http.Header{"Authorization": []string{"Bearer secret"}}}
	for _, target := range []string{
		"https://sub.mcp.example/tools",
		"https://mcp.example:8443/tools",
		"http://mcp.example/tools",
		"https://other.example/tools",
	} {
		t.Run(target, func(t *testing.T) {
			targetURL, _ := url.Parse(target)
			err := client.CheckRedirect(&http.Request{URL: targetURL}, []*http.Request{previous})
			if !IsAuthenticationError(err) {
				t.Fatalf("redirect to %s error = %v", target, err)
			}
		})
	}
	sameOrigin, _ := url.Parse("https://MCP.EXAMPLE:443/other")
	if err := client.CheckRedirect(&http.Request{URL: sameOrigin}, []*http.Request{previous}); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
}

func TestAuthenticated307RedirectDoesNotReplayRequestBodyCrossOrigin(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	client := cloneHTTPClientWithRedirectGuard(source.Client())
	request, err := http.NewRequest(http.MethodPost, source.URL, strings.NewReader(`{"secret":"input"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer vault-token")
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if !IsAuthenticationError(err) {
		t.Fatalf("cross-origin 307 error = %v", err)
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target received %d replayed requests", targetRequests)
	}
}

func TestBearerHandlerWrapsResolverFailure(t *testing.T) {
	handler := &bearerHandler{
		sessionID: "sesn_1", server: domain.MCPServer{Name: "secure", URL: "https://mcp.example"},
		source: staticAuthSource{err: errors.New("keyring unavailable")},
	}
	_, err := handler.TokenSource(t.Context())
	if !IsAuthenticationError(err) || !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatalf("resolver error = %v", err)
	}
}

func TestRemoteDiscoverAndCallSupportsCurrentAndLegacyHTTPProtocols(t *testing.T) {
	type echoArgs struct {
		Message string `json:"message"`
	}
	tests := []struct {
		name        string
		stateless   bool
		wantVersion string
	}{
		{name: "current stateless protocol", stateless: true, wantVersion: "2026-07-28"},
		{name: "legacy stateful protocol", stateless: false, wantVersion: "2025-11-25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{
				Name:    "mango-mcp-conformance",
				Version: "1.0.0",
			}, nil)
			mcp.AddTool(server, &mcp.Tool{
				Name:        "echo",
				Description: "echo a message",
			}, func(
				_ context.Context,
				_ *mcp.CallToolRequest,
				args echoArgs,
			) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{
					&mcp.TextContent{Text: "hello " + args.Message},
				}}, nil, nil
			})

			handler := mcp.NewStreamableHTTPHandler(
				func(*http.Request) *mcp.Server { return server },
				&mcp.StreamableHTTPOptions{Stateless: test.stateless},
			)
			var protocolVersions sync.Map
			httpServer := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, request *http.Request) {
					if version := request.Header.Get("Mcp-Protocol-Version"); version != "" {
						protocolVersions.Store(version, struct{}{})
					}
					handler.ServeHTTP(w, request)
				},
			))
			t.Cleanup(httpServer.Close)

			remote := NewRemote(httpServer.Client())
			endpoint := domain.MCPServer{Name: "conformance", URL: httpServer.URL}
			tools, err := remote.Discover(t.Context(), endpoint)
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			if len(tools) != 1 || tools[0].Name != "echo" ||
				tools[0].Description != "echo a message" {
				t.Fatalf("Discover() tools = %#v", tools)
			}
			if tools[0].InputSchema["type"] != "object" {
				t.Fatalf("Discover() input schema = %#v", tools[0].InputSchema)
			}

			result, err := remote.Call(
				t.Context(),
				endpoint,
				"echo",
				map[string]any{"message": "mango"},
			)
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}
			if result.IsError {
				t.Fatalf("Call() returned tool error: %s", result.Raw)
			}
			var payload struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(result.Raw, &payload); err != nil {
				t.Fatalf("decode Call() result: %v", err)
			}
			if len(payload.Content) != 1 || payload.Content[0].Type != "text" ||
				payload.Content[0].Text != "hello mango" {
				t.Fatalf("Call() result = %s", result.Raw)
			}
			if _, ok := protocolVersions.Load(test.wantVersion); !ok {
				var seen []string
				protocolVersions.Range(func(key, _ any) bool {
					seen = append(seen, key.(string))
					return true
				})
				t.Fatalf("protocol versions = %v, want %q", seen, test.wantVersion)
			}
		})
	}
}

func TestDialPublicMCPRejectsLocalhostResolution(t *testing.T) {
	connection, err := dialPublicMCP(
		context.Background(),
		"tcp",
		"localhost:80",
	)
	if connection != nil {
		_ = connection.Close()
		t.Fatal("localhost connection unexpectedly succeeded")
	}
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("expected non-public egress rejection, got %v", err)
	}
}

func TestNewRemoteDefaultTransportDisablesAmbientProxy(t *testing.T) {
	remote := NewRemote(nil)
	transport, ok := remote.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", remote.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default MCP transport inherited an ambient proxy")
	}
	if transport.DialContext == nil {
		t.Fatal("default MCP transport has no egress-enforcing dialer")
	}
}
