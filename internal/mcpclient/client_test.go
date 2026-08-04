package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

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
