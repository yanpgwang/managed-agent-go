package httpapi

import (
	"bytes"
	"context"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/secretcrypto"
)

func TestSDK_VaultAndStaticBearerCredentialLifecycle(t *testing.T) {
	repo := newHTTPVaultRepository()
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{11}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewVaultService(
		repo, keyring, domain.NewSeqIDGen(),
		domain.FixedClock{T: time.Unix(1000, 0).UTC()},
	)
	server := httptest.NewServer(NewServer(Deps{Vaults: service}, Config{
		RequireBeta: true, RequireAuth: true, RequireVersion: true, RequireContentType: true,
	}).Handler())
	t.Cleanup(server.Close)
	client := anthropic.NewClient(option.WithBaseURL(server.URL), option.WithAPIKey("sk-test"))
	ctx := context.Background()

	vault, err := client.Beta.Vaults.New(ctx, anthropic.BetaVaultNewParams{
		DisplayName: "Production tools", Metadata: map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("create vault: %v", err)
	}
	if vault.Type != anthropic.BetaManagedAgentsVaultTypeVault || vault.DisplayName != "Production tools" {
		t.Fatalf("vault = %s", vault.RawJSON())
	}
	gotVault, err := client.Beta.Vaults.Get(ctx, vault.ID, anthropic.BetaVaultGetParams{})
	if err != nil || gotVault.ID != vault.ID {
		t.Fatalf("get vault = %#v, %v", gotVault, err)
	}
	updatedVault, err := client.Beta.Vaults.Update(ctx, vault.ID, anthropic.BetaVaultUpdateParams{
		DisplayName: anthropic.String("Production MCP tools"),
	})
	if err != nil || updatedVault.DisplayName != "Production MCP tools" {
		t.Fatalf("update vault = %#v, %v", updatedVault, err)
	}
	updatedVault, err = client.Beta.Vaults.Update(ctx, vault.ID, anthropic.BetaVaultUpdateParams{
		DisplayName: param.Null[string](), Metadata: param.NullMap[map[string]string](),
	})
	if err != nil || updatedVault.DisplayName != "Production MCP tools" || len(updatedVault.Metadata) != 0 {
		t.Fatalf("nullable vault update = %#v, %v", updatedVault, err)
	}
	if _, err := client.Beta.Vaults.New(ctx, anthropic.BetaVaultNewParams{DisplayName: "Development tools"}); err != nil {
		t.Fatalf("create second vault: %v", err)
	}
	vaultPager := client.Beta.Vaults.ListAutoPaging(ctx, anthropic.BetaVaultListParams{Limit: anthropic.Int(1)})
	vaultCount := 0
	for vaultPager.Next() {
		vaultCount++
	}
	if err := vaultPager.Err(); err != nil || vaultCount != 2 {
		t.Fatalf("vault auto-pagination count = %d, err = %v", vaultCount, err)
	}

	credential, err := client.Beta.Vaults.Credentials.New(ctx, vault.ID, anthropic.BetaVaultCredentialNewParams{
		DisplayName: anthropic.String("Build MCP"),
		Auth: anthropic.BetaVaultCredentialNewParamsAuthUnion{
			OfStaticBearer: &anthropic.BetaManagedAgentsStaticBearerCreateParams{
				Type:         anthropic.BetaManagedAgentsStaticBearerCreateParamsTypeStaticBearer,
				MCPServerURL: "https://MCP.example:443/api", Token: "sdk-secret-token",
			},
		},
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if credential.Auth.AsStaticBearer().MCPServerURL != "https://MCP.example:443/api" {
		t.Fatalf("credential = %s", credential.RawJSON())
	}
	if strings.Contains(credential.RawJSON(), "sdk-secret-token") || strings.Contains(credential.RawJSON(), "cipher") {
		t.Fatalf("credential response leaked secret material: %s", credential.RawJSON())
	}

	got, err := client.Beta.Vaults.Credentials.Get(ctx, credential.ID, anthropic.BetaVaultCredentialGetParams{VaultID: vault.ID})
	if err != nil || got.ID != credential.ID {
		t.Fatalf("get credential = %#v, %v", got, err)
	}
	updated, err := client.Beta.Vaults.Credentials.Update(ctx, credential.ID, anthropic.BetaVaultCredentialUpdateParams{
		VaultID:     vault.ID,
		DisplayName: param.Null[string](),
		Metadata:    param.NullMap[map[string]string](),
		Auth: anthropic.BetaVaultCredentialUpdateParamsAuthUnion{
			OfStaticBearer: &anthropic.BetaManagedAgentsStaticBearerUpdateParams{
				Type:  anthropic.BetaManagedAgentsStaticBearerUpdateParamsTypeStaticBearer,
				Token: param.Null[string](),
			},
		},
	})
	if err != nil || !strings.Contains(updated.RawJSON(), `"display_name":null`) || !strings.Contains(updated.RawJSON(), `"metadata":{}`) {
		t.Fatalf("update credential = %#v, %v", updated, err)
	}
	page, err := client.Beta.Vaults.Credentials.List(ctx, vault.ID, anthropic.BetaVaultCredentialListParams{})
	if err != nil || len(page.Data) != 1 || page.Data[0].ID != credential.ID || strings.Contains(page.RawJSON(), `"has_more"`) {
		t.Fatalf("credential page = %#v, %v", page, err)
	}
	archived, err := client.Beta.Vaults.Credentials.Archive(ctx, credential.ID, anthropic.BetaVaultCredentialArchiveParams{VaultID: vault.ID})
	if err != nil || !archived.JSON.ArchivedAt.Valid() {
		t.Fatalf("archive credential = %#v, %v", archived, err)
	}
	deleted, err := client.Beta.Vaults.Credentials.Delete(ctx, credential.ID, anthropic.BetaVaultCredentialDeleteParams{VaultID: vault.ID})
	if err != nil || deleted.Type != anthropic.BetaManagedAgentsDeletedCredentialTypeVaultCredentialDeleted {
		t.Fatalf("delete credential = %#v, %v", deleted, err)
	}
	oauth, err := client.Beta.Vaults.Credentials.New(ctx, vault.ID, anthropic.BetaVaultCredentialNewParams{
		DisplayName: param.Null[string](),
		Auth: anthropic.BetaVaultCredentialNewParamsAuthUnion{
			OfMCPOAuth: &anthropic.BetaManagedAgentsMCPOAuthCreateParams{
				Type:         anthropic.BetaManagedAgentsMCPOAuthCreateParamsTypeMCPOAuth,
				MCPServerURL: "https://oauth-mcp.example/mcp", AccessToken: "oauth-access-secret",
				ExpiresAt: anthropic.Time(time.Unix(4000, 0).UTC()),
				Refresh: anthropic.BetaManagedAgentsMCPOAuthRefreshParams{
					ClientID: "client-id", RefreshToken: "oauth-refresh-secret",
					TokenEndpoint: "https://auth.example/token",
					Resource:      param.Null[string](), Scope: anthropic.String("openid"),
					TokenEndpointAuth: anthropic.BetaManagedAgentsMCPOAuthRefreshParamsTokenEndpointAuthUnion{
						OfClientSecretBasic: &anthropic.BetaManagedAgentsTokenEndpointAuthBasicParam{
							Type:         anthropic.BetaManagedAgentsTokenEndpointAuthBasicParamTypeClientSecretBasic,
							ClientSecret: "oauth-client-secret",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create OAuth credential: %v", err)
	}
	for _, secret := range []string{"oauth-access-secret", "oauth-refresh-secret", "oauth-client-secret"} {
		if strings.Contains(oauth.RawJSON(), secret) {
			t.Fatalf("OAuth response leaked %q: %s", secret, oauth.RawJSON())
		}
	}
	publicOAuth := oauth.Auth.AsMCPOAuth()
	if publicOAuth.Refresh.ClientID != "client-id" || publicOAuth.Refresh.TokenEndpoint != "https://auth.example/token" {
		t.Fatalf("OAuth public auth = %s", publicOAuth.RawJSON())
	}
	updatedOAuth, err := client.Beta.Vaults.Credentials.Update(ctx, oauth.ID, anthropic.BetaVaultCredentialUpdateParams{
		VaultID: vault.ID,
		Auth: anthropic.BetaVaultCredentialUpdateParamsAuthUnion{
			OfMCPOAuth: &anthropic.BetaManagedAgentsMCPOAuthUpdateParams{
				Type: anthropic.BetaManagedAgentsMCPOAuthUpdateParamsTypeMCPOAuth,
				Refresh: anthropic.BetaManagedAgentsMCPOAuthRefreshUpdateParams{
					RefreshToken: param.Null[string](), Scope: param.Null[string](),
					TokenEndpointAuth: anthropic.BetaManagedAgentsMCPOAuthRefreshUpdateParamsTokenEndpointAuthUnion{
						OfClientSecretBasic: &anthropic.BetaManagedAgentsTokenEndpointAuthBasicUpdateParam{
							Type:         anthropic.BetaManagedAgentsTokenEndpointAuthBasicUpdateParamTypeClientSecretBasic,
							ClientSecret: param.Null[string](),
						},
					},
				},
			},
		},
	})
	if err != nil || !strings.Contains(updatedOAuth.RawJSON(), `"scope":null`) || strings.Contains(updatedOAuth.RawJSON(), "oauth-refresh-secret") {
		t.Fatalf("nested nullable OAuth update = %#v, %v", updatedOAuth, err)
	}
	updatedOAuth, err = client.Beta.Vaults.Credentials.Update(ctx, oauth.ID, anthropic.BetaVaultCredentialUpdateParams{
		VaultID:  vault.ID,
		Metadata: param.NullMap[map[string]string](),
		Auth: anthropic.BetaVaultCredentialUpdateParamsAuthUnion{
			OfMCPOAuth: &anthropic.BetaManagedAgentsMCPOAuthUpdateParams{
				Type:        anthropic.BetaManagedAgentsMCPOAuthUpdateParamsTypeMCPOAuth,
				AccessToken: param.Null[string](), ExpiresAt: param.Null[time.Time](),
				Refresh: param.NullStruct[anthropic.BetaManagedAgentsMCPOAuthRefreshUpdateParams](),
			},
		},
	})
	if err != nil || !strings.Contains(updatedOAuth.RawJSON(), `"expires_at":null`) || !strings.Contains(updatedOAuth.RawJSON(), `"refresh":null`) {
		t.Fatalf("nullable OAuth update = %#v, %v", updatedOAuth, err)
	}
	pagerCredential, err := client.Beta.Vaults.Credentials.New(ctx, vault.ID, anthropic.BetaVaultCredentialNewParams{
		Auth: anthropic.BetaVaultCredentialNewParamsAuthUnion{
			OfStaticBearer: &anthropic.BetaManagedAgentsStaticBearerCreateParams{
				Type:         anthropic.BetaManagedAgentsStaticBearerCreateParamsTypeStaticBearer,
				MCPServerURL: "https://pager.example/mcp", Token: "pager-secret",
			},
		},
	})
	if err != nil {
		t.Fatalf("create pager credential: %v", err)
	}
	credentialPager := client.Beta.Vaults.Credentials.ListAutoPaging(ctx, vault.ID, anthropic.BetaVaultCredentialListParams{Limit: anthropic.Int(1)})
	credentialIDs := map[string]bool{}
	for credentialPager.Next() {
		credentialIDs[credentialPager.Current().ID] = true
	}
	if err := credentialPager.Err(); err != nil || !credentialIDs[oauth.ID] || !credentialIDs[pagerCredential.ID] || len(credentialIDs) != 2 {
		t.Fatalf("credential auto-pagination IDs = %#v, err = %v", credentialIDs, err)
	}
	nullableCreate, err := client.Beta.Vaults.Credentials.New(ctx, vault.ID, anthropic.BetaVaultCredentialNewParams{
		DisplayName: param.Null[string](),
		Auth: anthropic.BetaVaultCredentialNewParamsAuthUnion{
			OfMCPOAuth: &anthropic.BetaManagedAgentsMCPOAuthCreateParams{
				Type:         anthropic.BetaManagedAgentsMCPOAuthCreateParamsTypeMCPOAuth,
				MCPServerURL: "https://nullable.example/mcp", AccessToken: "nullable-access-secret",
				ExpiresAt: param.Null[time.Time](),
				Refresh:   param.NullStruct[anthropic.BetaManagedAgentsMCPOAuthRefreshParams](),
			},
		},
	})
	if err != nil || !strings.Contains(nullableCreate.RawJSON(), `"display_name":null`) || !strings.Contains(nullableCreate.RawJSON(), `"expires_at":null`) || !strings.Contains(nullableCreate.RawJSON(), `"refresh":null`) {
		t.Fatalf("nullable OAuth create = %#v, %v", nullableCreate, err)
	}
	archivedVault, err := client.Beta.Vaults.Archive(ctx, vault.ID, anthropic.BetaVaultArchiveParams{})
	if err != nil || !archivedVault.JSON.ArchivedAt.Valid() {
		t.Fatalf("archive vault = %#v, %v", archivedVault, err)
	}
	deletedVault, err := client.Beta.Vaults.Delete(ctx, vault.ID, anthropic.BetaVaultDeleteParams{})
	if err != nil || deletedVault.Type != anthropic.BetaManagedAgentsDeletedVaultTypeVaultDeleted {
		t.Fatalf("delete vault = %#v, %v", deletedVault, err)
	}
}

type httpVaultRepository struct {
	mu          sync.Mutex
	vaults      map[string]domain.Vault
	credentials map[string]domain.VaultCredential
}

func newHTTPVaultRepository() *httpVaultRepository {
	return &httpVaultRepository{vaults: map[string]domain.Vault{}, credentials: map[string]domain.VaultCredential{}}
}

func (r *httpVaultRepository) CreateVault(_ context.Context, item domain.Vault) (domain.Vault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vaults[item.ID] = item
	return item, nil
}
func (r *httpVaultRepository) GetVault(_ context.Context, id string) (domain.Vault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.vaults[id]
	if !ok {
		return domain.Vault{}, domain.NotFound("vault not found")
	}
	return item, nil
}
func (r *httpVaultRepository) UpdateVault(_ context.Context, id string, patch app.VaultUpdateInput, clock domain.Clock) (domain.Vault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.vaults[id]
	if !ok {
		return domain.Vault{}, domain.NotFound("vault not found")
	}
	if patch.DisplayName.Present && patch.DisplayName.Value != nil {
		item.DisplayName = *patch.DisplayName.Value
	}
	if patch.Metadata.Present {
		if patch.Metadata.Value == nil {
			item.Metadata = map[string]string{}
		} else {
			for key, value := range *patch.Metadata.Value {
				if value == nil {
					delete(item.Metadata, key)
				} else {
					item.Metadata[key] = *value
				}
			}
		}
	}
	item.UpdatedAt = clock.Now().UTC()
	r.vaults[id] = item
	return item, nil
}
func (r *httpVaultRepository) ListVaults(_ context.Context, query app.VaultListQuery) (app.VaultListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	page := app.VaultListPage{Vaults: make([]domain.Vault, 0, len(r.vaults))}
	for _, item := range r.vaults {
		if (query.IncludeArchived || item.ArchivedAt == nil) && resourceAfterBoundary(item.CreatedAt, item.ID, query.After) {
			page.Vaults = append(page.Vaults, item)
		}
	}
	sort.Slice(page.Vaults, func(i, j int) bool {
		return resourceNewer(page.Vaults[i].CreatedAt, page.Vaults[i].ID, page.Vaults[j].CreatedAt, page.Vaults[j].ID)
	})
	limit := query.Limit
	if limit <= 0 {
		limit = app.DefaultVaultListLimit
	}
	if len(page.Vaults) > limit {
		page.HasNext = true
		page.Vaults = page.Vaults[:limit]
	}
	return page, nil
}
func (r *httpVaultRepository) ArchiveVault(_ context.Context, id string, clock domain.Clock) (domain.Vault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.vaults[id]
	if !ok {
		return domain.Vault{}, domain.NotFound("vault not found")
	}
	now := clock.Now().UTC()
	item.ArchivedAt, item.UpdatedAt = &now, now
	r.vaults[id] = item
	for credentialID, credential := range r.credentials {
		if credential.VaultID == id && credential.ArchivedAt == nil {
			credential.ArchivedAt, credential.UpdatedAt = &now, now
			credential.SecretEnvelope = nil
			r.credentials[credentialID] = credential
		}
	}
	return item, nil
}
func (r *httpVaultRepository) DeleteVault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.vaults[id]; !ok {
		return domain.NotFound("vault not found")
	}
	delete(r.vaults, id)
	for credentialID, credential := range r.credentials {
		if credential.VaultID == id {
			delete(r.credentials, credentialID)
		}
	}
	return nil
}
func (r *httpVaultRepository) CreateCredential(_ context.Context, item domain.VaultCredential, _ int) (domain.VaultCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.vaults[item.VaultID]; !ok {
		return domain.VaultCredential{}, domain.NotFound("vault not found")
	}
	r.credentials[item.ID] = item
	return item, nil
}
func (r *httpVaultRepository) GetCredential(_ context.Context, vaultID, credentialID string) (domain.VaultCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.credentials[credentialID]
	if !ok || item.VaultID != vaultID {
		return domain.VaultCredential{}, domain.NotFound("credential not found")
	}
	return item, nil
}
func (r *httpVaultRepository) UpdateCredential(_ context.Context, vaultID, credentialID string, update func(domain.VaultCredential) (domain.VaultCredential, bool, error)) (domain.VaultCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.credentials[credentialID]
	if !ok || item.VaultID != vaultID {
		return domain.VaultCredential{}, domain.NotFound("credential not found")
	}
	next, changed, err := update(item)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	if changed {
		r.credentials[credentialID] = next
		item = next
	}
	return item, nil
}
func (r *httpVaultRepository) ListCredentials(_ context.Context, vaultID string, query app.CredentialListQuery) (app.CredentialListPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.vaults[vaultID]; !ok {
		return app.CredentialListPage{}, domain.NotFound("vault not found")
	}
	page := app.CredentialListPage{Credentials: make([]domain.VaultCredential, 0)}
	for _, item := range r.credentials {
		if item.VaultID == vaultID && (query.IncludeArchived || item.ArchivedAt == nil) && resourceAfterBoundary(item.CreatedAt, item.ID, query.After) {
			page.Credentials = append(page.Credentials, item)
		}
	}
	sort.Slice(page.Credentials, func(i, j int) bool {
		return resourceNewer(page.Credentials[i].CreatedAt, page.Credentials[i].ID, page.Credentials[j].CreatedAt, page.Credentials[j].ID)
	})
	limit := query.Limit
	if limit <= 0 {
		limit = app.DefaultCredentialListLimit
	}
	if len(page.Credentials) > limit {
		page.HasNext = true
		page.Credentials = page.Credentials[:limit]
	}
	return page, nil
}

func resourceAfterBoundary(createdAt time.Time, id string, after *app.ResourcePageBoundary) bool {
	return after == nil || createdAt.Before(after.CreatedAt) || (createdAt.Equal(after.CreatedAt) && id < after.ID)
}

func resourceNewer(leftTime time.Time, leftID string, rightTime time.Time, rightID string) bool {
	return leftTime.After(rightTime) || (leftTime.Equal(rightTime) && leftID > rightID)
}
func (r *httpVaultRepository) ArchiveCredential(_ context.Context, vaultID, credentialID string, clock domain.Clock) (domain.VaultCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.credentials[credentialID]
	if !ok || item.VaultID != vaultID {
		return domain.VaultCredential{}, domain.NotFound("credential not found")
	}
	now := clock.Now().UTC()
	item.ArchivedAt, item.UpdatedAt = &now, now
	item.SecretEnvelope = nil
	r.credentials[credentialID] = item
	return item, nil
}
func (r *httpVaultRepository) DeleteCredential(_ context.Context, vaultID, credentialID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.credentials[credentialID]
	if !ok || item.VaultID != vaultID {
		return domain.NotFound("credential not found")
	}
	delete(r.credentials, credentialID)
	return nil
}
