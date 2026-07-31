// Package mcpclient owns remote MCP discovery and invocation. It deliberately
// returns protocol-native JSON for tool results; the agent runtime separately
// derives model-facing and public projections.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	session, err := r.connect(ctx, server)
	if err != nil {
		return nil, err
	}
	defer session.Close()

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
	session, err := r.connect(ctx, server)
	if err != nil {
		return Result{}, err
	}
	defer session.Close()
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
	server domain.MCPServer,
) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "managed-agent-go",
		Version: "dev",
	}, nil)
	session, err := client.Connect(
		ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           r.http,
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: connect: %w", server.Name, err)
	}
	return session, nil
}
