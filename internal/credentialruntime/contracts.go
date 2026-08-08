// Package credentialruntime defines transport-neutral contracts shared by the
// Vault application service and the outbound OAuth and MCP adapters.
package credentialruntime

import (
	"context"
	"time"
)

// BearerCredential is a request-scoped runtime projection. Implementations
// must never persist or log Token.
type BearerCredential struct {
	Token        string
	VaultID      string
	CredentialID string
}

// AuthSource resolves the current credential for a Session and MCP URL. It is
// called for every outgoing MCP request so rotation, archival, and refresh take
// effect without restarting the Session.
type AuthSource interface {
	ResolveMCPBearer(context.Context, string, string) (BearerCredential, bool, error)
}

type Verdict string

const (
	VerdictValid   Verdict = "valid"
	VerdictInvalid Verdict = "invalid"
	VerdictUnknown Verdict = "unknown"
)

// HTTPResponse is the bounded, scrubbed response projection exposed by OAuth
// validation. Adapters must remove credential material before populating Body.
type HTTPResponse struct {
	Body          string `json:"body"`
	BodyTruncated bool   `json:"body_truncated"`
	ContentType   string `json:"content_type"`
	StatusCode    int    `json:"status_code"`
}

type MCPProbeFailure struct {
	Method       string        `json:"method"`
	HTTPResponse *HTTPResponse `json:"http_response"`
}

type MCPProbeResult struct {
	Verdict Verdict
	Failure *MCPProbeFailure
}

// MCPValidator live-probes initialize and tools/list with a supplied bearer.
type MCPValidator interface {
	ValidateBearer(context.Context, string, string) (MCPProbeResult, error)
}

type OAuthRefreshStatus string

const (
	OAuthRefreshSucceeded    OAuthRefreshStatus = "succeeded"
	OAuthRefreshFailed       OAuthRefreshStatus = "failed"
	OAuthRefreshConnectError OAuthRefreshStatus = "connect_error"
	OAuthRefreshUnavailable  OAuthRefreshStatus = "no_refresh_token"
)

type OAuthRefreshRequest struct {
	TokenEndpoint     string
	ClientID          string
	RefreshToken      string
	TokenEndpointAuth string
	ClientSecret      string
	Resource          string
	Scope             string
}

type OAuthRefreshResult struct {
	Status       OAuthRefreshStatus
	Verdict      Verdict
	HTTPResponse *HTTPResponse
	AccessToken  string
	RefreshToken *string
	ExpiresIn    *time.Duration
}

type OAuthRefresher interface {
	Refresh(context.Context, OAuthRefreshRequest) (OAuthRefreshResult, error)
}
