// Package mcpclient owns remote MCP discovery and invocation. It deliberately
// returns protocol-native JSON for tool results; the agent runtime separately
// derives model-facing and public projections.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

type Tool struct {
	Name         string
	Description  string
	InputSchema  map[string]any
	OutputSchema any
}

type Result struct {
	Raw     json.RawMessage
	IsError bool
}

type Client interface {
	Discover(ctx context.Context, server domain.MCPServer) ([]Tool, error)
	Call(
		ctx context.Context,
		server domain.MCPServer,
		toolName string,
		input map[string]any,
	) (Result, error)
}

// BearerCredential is a request-scoped runtime projection. Implementations
// must never persist or log Token.
type BearerCredential struct {
	Token        string
	VaultID      string
	CredentialID string
}

// AuthSource resolves the current credential for a Session and MCP URL. It is
// called for every outgoing MCP request so rotation and archival take effect
// without restarting the Session.
type AuthSource interface {
	ResolveMCPBearer(context.Context, string, string) (BearerCredential, bool, error)
}

// AuthenticatedClient is the optional authenticated extension used by the
// Temporal runtime. Keeping it separate preserves small test/client adapters.
type AuthenticatedClient interface {
	DiscoverAuthenticated(context.Context, string, domain.MCPServer, AuthSource) ([]Tool, error)
	CallAuthenticated(context.Context, string, domain.MCPServer, string, map[string]any, AuthSource) (Result, error)
}

// AuthError identifies credential resolution and remote 401/403 failures.
type AuthError struct {
	ServerName string
	Reason     string
}

func (e *AuthError) Error() string {
	if e.ServerName == "" {
		return "MCP authentication failed: " + e.Reason
	}
	return "MCP authentication failed for " + e.ServerName + ": " + e.Reason
}

// AuthenticatedRedirectError prevents a bearer token and a replayable MCP
// request body from crossing an origin boundary.
type AuthenticatedRedirectError struct {
	From string
	To   string
}

func (e *AuthenticatedRedirectError) Error() string {
	return "refusing authenticated MCP redirect from " + e.From + " to " + e.To
}

func IsAuthenticationError(err error) bool {
	var authErr *AuthError
	var redirectErr *AuthenticatedRedirectError
	return errors.As(err, &authErr) || errors.As(err, &redirectErr)
}

type Remote struct {
	http *http.Client
}

func NewRemote(httpClient *http.Client) *Remote {
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// Tenant-configured MCP endpoints must not inherit a process-wide proxy
		// that can bypass target-IP validation. A future managed egress proxy is
		// an explicit connector capability, not an ambient environment setting.
		transport.Proxy = nil
		transport.DialContext = dialPublicMCP
		httpClient = &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		}
	}
	return &Remote{http: httpClient}
}

func dialPublicMCP(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("mcp egress: parse address: %w", err)
	}
	var addresses []net.IP
	if literal := net.ParseIP(host); literal != nil {
		addresses = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("mcp egress: resolve %s: %w", host, err)
		}
		addresses = resolved
	}

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var lastErr error
	allowed := false
	for _, candidate := range addresses {
		parsed, ok := netipAddr(candidate)
		if !ok || !domain.MCPAddressAllowed(parsed) {
			continue
		}
		allowed = true
		connection, err := dialer.DialContext(
			ctx,
			network,
			net.JoinHostPort(parsed.String(), port),
		)
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if !allowed {
		return nil, fmt.Errorf(
			"mcp egress: %s resolves only to non-public addresses",
			host,
		)
	}
	return nil, fmt.Errorf("mcp egress: dial %s: %w", host, lastErr)
}

func netipAddr(value net.IP) (address netip.Addr, ok bool) {
	address, ok = netip.AddrFromSlice(value)
	if ok {
		address = address.Unmap()
	}
	return address, ok
}

func (r *Remote) Discover(
	ctx context.Context,
	server domain.MCPServer,
) ([]Tool, error) {
	return r.DiscoverAuthenticated(ctx, "", server, nil)
}

func (r *Remote) DiscoverAuthenticated(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
	authSource AuthSource,
) ([]Tool, error) {
	session, err := r.connect(ctx, sessionID, server, authSource)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Shutdown errors do not invalidate a completed discovery response.
		_ = session.Close()
	}()

	var out []Tool
	cursor := ""
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{
			Cursor: cursor,
		})
		if err != nil {
			return nil, fmt.Errorf(
				"mcp %s: list tools: %w",
				server.Name,
				err,
			)
		}
		for _, tool := range result.Tools {
			if tool == nil || tool.Name == "" {
				continue
			}
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok {
				raw, err := json.Marshal(tool.InputSchema)
				if err != nil {
					return nil, fmt.Errorf(
						"mcp %s tool %s: encode input schema: %w",
						server.Name,
						tool.Name,
						err,
					)
				}
				if err := json.Unmarshal(raw, &schema); err != nil {
					return nil, fmt.Errorf(
						"mcp %s tool %s: input schema is not an object",
						server.Name,
						tool.Name,
					)
				}
			}
			out = append(out, Tool{
				Name:         tool.Name,
				Description:  tool.Description,
				InputSchema:  schema,
				OutputSchema: tool.OutputSchema,
			})
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (r *Remote) Call(
	ctx context.Context,
	server domain.MCPServer,
	toolName string,
	input map[string]any,
) (Result, error) {
	return r.CallAuthenticated(ctx, "", server, toolName, input, nil)
}

func (r *Remote) CallAuthenticated(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
	toolName string,
	input map[string]any,
	authSource AuthSource,
) (Result, error) {
	session, err := r.connect(ctx, sessionID, server, authSource)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		// A shutdown error after CallTool returned must not trigger a retry of a
		// possibly side-effecting operation.
		_ = session.Close()
	}()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: input,
	})
	if err != nil {
		return Result{}, fmt.Errorf(
			"mcp %s tool %s: call: %w",
			server.Name,
			toolName,
			err,
		)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Result{}, fmt.Errorf(
			"mcp %s tool %s: encode result: %w",
			server.Name,
			toolName,
			err,
		)
	}
	return Result{
		Raw:     json.RawMessage(raw),
		IsError: result.IsError,
	}, nil
}

func (r *Remote) connect(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
	authSource AuthSource,
) (*mcp.ClientSession, error) {
	httpClient := cloneHTTPClientWithRedirectGuard(r.http)
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "managed-agent-go",
		Version: "dev",
	}, nil)
	session, err := client.Connect(
		ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           httpClient,
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
			OAuthHandler: &bearerHandler{
				sessionID: sessionID,
				server:    server,
				source:    authSource,
			},
		},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: connect: %w", server.Name, err)
	}
	return session, nil
}

type bearerHandler struct {
	sessionID string
	server    domain.MCPServer
	source    AuthSource
}

func (h *bearerHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if h.source == nil {
		return nil, nil
	}
	credential, matched, err := h.source.ResolveMCPBearer(ctx, h.sessionID, h.server.URL)
	if err != nil {
		if IsAuthenticationError(err) {
			return nil, err
		}
		return nil, &AuthError{ServerName: h.server.Name, Reason: err.Error()}
	}
	if !matched {
		return nil, nil
	}
	if credential.Token == "" {
		return nil, &AuthError{ServerName: h.server.Name, Reason: "the selected credential has no usable access token"}
	}
	return oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: credential.Token,
		TokenType:   "Bearer",
	}), nil
}

func (h *bearerHandler) Authorize(
	_ context.Context,
	_ *http.Request,
	response *http.Response,
) error {
	status := "the server rejected the credential"
	if response != nil {
		status = response.Status
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}
	return &AuthError{ServerName: h.server.Name, Reason: status}
}

func cloneHTTPClientWithRedirectGuard(base *http.Client) *http.Client {
	cloned := *base
	existing := base.CheckRedirect
	cloned.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 {
			previous := via[len(via)-1]
			if previous.Header.Get("Authorization") != "" &&
				mcpOrigin(previous.URL) != mcpOrigin(request.URL) {
				return &AuthenticatedRedirectError{
					From: mcpOrigin(previous.URL),
					To:   mcpOrigin(request.URL),
				}
			}
		}
		if existing != nil {
			return existing(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &cloned
}

func mcpOrigin(value *url.URL) string {
	scheme := strings.ToLower(value.Scheme)
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
