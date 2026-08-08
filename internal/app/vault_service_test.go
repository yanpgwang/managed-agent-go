package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/secretcrypto"
)

func TestVaultServiceSealsCredentialAndReturnsOnlyPublicFields(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{7}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(repo, keyring, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1000, 0)})
	result, err := service.CreateCredential(context.Background(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://MCP.Example:443",
			Token: "plain-secret-token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SecretEnvelope != nil || result.CredentialKey != "" || result.Version != 0 {
		t.Fatalf("public result contains repository-only fields: %#v", result)
	}
	stored := repo.credential
	if stored.SecretEnvelope == nil {
		t.Fatal("stored credential is missing encrypted payload")
	}
	if bytes.Contains(stored.SecretEnvelope.Ciphertext, []byte("plain-secret-token")) {
		t.Fatal("ciphertext contains the plaintext token")
	}
	publicJSON, err := json.Marshal(stored.Auth)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicJSON, []byte("plain-secret-token")) {
		t.Fatal("public auth contains the token")
	}
	plaintext, err := keyring.Open(*stored.SecretEnvelope, credentialAAD(stored.VaultID, stored.ID, stored.CredentialKey, stored.Auth))
	if err != nil {
		t.Fatal(err)
	}
	defer secretcrypto.Zero(plaintext)
	var secret credentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		t.Fatal(err)
	}
	defer secret.zero()
	if string(secret.Token) != "plain-secret-token" {
		t.Fatalf("decrypted token = %q", secret.Token)
	}
	if stored.Auth.MCPServerURL != "https://MCP.Example:443" || stored.CredentialKey != "https://mcp.example" {
		t.Fatalf("public URL = %q, credential key = %q", stored.Auth.MCPServerURL, stored.CredentialKey)
	}
}

func TestVaultServiceDetectsPublicAuthTampering(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{8}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(repo, keyring, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1000, 0)})
	created, err := service.CreateCredential(context.Background(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://mcp.example/", Token: "token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo.credential.Auth.MCPServerURL = "https://attacker.example/"
	_, err = service.GetCredential(context.Background(), "vlt_1", created.ID)
	if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("tampered public auth error = %v", err)
	}
	repo.credential.Auth.MCPServerURL = "https://mcp.example/"
	repo.credential.CredentialKey = "https://attacker.example/"
	_, err = service.GetCredential(context.Background(), "vlt_1", created.ID)
	if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("tampered credential key error = %v", err)
	}
}

func TestVaultServiceRejectsEnvironmentVariableWithoutSecretEgress(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{9}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(repo, keyring, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1000, 0)})
	_, err = service.CreateCredential(context.Background(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{Type: "environment_variable", MCPServerURL: "https://mcp.example/"},
	})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindUnsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
	if repo.createCredentialCalls != 0 {
		t.Fatal("unsupported credential reached repository")
	}
}

func TestVaultValidationRejectsPostgresUnsafeText(t *testing.T) {
	if err := validateVaultMetadata(map[string]string{"safe": "bad\x00value"}); err == nil {
		t.Fatal("metadata value containing NUL was accepted")
	}
	if err := validateVaultMetadata(map[string]string{"bad\x00key": "value"}); err == nil {
		t.Fatal("metadata key containing NUL was accepted")
	}
	resource := "bad\x00resource"
	scope := "bad\x00scope"
	for _, test := range []struct {
		name  string
		input OAuthRefreshCreateInput
	}{
		{
			name: "client ID",
			input: OAuthRefreshCreateInput{
				ClientID: "bad\x00client", RefreshToken: "refresh", TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none",
			},
		},
		{
			name: "resource",
			input: OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "refresh", TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none", Resource: &resource,
			},
		},
		{
			name: "scope",
			input: OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "refresh", TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none", Scope: &scope,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := prepareOAuthRefresh(test.input); err == nil {
				t.Fatal("PostgreSQL-unsafe OAuth text was accepted")
			}
		})
	}
}

func TestVaultServiceAppliesNullableCredentialUpdates(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{10}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(repo, keyring, domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1000, 0)})
	displayName := "OAuth credential"
	expiresAt := time.Unix(2000, 0).UTC()
	clientSecret := "client-secret"
	created, err := service.CreateCredential(context.Background(), "vlt_1", CredentialCreateInput{
		DisplayName: &displayName,
		Metadata:    map[string]string{"environment": "test"},
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/",
			AccessToken: "access", ExpiresAt: &expiresAt, Refresh: &OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "refresh",
				TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "client_secret_basic",
				ClientSecret: &clientSecret,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateCredential(context.Background(), "vlt_1", created.ID, CredentialUpdateInput{
		DisplayName: PatchNull[string](),
		Metadata:    PatchNull[map[string]*string](),
		Auth: &CredentialAuthUpdateInput{
			Type: domain.CredentialAuthMCPOAuth, AccessToken: PatchNull[string](),
			ExpiresAt: PatchNull[time.Time](), Refresh: PatchNull[OAuthRefreshUpdateInput](),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != nil || len(updated.Metadata) != 0 || updated.Auth.ExpiresAt != nil || updated.Auth.Refresh != nil || updated.SecretEnvelope != nil {
		t.Fatalf("updated public credential = %#v", updated)
	}
	stored := repo.credential
	plaintext, err := keyring.Open(*stored.SecretEnvelope, credentialAAD(stored.VaultID, stored.ID, stored.CredentialKey, stored.Auth))
	if err != nil {
		t.Fatal(err)
	}
	defer secretcrypto.Zero(plaintext)
	var secret credentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		t.Fatal(err)
	}
	defer secret.zero()
	if len(secret.AccessToken) != 0 || len(secret.RefreshToken) != 0 || len(secret.ClientSecret) != 0 {
		t.Fatalf("nullable update retained a secret: %#v", secret)
	}
}

func TestCanonicalMCPServerURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://EXAMPLE.com:443/mcp?tenant=1": "https://example.com/mcp?tenant=1",
		"https://example.com":                  "https://example.com",
		"https://example.com/":                 "https://example.com",
		"https://example.com/mcp///":           "https://example.com/mcp",
		"https://example.com:8443/mcp/":        "https://example.com:8443/mcp",
		"https://[2001:db8::1]:443/mcp/":       "https://[2001:db8::1]/mcp",
	} {
		canonical, err := CanonicalMCPServerURL(raw)
		if err != nil || canonical != want {
			t.Fatalf("CanonicalMCPServerURL(%q) = %q, %v; want %q", raw, canonical, err, want)
		}
	}
	for _, raw := range []string{
		"http://example.com/mcp", "https://user:pass@example.com/mcp", "https://example.com/mcp#fragment",
		"https://example.com:70000/mcp",
	} {
		if _, err := CanonicalMCPServerURL(raw); err == nil {
			t.Fatalf("CanonicalMCPServerURL(%q) succeeded", raw)
		}
	}
}

func TestVaultRuntimeRejectsExpiredOAuthWithoutRefresh(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{12}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(
		repo, keyring, domain.NewSeqIDGen(),
		domain.FixedClock{T: time.Unix(1000, 0).UTC()},
	)
	expired := time.Unix(999, 0).UTC()
	if _, err := service.CreateCredential(context.Background(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "expired-token", ExpiresAt: &expired,
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, matched, err := service.ResolveMCPBearer(
		context.Background(), "sesn_1", "https://mcp.example/mcp/",
	)
	if err == nil || matched || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired OAuth resolution matched=%v, err=%v", matched, err)
	}
}

func TestUnavailableVaultAuthFailsOnlyForMatchingCredential(t *testing.T) {
	repo := &vaultRepositoryFake{credentials: []domain.VaultCredential{{
		ID: "vcrd_1", VaultID: "vlt_1",
		Auth: domain.CredentialAuth{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://secure.example/mcp/",
		},
	}}}
	source := NewUnavailableVaultAuthSource(repo)
	if _, matched, err := source.ResolveMCPBearer(
		context.Background(), "sesn_1", "https://unrelated.example/mcp",
	); err != nil || matched {
		t.Fatalf("unrelated endpoint matched=%v, err=%v", matched, err)
	}
	if _, matched, err := source.ResolveMCPBearer(
		context.Background(), "sesn_1", "https://secure.example/mcp",
	); err == nil || matched || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("matching endpoint matched=%v, err=%v", matched, err)
	}
}

type vaultRepositoryFake struct {
	credential            domain.VaultCredential
	credentials           []domain.VaultCredential
	createCredentialCalls int
}

func (r *vaultRepositoryFake) ResolveSessionCredentials(
	context.Context,
	string,
) ([]domain.VaultCredential, error) {
	if len(r.credentials) > 0 {
		return append([]domain.VaultCredential(nil), r.credentials...), nil
	}
	if r.credential.ID == "" {
		return nil, nil
	}
	return []domain.VaultCredential{r.credential}, nil
}

func (r *vaultRepositoryFake) CreateVault(_ context.Context, item domain.Vault) (domain.Vault, error) {
	return item, nil
}
func (r *vaultRepositoryFake) GetVault(context.Context, string) (domain.Vault, error) {
	return domain.Vault{}, nil
}
func (r *vaultRepositoryFake) UpdateVault(_ context.Context, _ string, _ VaultUpdateInput, _ domain.Clock) (domain.Vault, error) {
	return domain.Vault{}, nil
}
func (r *vaultRepositoryFake) ListVaults(context.Context, VaultListQuery) (VaultListPage, error) {
	return VaultListPage{}, nil
}
func (r *vaultRepositoryFake) ArchiveVault(context.Context, string, domain.Clock) (domain.Vault, error) {
	return domain.Vault{}, nil
}
func (r *vaultRepositoryFake) DeleteVault(context.Context, string) error { return nil }
func (r *vaultRepositoryFake) CreateCredential(_ context.Context, item domain.VaultCredential, _ int) (domain.VaultCredential, error) {
	r.createCredentialCalls++
	r.credential = item
	return item, nil
}
func (r *vaultRepositoryFake) GetCredential(_ context.Context, _, _ string) (domain.VaultCredential, error) {
	return r.credential, nil
}
func (r *vaultRepositoryFake) UpdateCredential(_ context.Context, _, _ string, update func(domain.VaultCredential) (domain.VaultCredential, bool, error)) (domain.VaultCredential, error) {
	next, changed, err := update(r.credential)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	if changed {
		r.credential = next
	}
	return r.credential, nil
}
func (r *vaultRepositoryFake) ListCredentials(context.Context, string, CredentialListQuery) (CredentialListPage, error) {
	return CredentialListPage{Credentials: []domain.VaultCredential{r.credential}}, nil
}
func (r *vaultRepositoryFake) ArchiveCredential(context.Context, string, string, domain.Clock) (domain.VaultCredential, error) {
	return r.credential, nil
}
func (r *vaultRepositoryFake) DeleteCredential(context.Context, string, string) error { return nil }
