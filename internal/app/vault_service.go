package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/secretcrypto"
)

const (
	DefaultVaultListLimit      = 20
	MaxVaultListLimit          = 100
	DefaultCredentialListLimit = 20
	MaxCredentialListLimit     = 100
	MaxCredentialsPerVault     = 20
)

type VaultCreateInput struct {
	DisplayName string
	Metadata    map[string]string
}

type VaultUpdateInput struct {
	DisplayName NullablePatch[string]
	Metadata    NullablePatch[map[string]*string]
}

type VaultListQuery struct {
	IncludeArchived bool
	After           *ResourcePageBoundary
	Limit           int
}

type VaultListPage struct {
	Vaults  []domain.Vault
	HasNext bool
}

type OAuthRefreshCreateInput struct {
	ClientID          string
	RefreshToken      string
	TokenEndpoint     string
	TokenEndpointAuth string
	ClientSecret      *string
	Resource          *string
	Scope             *string
}

type CredentialAuthCreateInput struct {
	Type         string
	MCPServerURL string
	Token        string
	AccessToken  string
	ExpiresAt    *time.Time
	Refresh      *OAuthRefreshCreateInput
}

type CredentialCreateInput struct {
	DisplayName *string
	Metadata    map[string]string
	Auth        CredentialAuthCreateInput
}

// NullablePatch preserves the three states used by nullable update fields:
// omitted (Present=false), explicit JSON null (Present=true, Value=nil), and a
// concrete value. A pointer alone cannot distinguish omission from null.
type NullablePatch[T any] struct {
	Present bool
	Value   *T
}

func PatchValue[T any](value T) NullablePatch[T] {
	return NullablePatch[T]{Present: true, Value: &value}
}

func PatchNull[T any]() NullablePatch[T] {
	return NullablePatch[T]{Present: true}
}

type OAuthTokenEndpointAuthUpdateInput struct {
	Type         string
	ClientSecret NullablePatch[string]
}

type OAuthRefreshUpdateInput struct {
	RefreshToken      NullablePatch[string]
	Scope             NullablePatch[string]
	TokenEndpointAuth *OAuthTokenEndpointAuthUpdateInput
}

type CredentialAuthUpdateInput struct {
	Type        string
	Token       NullablePatch[string]
	AccessToken NullablePatch[string]
	ExpiresAt   NullablePatch[time.Time]
	Refresh     NullablePatch[OAuthRefreshUpdateInput]
}

type CredentialUpdateInput struct {
	DisplayName NullablePatch[string]
	Metadata    NullablePatch[map[string]*string]
	Auth        *CredentialAuthUpdateInput
}

type CredentialListQuery struct {
	IncludeArchived bool
	After           *ResourcePageBoundary
	Limit           int
}

type CredentialListPage struct {
	Credentials []domain.VaultCredential
	HasNext     bool
}

type VaultRepository interface {
	CreateVault(context.Context, domain.Vault) (domain.Vault, error)
	GetVault(context.Context, string) (domain.Vault, error)
	UpdateVault(context.Context, string, VaultUpdateInput, domain.Clock) (domain.Vault, error)
	ListVaults(context.Context, VaultListQuery) (VaultListPage, error)
	ArchiveVault(context.Context, string, domain.Clock) (domain.Vault, error)
	DeleteVault(context.Context, string) error

	CreateCredential(context.Context, domain.VaultCredential, int) (domain.VaultCredential, error)
	GetCredential(context.Context, string, string) (domain.VaultCredential, error)
	UpdateCredential(context.Context, string, string, func(domain.VaultCredential) (domain.VaultCredential, bool, error)) (domain.VaultCredential, error)
	ListCredentials(context.Context, string, CredentialListQuery) (CredentialListPage, error)
	ArchiveCredential(context.Context, string, string, domain.Clock) (domain.VaultCredential, error)
	DeleteCredential(context.Context, string, string) error
}

type VaultService struct {
	repo   VaultRepository
	cipher secretcrypto.Cipher
	ids    domain.IDGenerator
	clock  domain.Clock
}

func NewVaultService(repo VaultRepository, cipher secretcrypto.Cipher, ids domain.IDGenerator, clock domain.Clock) *VaultService {
	return &VaultService{repo: repo, cipher: cipher, ids: ids, clock: clock}
}

func (s *VaultService) CreateVault(ctx context.Context, input VaultCreateInput) (domain.Vault, error) {
	if err := validateVaultDisplayName(input.DisplayName); err != nil {
		return domain.Vault{}, err
	}
	if err := validateVaultMetadata(input.Metadata); err != nil {
		return domain.Vault{}, err
	}
	now := s.clock.Now().UTC()
	return s.repo.CreateVault(ctx, domain.Vault{
		ID: s.ids.NewID(domain.PrefixVault), DisplayName: input.DisplayName,
		Metadata: cloneStringMap(input.Metadata), CreatedAt: now, UpdatedAt: now,
	})
}

func (s *VaultService) GetVault(ctx context.Context, id string) (domain.Vault, error) {
	return s.repo.GetVault(ctx, id)
}

func (s *VaultService) UpdateVault(ctx context.Context, id string, input VaultUpdateInput) (domain.Vault, error) {
	if input.DisplayName.Present && input.DisplayName.Value != nil {
		if err := validateVaultDisplayName(*input.DisplayName.Value); err != nil {
			return domain.Vault{}, err
		}
	}
	if input.Metadata.Present && input.Metadata.Value != nil {
		if err := validateVaultMetadataPatch(*input.Metadata.Value); err != nil {
			return domain.Vault{}, err
		}
	}
	return s.repo.UpdateVault(ctx, id, input, s.clock)
}

func (s *VaultService) ListVaults(ctx context.Context, query VaultListQuery) (VaultListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultVaultListLimit
	}
	if query.Limit < 1 || query.Limit > MaxVaultListLimit {
		return VaultListPage{}, domain.Validation("limit must be between 1 and 100")
	}
	return s.repo.ListVaults(ctx, query)
}

func (s *VaultService) ArchiveVault(ctx context.Context, id string) (domain.Vault, error) {
	return s.repo.ArchiveVault(ctx, id, s.clock)
}

func (s *VaultService) DeleteVault(ctx context.Context, id string) error {
	return s.repo.DeleteVault(ctx, id)
}

func (s *VaultService) CreateCredential(ctx context.Context, vaultID string, input CredentialCreateInput) (domain.VaultCredential, error) {
	if err := validateOptionalCredentialDisplayName(input.DisplayName); err != nil {
		return domain.VaultCredential{}, err
	}
	if err := validateVaultMetadata(input.Metadata); err != nil {
		return domain.VaultCredential{}, err
	}
	credentialID := s.ids.NewID(domain.PrefixVaultCredential)
	public, secret, key, err := prepareCredentialCreate(input.Auth)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	defer secret.zero()
	envelope, err := sealCredentialSecret(s.cipher, vaultID, credentialID, key, public, secret)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	now := s.clock.Now().UTC()
	created, err := s.repo.CreateCredential(ctx, domain.VaultCredential{
		ID: credentialID, VaultID: vaultID, DisplayName: cloneStringPointer(input.DisplayName),
		Metadata: cloneStringMap(input.Metadata), Auth: public, CredentialKey: key,
		SecretEnvelope: &envelope, Version: 1, CreatedAt: now, UpdatedAt: now,
	}, MaxCredentialsPerVault)
	return publicCredential(created), err
}

func (s *VaultService) GetCredential(ctx context.Context, vaultID, credentialID string) (domain.VaultCredential, error) {
	item, err := s.repo.GetCredential(ctx, vaultID, credentialID)
	if err != nil {
		return domain.VaultCredential{}, err
	}
	if err := s.verifyCredentialEnvelope(item); err != nil {
		return domain.VaultCredential{}, err
	}
	return publicCredential(item), nil
}

func (s *VaultService) UpdateCredential(ctx context.Context, vaultID, credentialID string, input CredentialUpdateInput) (domain.VaultCredential, error) {
	if input.DisplayName.Present && input.DisplayName.Value != nil {
		if err := validateVaultDisplayName(*input.DisplayName.Value); err != nil {
			return domain.VaultCredential{}, err
		}
	}
	if input.Metadata.Present && input.Metadata.Value != nil {
		if err := validateVaultMetadataPatch(*input.Metadata.Value); err != nil {
			return domain.VaultCredential{}, err
		}
	}
	updated, err := s.repo.UpdateCredential(ctx, vaultID, credentialID, func(current domain.VaultCredential) (domain.VaultCredential, bool, error) {
		if current.ArchivedAt != nil || current.SecretEnvelope == nil {
			return domain.VaultCredential{}, false, domain.Validation("archived credentials are read-only")
		}
		plaintext, err := s.cipher.Open(*current.SecretEnvelope, credentialAAD(vaultID, credentialID, current.CredentialKey, current.Auth))
		if err != nil {
			return domain.VaultCredential{}, false, errors.New("open credential secret: encrypted payload is unavailable")
		}
		defer secretcrypto.Zero(plaintext)
		next := current
		next.DisplayName = cloneStringPointer(current.DisplayName)
		next.Metadata = cloneStringMap(current.Metadata)
		next.Auth = cloneCredentialAuth(current.Auth)
		if input.DisplayName.Present {
			next.DisplayName = cloneStringPointer(input.DisplayName.Value)
		}
		if input.Metadata.Present {
			if input.Metadata.Value == nil {
				next.Metadata = map[string]string{}
			} else {
				for key, value := range *input.Metadata.Value {
					if value == nil {
						delete(next.Metadata, key)
					} else {
						next.Metadata[key] = *value
					}
				}
			}
		}
		if len(next.Metadata) > 16 {
			return domain.VaultCredential{}, false, domain.Validation("metadata must contain at most 16 entries")
		}
		secretChanged := false
		if input.Auth != nil {
			if input.Auth.Type != current.Auth.Type {
				return domain.VaultCredential{}, false, domain.Validation("credential auth type is immutable")
			}
			var secret credentialSecret
			if err := json.Unmarshal(plaintext, &secret); err != nil {
				return domain.VaultCredential{}, false, errors.New("open credential secret: invalid encrypted payload")
			}
			defer secret.zero()
			secretChanged, err = applyCredentialAuthUpdate(&next.Auth, &secret, *input.Auth)
			if err != nil {
				return domain.VaultCredential{}, false, err
			}
			if secretChanged {
				envelope, err := sealCredentialSecret(s.cipher, vaultID, credentialID, next.CredentialKey, next.Auth, secret)
				if err != nil {
					return domain.VaultCredential{}, false, err
				}
				next.SecretEnvelope = &envelope
			}
		}
		changed := secretChanged || !equalOptionalString(next.DisplayName, current.DisplayName) || !equalVaultStringMap(next.Metadata, current.Metadata) || !equalCredentialAuth(next.Auth, current.Auth)
		if changed {
			next.Version = current.Version + 1
			next.UpdatedAt = s.clock.Now().UTC()
		}
		return next, changed, nil
	})
	return publicCredential(updated), err
}

func (s *VaultService) ListCredentials(ctx context.Context, vaultID string, query CredentialListQuery) (CredentialListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultCredentialListLimit
	}
	if query.Limit < 1 || query.Limit > MaxCredentialListLimit {
		return CredentialListPage{}, domain.Validation("limit must be between 1 and 100")
	}
	page, err := s.repo.ListCredentials(ctx, vaultID, query)
	if err != nil {
		return CredentialListPage{}, err
	}
	for index := range page.Credentials {
		if err := s.verifyCredentialEnvelope(page.Credentials[index]); err != nil {
			return CredentialListPage{}, err
		}
		page.Credentials[index] = publicCredential(page.Credentials[index])
	}
	return page, nil
}

func (s *VaultService) ArchiveCredential(ctx context.Context, vaultID, credentialID string) (domain.VaultCredential, error) {
	item, err := s.repo.ArchiveCredential(ctx, vaultID, credentialID, s.clock)
	return publicCredential(item), err
}

func (s *VaultService) DeleteCredential(ctx context.Context, vaultID, credentialID string) error {
	return s.repo.DeleteCredential(ctx, vaultID, credentialID)
}

type credentialSecret struct {
	Token        []byte `json:"token,omitempty"`
	AccessToken  []byte `json:"access_token,omitempty"`
	RefreshToken []byte `json:"refresh_token,omitempty"`
	ClientSecret []byte `json:"client_secret,omitempty"`
}

func (s *credentialSecret) zero() {
	secretcrypto.Zero(s.Token)
	secretcrypto.Zero(s.AccessToken)
	secretcrypto.Zero(s.RefreshToken)
	secretcrypto.Zero(s.ClientSecret)
}

func prepareCredentialCreate(input CredentialAuthCreateInput) (domain.CredentialAuth, credentialSecret, string, error) {
	publicURL, canonical, err := validatedAndCanonicalHTTPSURL(input.MCPServerURL, "auth.mcp_server_url")
	if err != nil {
		return domain.CredentialAuth{}, credentialSecret{}, "", err
	}
	public := domain.CredentialAuth{Type: input.Type, MCPServerURL: publicURL}
	switch input.Type {
	case domain.CredentialAuthStaticBearer:
		if input.Token == "" {
			return domain.CredentialAuth{}, credentialSecret{}, "", domain.Validation("auth.token is required")
		}
		return public, credentialSecret{Token: []byte(input.Token)}, canonical, nil
	case domain.CredentialAuthMCPOAuth:
		if input.AccessToken == "" {
			return domain.CredentialAuth{}, credentialSecret{}, "", domain.Validation("auth.access_token is required")
		}
		public.ExpiresAt = cloneTimePointer(input.ExpiresAt)
		secret := credentialSecret{AccessToken: []byte(input.AccessToken)}
		if input.Refresh != nil {
			refreshPublic, refreshSecret, err := prepareOAuthRefresh(*input.Refresh)
			if err != nil {
				secret.zero()
				return domain.CredentialAuth{}, credentialSecret{}, "", err
			}
			public.Refresh = &refreshPublic
			secret.RefreshToken = refreshSecret.RefreshToken
			secret.ClientSecret = refreshSecret.ClientSecret
		}
		return public, secret, canonical, nil
	case "environment_variable":
		return domain.CredentialAuth{}, credentialSecret{}, "", domain.Unsupported("environment_variable credentials require a SecretEgress-capable sandbox provider")
	default:
		return domain.CredentialAuth{}, credentialSecret{}, "", domain.Validation("auth.type must be mcp_oauth, static_bearer, or environment_variable")
	}
}

func prepareOAuthRefresh(input OAuthRefreshCreateInput) (domain.OAuthRefreshPublic, credentialSecret, error) {
	if input.ClientID == "" || input.RefreshToken == "" || input.TokenEndpoint == "" || input.TokenEndpointAuth == "" {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, domain.Validation("auth.refresh requires client_id, refresh_token, token_endpoint, and token_endpoint_auth")
	}
	if err := validatePostgresText("auth.refresh.client_id", input.ClientID); err != nil {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, err
	}
	if err := validateOptionalPostgresText("auth.refresh.resource", input.Resource); err != nil {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, err
	}
	if err := validateOptionalPostgresText("auth.refresh.scope", input.Scope); err != nil {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, err
	}
	tokenEndpoint, _, err := validatedAndCanonicalHTTPSURL(input.TokenEndpoint, "auth.refresh.token_endpoint")
	if err != nil {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, err
	}
	if err := validateTokenEndpointAuth(input.TokenEndpointAuth, input.ClientSecret, true); err != nil {
		return domain.OAuthRefreshPublic{}, credentialSecret{}, err
	}
	secret := credentialSecret{RefreshToken: []byte(input.RefreshToken)}
	if input.ClientSecret != nil {
		secret.ClientSecret = []byte(*input.ClientSecret)
	}
	return domain.OAuthRefreshPublic{
		ClientID: input.ClientID, TokenEndpoint: tokenEndpoint,
		TokenEndpointAuth: input.TokenEndpointAuth,
		Resource:          cloneStringPointer(input.Resource), Scope: cloneStringPointer(input.Scope),
	}, secret, nil
}

func applyCredentialAuthUpdate(public *domain.CredentialAuth, secret *credentialSecret, input CredentialAuthUpdateInput) (bool, error) {
	changed := false
	switch input.Type {
	case domain.CredentialAuthStaticBearer:
		if input.Token.Present {
			if input.Token.Value != nil && *input.Token.Value == "" {
				return false, domain.Validation("auth.token cannot be empty")
			}
			var replacement []byte
			if input.Token.Value != nil {
				replacement = []byte(*input.Token.Value)
			}
			if !bytes.Equal(secret.Token, replacement) {
				secretcrypto.Zero(secret.Token)
				secret.Token = replacement
				changed = true
			}
		}
	case domain.CredentialAuthMCPOAuth:
		if input.AccessToken.Present {
			if input.AccessToken.Value != nil && *input.AccessToken.Value == "" {
				return false, domain.Validation("auth.access_token cannot be empty")
			}
			var replacement []byte
			if input.AccessToken.Value != nil {
				replacement = []byte(*input.AccessToken.Value)
			}
			if !bytes.Equal(secret.AccessToken, replacement) {
				secretcrypto.Zero(secret.AccessToken)
				secret.AccessToken = replacement
				changed = true
			}
		}
		if input.ExpiresAt.Present {
			if !equalOptionalTime(input.ExpiresAt.Value, public.ExpiresAt) {
				public.ExpiresAt = cloneTimePointer(input.ExpiresAt.Value)
				changed = true
			}
		}
		if input.Refresh.Present {
			if input.Refresh.Value == nil {
				if public.Refresh != nil || len(secret.RefreshToken) != 0 || len(secret.ClientSecret) != 0 {
					public.Refresh = nil
					secretcrypto.Zero(secret.RefreshToken)
					secretcrypto.Zero(secret.ClientSecret)
					secret.RefreshToken = nil
					secret.ClientSecret = nil
					changed = true
				}
				break
			}
			if public.Refresh == nil {
				return false, domain.Validation("auth.refresh cannot be added by update")
			}
			refresh := input.Refresh.Value
			if refresh.RefreshToken.Present {
				if refresh.RefreshToken.Value != nil && *refresh.RefreshToken.Value == "" {
					return false, domain.Validation("auth.refresh.refresh_token cannot be empty")
				}
				var replacement []byte
				if refresh.RefreshToken.Value != nil {
					replacement = []byte(*refresh.RefreshToken.Value)
				}
				if !bytes.Equal(secret.RefreshToken, replacement) {
					secretcrypto.Zero(secret.RefreshToken)
					secret.RefreshToken = replacement
					changed = true
				}
			}
			if refresh.Scope.Present {
				if err := validateOptionalPostgresText("auth.refresh.scope", refresh.Scope.Value); err != nil {
					return false, err
				}
				if !equalOptionalString(refresh.Scope.Value, public.Refresh.Scope) {
					public.Refresh.Scope = cloneStringPointer(refresh.Scope.Value)
					changed = true
				}
			}
			if refresh.TokenEndpointAuth != nil {
				authUpdate := refresh.TokenEndpointAuth
				var clientSecret *string
				if authUpdate.ClientSecret.Present {
					clientSecret = authUpdate.ClientSecret.Value
				}
				if err := validateTokenEndpointAuth(authUpdate.Type, clientSecret, false); err != nil {
					return false, err
				}
				if public.Refresh.TokenEndpointAuth != authUpdate.Type {
					public.Refresh.TokenEndpointAuth = authUpdate.Type
					changed = true
				}
				if authUpdate.ClientSecret.Present {
					if authUpdate.ClientSecret.Value != nil && *authUpdate.ClientSecret.Value == "" {
						return false, domain.Validation("auth.refresh.token_endpoint_auth.client_secret cannot be empty")
					}
					var replacement []byte
					if authUpdate.ClientSecret.Value != nil {
						replacement = []byte(*authUpdate.ClientSecret.Value)
					}
					if !bytes.Equal(secret.ClientSecret, replacement) {
						secretcrypto.Zero(secret.ClientSecret)
						secret.ClientSecret = replacement
						changed = true
					}
				}
			}
		}
	default:
		return false, domain.Validation("credential auth type is invalid")
	}
	return changed, nil
}

func sealCredentialSecret(cipher secretcrypto.Cipher, vaultID, credentialID, credentialKey string, auth domain.CredentialAuth, secret credentialSecret) (domain.SecretEnvelope, error) {
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return domain.SecretEnvelope{}, err
	}
	defer secretcrypto.Zero(plaintext)
	envelope, err := cipher.Seal(plaintext, credentialAAD(vaultID, credentialID, credentialKey, auth))
	if err != nil {
		return domain.SecretEnvelope{}, errors.New("seal credential secret: encryption failed")
	}
	return envelope, nil
}

func credentialAAD(vaultID, credentialID, credentialKey string, auth domain.CredentialAuth) []byte {
	publicAuth, _ := json.Marshal(auth)
	return append([]byte("managed-agent-go/vault-credential/v1\x00"+vaultID+"\x00"+credentialID+"\x00"+credentialKey+"\x00"), publicAuth...)
}

func (s *VaultService) verifyCredentialEnvelope(item domain.VaultCredential) error {
	if item.ArchivedAt != nil && item.SecretEnvelope == nil {
		return nil
	}
	if item.SecretEnvelope == nil {
		return errors.New("credential secret is unavailable")
	}
	plaintext, err := s.cipher.Open(*item.SecretEnvelope, credentialAAD(item.VaultID, item.ID, item.CredentialKey, item.Auth))
	if err != nil {
		return errors.New("credential secret integrity check failed")
	}
	secretcrypto.Zero(plaintext)
	return nil
}

func publicCredential(item domain.VaultCredential) domain.VaultCredential {
	item.SecretEnvelope = nil
	item.CredentialKey = ""
	item.Version = 0
	return item
}

// CanonicalMCPServerURL is the single identity function used for stored MCP
// credential keys and, in the runtime PR, for MCP server lookup. It rejects
// plaintext transport and URL features that could redirect credentials to a
// different authority.
func CanonicalMCPServerURL(raw string) (string, error) {
	_, canonical, err := validatedAndCanonicalHTTPSURL(raw, "auth.mcp_server_url")
	return canonical, err
}

func validatedAndCanonicalHTTPSURL(raw, field string) (string, string, error) {
	if raw == "" {
		return "", "", domain.Validation(field + " is required")
	}
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > 2048 {
		return "", "", domain.Validation(field + " must contain at most 2048 characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return "", "", domain.Validation(field + " must be an absolute HTTPS URL without user info or fragment")
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return "", "", domain.Validation(field + " must contain a host")
	}
	port := u.Port()
	if strings.Contains(u.Host, ":") && port == "" && !strings.HasSuffix(u.Host, "]") {
		return "", "", domain.Validation(field + " contains an invalid port")
	}
	u.Scheme = "https"
	if port != "" {
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", "", domain.Validation(field + " contains an invalid port")
		}
	}
	publicURL := u.String()
	canonical := *u
	switch port {
	case "", "443":
		if strings.Contains(hostname, ":") {
			canonical.Host = "[" + hostname + "]"
		} else {
			canonical.Host = hostname
		}
	default:
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", "", domain.Validation(field + " contains an invalid port")
		}
		if parsedPort == 443 {
			if strings.Contains(hostname, ":") {
				canonical.Host = "[" + hostname + "]"
			} else {
				canonical.Host = hostname
			}
		} else {
			canonical.Host = net.JoinHostPort(hostname, strconv.Itoa(parsedPort))
		}
	}
	if canonical.Path == "" {
		canonical.Path = "/"
	}
	return publicURL, canonical.String(), nil
}

func validateTokenEndpointAuth(authType string, clientSecret *string, create bool) error {
	switch authType {
	case "none":
		if !create {
			return domain.Validation("auth.refresh.token_endpoint_auth.type must be client_secret_basic or client_secret_post on update")
		}
		if clientSecret != nil {
			return domain.Validation("auth.refresh.token_endpoint_auth.client_secret is not allowed for type none")
		}
	case "client_secret_basic", "client_secret_post":
		if create && (clientSecret == nil || *clientSecret == "") {
			return domain.Validation("auth.refresh.token_endpoint_auth.client_secret is required")
		}
		if clientSecret != nil && *clientSecret == "" {
			return domain.Validation("auth.refresh.token_endpoint_auth.client_secret cannot be empty")
		}
	default:
		return domain.Validation("auth.refresh.token_endpoint_auth.type is invalid")
	}
	return nil
}

func validateVaultDisplayName(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 255 {
		return domain.Validation("display_name must contain between 1 and 255 characters")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return domain.Validation("display_name contains a control character")
		}
	}
	return nil
}

func validateOptionalCredentialDisplayName(value *string) error {
	if value == nil {
		return nil
	}
	return validateVaultDisplayName(*value)
}

func validateVaultMetadata(metadata map[string]string) error {
	if len(metadata) > 16 {
		return domain.Validation("metadata must contain at most 16 entries")
	}
	for key, value := range metadata {
		if err := validateVaultMetadataEntry(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateVaultMetadataPatch(metadata map[string]*string) error {
	for key, value := range metadata {
		if value == nil {
			if err := validateVaultMetadataKey(key); err != nil {
				return err
			}
			continue
		}
		if err := validateVaultMetadataEntry(key, *value); err != nil {
			return err
		}
	}
	return nil
}

func validateVaultMetadataEntry(key, value string) error {
	if err := validateVaultMetadataKey(key); err != nil {
		return err
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || utf8.RuneCountInString(value) > 512 {
		return domain.Validation("metadata values must contain at most 512 characters and cannot contain NUL")
	}
	return nil
}

func validateVaultMetadataKey(key string) error {
	if !utf8.ValidString(key) || strings.ContainsRune(key, '\x00') || utf8.RuneCountInString(key) < 1 || utf8.RuneCountInString(key) > 64 {
		return domain.Validation("metadata keys must contain between 1 and 64 characters and cannot contain NUL")
	}
	return nil
}

func validatePostgresText(field, value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return domain.Validation(field + " must be valid UTF-8 and cannot contain NUL")
	}
	return nil
}

func validateOptionalPostgresText(field string, value *string) error {
	if value == nil {
		return nil
	}
	return validatePostgresText(field, *value)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func cloneCredentialAuth(value domain.CredentialAuth) domain.CredentialAuth {
	cloned := value
	cloned.ExpiresAt = cloneTimePointer(value.ExpiresAt)
	if value.Refresh != nil {
		refresh := *value.Refresh
		refresh.Resource = cloneStringPointer(value.Refresh.Resource)
		refresh.Scope = cloneStringPointer(value.Refresh.Scope)
		cloned.Refresh = &refresh
	}
	return cloned
}

func equalOptionalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func equalOptionalTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

func equalCredentialAuth(a, b domain.CredentialAuth) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func equalVaultStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
