package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/credentialruntime"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/secretcrypto"
)

func TestVaultServiceSealsCredentialAndReturnsOnlyPublicFields(t *testing.T) {
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{7}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: time.Unix(1000, 0)},
	})
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
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: time.Unix(1000, 0)},
	})
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
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: time.Unix(1000, 0)},
	})
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
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: time.Unix(1000, 0)},
	})
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
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: time.Unix(1000, 0).UTC()},
	})
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

func TestVaultRuntimeRefreshesExpiredOAuthAndPersistsRotation(t *testing.T) {
	clock := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{13}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	rotatedRefresh := "rotated-refresh"
	expiresIn := time.Hour
	refresher := &oauthRefresherFake{result: credentialruntime.OAuthRefreshResult{
		Status: credentialruntime.OAuthRefreshSucceeded, Verdict: credentialruntime.VerdictValid,
		AccessToken: "fresh-access", RefreshToken: &rotatedRefresh, ExpiresIn: &expiresIn,
	}}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(), Clock: clock,
		OAuthRefresher: refresher,
	})
	expired := time.Unix(999, 0).UTC()
	clientSecret := "client-secret"
	if _, err := service.CreateCredential(t.Context(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "expired-access", ExpiresAt: &expired,
			Refresh: &OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "original-refresh",
				TokenEndpoint:     "https://auth.example/token",
				TokenEndpointAuth: "client_secret_post", ClientSecret: &clientSecret,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resolved, matched, err := service.ResolveMCPBearer(
		t.Context(), "sesn_1", "https://mcp.example/mcp",
	)
	if err != nil || !matched || resolved.Token != "fresh-access" {
		t.Fatalf("refreshed resolution = %#v, matched=%v, err=%v", resolved, matched, err)
	}
	if refresher.calls != 1 || refresher.request.RefreshToken != "original-refresh" ||
		refresher.request.ClientSecret != clientSecret {
		t.Fatalf("refresh request = %#v, calls=%d", refresher.request, refresher.calls)
	}
	if repo.credential.Version != 2 || repo.credential.Auth.ExpiresAt == nil ||
		!repo.credential.Auth.ExpiresAt.Equal(clock.T.Add(time.Hour)) {
		t.Fatalf("persisted credential = %#v", repo.credential)
	}
	secret, err := service.openCredentialSecret(repo.credential)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.zero()
	if string(secret.AccessToken) != "fresh-access" || string(secret.RefreshToken) != rotatedRefresh {
		t.Fatalf("persisted OAuth rotation = %#v", secret)
	}
}

func TestValidateMCPOAuthCredentialRefreshesRejectedTokenAndReprobes(t *testing.T) {
	clock := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{14}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	expiresIn := time.Hour
	refresher := &oauthRefresherFake{result: credentialruntime.OAuthRefreshResult{
		Status: credentialruntime.OAuthRefreshSucceeded, Verdict: credentialruntime.VerdictValid,
		AccessToken: "fresh-access", ExpiresIn: &expiresIn,
		HTTPResponse: &credentialruntime.HTTPResponse{StatusCode: http.StatusOK},
	}}
	validator := &mcpValidatorFake{results: []credentialruntime.MCPProbeResult{
		{
			Verdict: credentialruntime.VerdictInvalid,
			Failure: &credentialruntime.MCPProbeFailure{
				Method:       "initialize",
				HTTPResponse: &credentialruntime.HTTPResponse{StatusCode: http.StatusUnauthorized},
			},
		},
		{Verdict: credentialruntime.VerdictValid},
	}}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(), Clock: clock,
		OAuthRefresher: refresher, MCPValidator: validator,
	})
	if _, err := service.CreateCredential(t.Context(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "rejected-access", Refresh: &OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "refresh",
				TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.ValidateMCPOAuthCredential(t.Context(), "vlt_1", repo.credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != credentialruntime.VerdictValid || !result.HasRefreshToken ||
		result.MCPProbe != nil || result.Refresh == nil ||
		result.Refresh.Status != credentialruntime.OAuthRefreshSucceeded {
		t.Fatalf("validation result = %#v", result)
	}
	if len(validator.tokens) != 2 || validator.tokens[0] != "rejected-access" ||
		validator.tokens[1] != "fresh-access" {
		t.Fatalf("probe tokens = %#v", validator.tokens)
	}
}

func TestValidateMCPOAuthCredentialClassifiesMissingRefresh(t *testing.T) {
	clock := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{15}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	validator := &mcpValidatorFake{results: []credentialruntime.MCPProbeResult{{
		Verdict: credentialruntime.VerdictInvalid,
		Failure: &credentialruntime.MCPProbeFailure{
			Method:       "initialize",
			HTTPResponse: &credentialruntime.HTTPResponse{StatusCode: http.StatusUnauthorized},
		},
	}}}
	repo := &vaultRepositoryFake{}
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(), Clock: clock,
		MCPValidator: validator,
	})
	if _, err := service.CreateCredential(t.Context(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "rejected-access",
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.ValidateMCPOAuthCredential(t.Context(), "vlt_1", repo.credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != credentialruntime.VerdictInvalid || result.HasRefreshToken ||
		result.MCPProbe == nil || result.Refresh == nil ||
		result.Refresh.Status != credentialruntime.OAuthRefreshUnavailable {
		t.Fatalf("validation result = %#v", result)
	}
}

func TestOAuthRefreshDoesNotOverwriteConcurrentGrantRotation(t *testing.T) {
	clock := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{16}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &vaultRepositoryFake{}
	refresher := &oauthRefresherFake{result: credentialruntime.OAuthRefreshResult{
		Status: credentialruntime.OAuthRefreshSucceeded, Verdict: credentialruntime.VerdictValid,
		AccessToken: "stale-refresh-result",
	}}
	service := NewVaultService(VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(), Clock: clock,
		OAuthRefresher: refresher,
	})
	expired := clock.T.Add(-time.Second)
	created, err := service.CreateCredential(t.Context(), "vlt_1", CredentialCreateInput{
		Auth: CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "expired-access", ExpiresAt: &expired,
			Refresh: &OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "original-refresh",
				TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	refresher.onRefresh = func() {
		_, updateErr := service.UpdateCredential(t.Context(), "vlt_1", created.ID, CredentialUpdateInput{
			Auth: &CredentialAuthUpdateInput{
				Type: domain.CredentialAuthMCPOAuth,
				Refresh: PatchValue(OAuthRefreshUpdateInput{
					RefreshToken: PatchValue("concurrent-refresh"),
				}),
			},
		})
		if updateErr != nil {
			t.Errorf("concurrent rotation: %v", updateErr)
		}
	}
	_, _, err = service.ResolveMCPBearer(t.Context(), "sesn_1", "https://mcp.example/mcp")
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict {
		t.Fatalf("resolution error = %v, want conflict", err)
	}
	secret, err := service.openCredentialSecret(repo.credential)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.zero()
	if string(secret.RefreshToken) != "concurrent-refresh" ||
		string(secret.AccessToken) == "stale-refresh-result" {
		t.Fatalf("concurrent credential was overwritten: %#v", secret)
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

type oauthRefresherFake struct {
	result    credentialruntime.OAuthRefreshResult
	request   credentialruntime.OAuthRefreshRequest
	calls     int
	onRefresh func()
}

func (f *oauthRefresherFake) Refresh(
	_ context.Context,
	request credentialruntime.OAuthRefreshRequest,
) (credentialruntime.OAuthRefreshResult, error) {
	f.calls++
	f.request = request
	if f.onRefresh != nil {
		f.onRefresh()
	}
	return f.result, nil
}

type mcpValidatorFake struct {
	results []credentialruntime.MCPProbeResult
	tokens  []string
}

func (f *mcpValidatorFake) ValidateBearer(
	_ context.Context,
	_ string,
	token string,
) (credentialruntime.MCPProbeResult, error) {
	f.tokens = append(f.tokens, token)
	if len(f.results) == 0 {
		return credentialruntime.MCPProbeResult{}, errors.New("unexpected MCP validation call")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
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
