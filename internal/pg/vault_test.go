package pg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/credentialruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/secretcrypto"
)

func TestVaultRuntimeResolutionUsesSessionOrderAndCurrentCredentialState(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewVaultRepository(store)
	now := time.Unix(1000, 0).UTC()
	for _, id := range []string{"vlt_first", "vlt_second"} {
		if _, err := repo.CreateVault(ctx, domain.Vault{
			ID: id, DisplayName: id, Metadata: map[string]string{},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{31}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewVaultService(app.VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: now},
	})
	first, err := service.CreateCredential(ctx, "vlt_first", app.CredentialCreateInput{
		Auth: app.CredentialAuthCreateInput{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://MCP.example/mcp/", Token: "first-token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCredential(ctx, "vlt_second", app.CredentialCreateInput{
		Auth: app.CredentialAuthCreateInput{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://mcp.example/mcp", Token: "second-token",
		},
	}); err != nil {
		t.Fatal(err)
	}
	session := newSession("sesn_vault_runtime")
	session.VaultIDs = []string{"vlt_first", "vlt_second"}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	resolved, matched, err := service.ResolveMCPBearer(ctx, session.ID, "https://mcp.example:443/mcp///")
	if err != nil || !matched || resolved.Token != "first-token" || resolved.VaultID != "vlt_first" {
		t.Fatalf("first resolution = %#v, matched=%v, err=%v", resolved, matched, err)
	}
	if _, err := service.ArchiveCredential(ctx, "vlt_first", first.ID); err != nil {
		t.Fatal(err)
	}
	resolved, matched, err = service.ResolveMCPBearer(ctx, session.ID, "https://mcp.example/mcp")
	if err != nil || !matched || resolved.Token != "second-token" || resolved.VaultID != "vlt_second" {
		t.Fatalf("rotated resolution = %#v, matched=%v, err=%v", resolved, matched, err)
	}
}

func TestVaultRuntimeRefreshPersistsAcrossResolutions(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewVaultRepository(store)
	now := time.Unix(1000, 0).UTC()
	if _, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_refresh", DisplayName: "Refresh", Metadata: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{33}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	expiresIn := time.Hour
	refresher := &pgOAuthRefresherFake{result: credentialruntime.OAuthRefreshResult{
		Status: credentialruntime.OAuthRefreshSucceeded, Verdict: credentialruntime.VerdictValid,
		AccessToken: "fresh-access", ExpiresIn: &expiresIn,
	}}
	service := app.NewVaultService(app.VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: now}, OAuthRefresher: refresher,
	})
	expired := now.Add(-time.Second)
	created, err := service.CreateCredential(ctx, "vlt_refresh", app.CredentialCreateInput{
		Auth: app.CredentialAuthCreateInput{
			Type: domain.CredentialAuthMCPOAuth, MCPServerURL: "https://mcp.example/mcp",
			AccessToken: "expired-access", ExpiresAt: &expired,
			Refresh: &app.OAuthRefreshCreateInput{
				ClientID: "client", RefreshToken: "refresh",
				TokenEndpoint: "https://auth.example/token", TokenEndpointAuth: "none",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := newSession("sesn_vault_refresh")
	session.VaultIDs = []string{"vlt_refresh"}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		resolved, matched, err := service.ResolveMCPBearer(
			ctx, session.ID, "https://mcp.example/mcp",
		)
		if err != nil || !matched || resolved.Token != "fresh-access" {
			t.Fatalf("resolution %d = %#v, matched=%v, err=%v", attempt, resolved, matched, err)
		}
	}
	if refresher.calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refresher.calls)
	}
	stored, err := repo.GetCredential(ctx, "vlt_refresh", created.ID)
	if err != nil || stored.Version != 2 || stored.Auth.ExpiresAt == nil ||
		!stored.Auth.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("persisted credential = %#v, %v", stored, err)
	}
}

type pgOAuthRefresherFake struct {
	result credentialruntime.OAuthRefreshResult
	calls  int
}

func (f *pgOAuthRefresherFake) Refresh(
	context.Context,
	credentialruntime.OAuthRefreshRequest,
) (credentialruntime.OAuthRefreshResult, error) {
	f.calls++
	return f.result, nil
}

func TestVaultRepositoryRejectsLegacyCanonicalURLCollision(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	repo := NewVaultRepository(store)
	now := time.Unix(1000, 0).UTC()
	if _, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_collision", DisplayName: "Collision", Metadata: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	legacy := testStoredCredential("vlt_collision", "vcrd_legacy")
	legacy.Auth.MCPServerURL = "https://mcp.example/mcp/"
	legacy.CredentialKey = legacy.Auth.MCPServerURL
	if _, err := repo.CreateCredential(ctx, legacy, 20); err != nil {
		t.Fatal(err)
	}
	keyring, err := secretcrypto.NewAESGCMKeyring("test", map[string][]byte{
		"test": bytes.Repeat([]byte{32}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewVaultService(app.VaultServiceConfig{
		Repository: repo, Cipher: keyring, IDGenerator: domain.NewSeqIDGen(),
		Clock: domain.FixedClock{T: now},
	})
	_, err = service.CreateCredential(ctx, "vlt_collision", app.CredentialCreateInput{
		Auth: app.CredentialAuthCreateInput{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://mcp.example/mcp", Token: "new",
		},
	})
	var domainErr *domain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict {
		t.Fatalf("canonical collision error = %v", err)
	}
}

func TestVaultRepositoryLifecycleAndCursorPagination(t *testing.T) {
	store := testStore(t)
	repo := NewVaultRepository(store)
	ctx := context.Background()
	firstTime := time.Unix(1000, 0).UTC()
	secondTime := time.Unix(2000, 0).UTC()

	firstVault, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_first", DisplayName: "First", Metadata: map[string]string{}, CreatedAt: firstTime, UpdatedAt: firstTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondVault, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_second", DisplayName: "Second", Metadata: map[string]string{"stage": "test"}, CreatedAt: secondTime, UpdatedAt: secondTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetVault(ctx, secondVault.ID); err != nil || got.DisplayName != "Second" {
		t.Fatalf("get vault = %#v, %v", got, err)
	}
	updatedName := "Second updated"
	owner := "platform"
	updatedVault, err := repo.UpdateVault(ctx, secondVault.ID, app.VaultUpdateInput{
		DisplayName: app.PatchValue(updatedName), Metadata: app.PatchValue(map[string]*string{"stage": nil, "owner": &owner}),
	}, domain.FixedClock{T: time.Unix(3000, 0).UTC()})
	if err != nil || updatedVault.DisplayName != updatedName || updatedVault.Metadata["owner"] != owner {
		t.Fatalf("update vault = %#v, %v", updatedVault, err)
	}
	updatedVault, err = repo.UpdateVault(ctx, secondVault.ID, app.VaultUpdateInput{
		DisplayName: app.PatchNull[string](), Metadata: app.PatchNull[map[string]*string](),
	}, domain.FixedClock{T: time.Unix(4000, 0).UTC()})
	if err != nil || updatedVault.DisplayName != updatedName || len(updatedVault.Metadata) != 0 {
		t.Fatalf("nullable update vault = %#v, %v", updatedVault, err)
	}
	firstVaultPage, err := repo.ListVaults(ctx, app.VaultListQuery{Limit: 1})
	if err != nil || !firstVaultPage.HasNext || len(firstVaultPage.Vaults) != 1 || firstVaultPage.Vaults[0].ID != secondVault.ID {
		t.Fatalf("first vault page = %#v, %v", firstVaultPage, err)
	}
	secondVaultPage, err := repo.ListVaults(ctx, app.VaultListQuery{
		Limit: 1, After: &app.ResourcePageBoundary{CreatedAt: firstVaultPage.Vaults[0].CreatedAt, ID: firstVaultPage.Vaults[0].ID},
	})
	if err != nil || secondVaultPage.HasNext || len(secondVaultPage.Vaults) != 1 || secondVaultPage.Vaults[0].ID != firstVault.ID {
		t.Fatalf("second vault page = %#v, %v", secondVaultPage, err)
	}

	firstCredential := testStoredCredential(secondVault.ID, "vcrd_first")
	firstCredential.Auth.MCPServerURL, firstCredential.CredentialKey = "https://first.example/", "https://first.example/"
	firstCredential.CreatedAt, firstCredential.UpdatedAt = firstTime, firstTime
	if _, err := repo.CreateCredential(ctx, firstCredential, 20); err != nil {
		t.Fatal(err)
	}
	secondCredential := testStoredCredential(secondVault.ID, "vcrd_second")
	secondCredential.Auth.MCPServerURL, secondCredential.CredentialKey = "https://second.example/", "https://second.example/"
	secondCredential.CreatedAt, secondCredential.UpdatedAt = secondTime, secondTime
	createdCredential, err := repo.CreateCredential(ctx, secondCredential, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetCredential(ctx, secondVault.ID, createdCredential.ID); err != nil || got.ID != createdCredential.ID {
		t.Fatalf("get credential = %#v, %v", got, err)
	}
	displayName := "Updated credential"
	updatedCredential, err := repo.UpdateCredential(ctx, secondVault.ID, createdCredential.ID, func(current domain.VaultCredential) (domain.VaultCredential, bool, error) {
		current.DisplayName = &displayName
		current.Metadata = map[string]string{"rotated": "true"}
		current.Version++
		current.UpdatedAt = time.Unix(3000, 0).UTC()
		return current, true, nil
	})
	if err != nil || updatedCredential.DisplayName == nil || *updatedCredential.DisplayName != displayName || updatedCredential.Version != 2 {
		t.Fatalf("update credential = %#v, %v", updatedCredential, err)
	}
	firstCredentialPage, err := repo.ListCredentials(ctx, secondVault.ID, app.CredentialListQuery{Limit: 1})
	if err != nil || !firstCredentialPage.HasNext || len(firstCredentialPage.Credentials) != 1 || firstCredentialPage.Credentials[0].ID != createdCredential.ID {
		t.Fatalf("first credential page = %#v, %v", firstCredentialPage, err)
	}
	secondCredentialPage, err := repo.ListCredentials(ctx, secondVault.ID, app.CredentialListQuery{
		Limit: 1, After: &app.ResourcePageBoundary{CreatedAt: firstCredentialPage.Credentials[0].CreatedAt, ID: firstCredentialPage.Credentials[0].ID},
	})
	if err != nil || secondCredentialPage.HasNext || len(secondCredentialPage.Credentials) != 1 || secondCredentialPage.Credentials[0].ID != firstCredential.ID {
		t.Fatalf("second credential page = %#v, %v", secondCredentialPage, err)
	}
	if err := repo.DeleteCredential(ctx, secondVault.ID, createdCredential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetCredential(ctx, secondVault.ID, createdCredential.ID); err == nil {
		t.Fatal("deleted credential is still readable")
	}
	if err := repo.DeleteVault(ctx, secondVault.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetVault(ctx, secondVault.ID); err == nil {
		t.Fatal("deleted vault is still readable")
	}
}

func TestVaultArchivePurgesCredentialCiphertextAndFreesActiveKey(t *testing.T) {
	store := testStore(t)
	repo := NewVaultRepository(store)
	ctx := context.Background()
	clock := domain.FixedClock{T: time.Unix(2000, 0).UTC()}

	vault, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_test", DisplayName: "Test", Metadata: map[string]string{},
		CreatedAt: time.Unix(1000, 0).UTC(), UpdatedAt: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.CreateCredential(ctx, testStoredCredential(vault.ID, "vcrd_first"), 20)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := repo.ArchiveCredential(ctx, vault.ID, first.ID, clock)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || archived.SecretEnvelope != nil {
		t.Fatalf("archived credential retained secret state: %#v", archived)
	}
	assertCredentialSecretColumnsNull(t, store, first.ID)

	second, err := repo.CreateCredential(ctx, testStoredCredential(vault.ID, "vcrd_second"), 20)
	if err != nil {
		t.Fatalf("credential key was not freed by archive: %v", err)
	}
	archivedVault, err := repo.ArchiveVault(ctx, vault.ID, domain.FixedClock{T: time.Unix(3000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if archivedVault.ArchivedAt == nil {
		t.Fatal("vault was not archived")
	}
	assertCredentialSecretColumnsNull(t, store, second.ID)
	secondAfter, err := repo.GetCredential(ctx, vault.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondAfter.ArchivedAt == nil || secondAfter.SecretEnvelope != nil {
		t.Fatalf("vault archive did not cascade credential archive: %#v", secondAfter)
	}
}

func TestVaultCredentialLimitIncludesArchivedCredentials(t *testing.T) {
	store := testStore(t)
	repo := NewVaultRepository(store)
	ctx := context.Background()
	vault, err := repo.CreateVault(ctx, domain.Vault{
		ID: "vlt_limit", DisplayName: "Limit", Metadata: map[string]string{},
		CreatedAt: time.Unix(1000, 0).UTC(), UpdatedAt: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		item := testStoredCredential(vault.ID, fmt.Sprintf("vcrd_%02d", index))
		item.Auth.MCPServerURL = fmt.Sprintf("https://mcp-%02d.example/", index)
		item.CredentialKey = item.Auth.MCPServerURL
		if _, err := repo.CreateCredential(ctx, item, 20); err != nil {
			t.Fatalf("create credential %d: %v", index, err)
		}
	}
	if _, err := repo.ArchiveCredential(ctx, vault.ID, "vcrd_00", domain.FixedClock{T: time.Unix(2000, 0)}); err != nil {
		t.Fatal(err)
	}
	extra := testStoredCredential(vault.ID, "vcrd_20")
	extra.Auth.MCPServerURL = "https://extra.example/"
	extra.CredentialKey = extra.Auth.MCPServerURL
	if _, err := repo.CreateCredential(ctx, extra, 20); err == nil {
		t.Fatal("archiving a credential incorrectly freed the 20-credential quota")
	}
}

func testStoredCredential(vaultID, id string) domain.VaultCredential {
	now := time.Unix(1000, 0).UTC()
	return domain.VaultCredential{
		ID: id, VaultID: vaultID, Metadata: map[string]string{},
		Auth: domain.CredentialAuth{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://mcp.example/",
		},
		CredentialKey: "https://mcp.example/",
		SecretEnvelope: &domain.SecretEnvelope{
			Version: 1, Algorithm: "AES-256-GCM", KeyID: "test",
			Nonce: []byte("123456789012"), Ciphertext: []byte("encrypted"),
		},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func assertCredentialSecretColumnsNull(t *testing.T, store *Store, credentialID string) {
	t.Helper()
	var nonNull int
	err := store.pool.QueryRow(context.Background(), `
SELECT num_nonnulls(secret_version, secret_algorithm, secret_key_id, secret_nonce, secret_ciphertext)
FROM vault_credentials WHERE id = $1`, credentialID).Scan(&nonNull)
	if err != nil {
		t.Fatal(err)
	}
	if nonNull != 0 {
		t.Fatalf("credential %s retains %d secret columns", credentialID, nonNull)
	}
}
