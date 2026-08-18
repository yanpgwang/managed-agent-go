package domain

import "time"

const (
	CredentialAuthMCPOAuth     = "mcp_oauth"
	CredentialAuthStaticBearer = "static_bearer"
)

// Vault is a Workspace-scoped container for credentials. Ownership stays in
// the relational root instead of leaking into the CMA wire representation.
type Vault struct {
	ID          string
	DisplayName string
	Metadata    map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

// OAuthRefreshPublic contains only the non-secret part of an OAuth refresh
// configuration. Refresh tokens and client secrets live exclusively inside the
// encrypted credential payload.
type OAuthRefreshPublic struct {
	ClientID          string  `json:"client_id"`
	TokenEndpoint     string  `json:"token_endpoint"`
	TokenEndpointAuth string  `json:"token_endpoint_auth_type"`
	Resource          *string `json:"resource"`
	Scope             *string `json:"scope"`
}

// CredentialAuth is safe to return from the public API and persist as JSON.
// It deliberately cannot represent an access token, bearer token, refresh
// token, or OAuth client secret.
type CredentialAuth struct {
	Type         string              `json:"type"`
	MCPServerURL string              `json:"mcp_server_url"`
	ExpiresAt    *time.Time          `json:"expires_at,omitempty"`
	Refresh      *OAuthRefreshPublic `json:"refresh,omitempty"`
}

// SecretEnvelope is the versioned, authenticated encrypted form persisted in
// PostgreSQL. KeyID selects a decrypt-only or active key in the process-local
// keyring; Algorithm and Version make rotations and future format migrations
// explicit.
type SecretEnvelope struct {
	Version    int
	Algorithm  string
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// VaultCredential is the repository representation. Public callers must never
// serialize SecretEnvelope or CredentialKey.
type VaultCredential struct {
	ID             string
	VaultID        string
	DisplayName    *string
	Metadata       map[string]string
	Auth           CredentialAuth
	CredentialKey  string
	SecretEnvelope *SecretEnvelope
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}
