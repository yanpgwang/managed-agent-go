package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

type vaultCreateRequest struct {
	DisplayName string                               `json:"display_name"`
	Metadata    optionalJSONField[map[string]string] `json:"metadata"`
}

type vaultUpdateRequest struct {
	DisplayName optionalJSONField[string]             `json:"display_name"`
	Metadata    optionalJSONField[map[string]*string] `json:"metadata"`
}

type credentialCreateRequest struct {
	DisplayName optionalJSONField[string]            `json:"display_name"`
	Metadata    optionalJSONField[map[string]string] `json:"metadata"`
	Auth        json.RawMessage                      `json:"auth"`
}

type credentialUpdateRequest struct {
	DisplayName optionalJSONField[string]             `json:"display_name"`
	Metadata    optionalJSONField[map[string]*string] `json:"metadata"`
	Auth        optionalJSONField[json.RawMessage]    `json:"auth"`
}

type authTypeRequest struct {
	Type string `json:"type"`
}

type staticBearerCreateRequest struct {
	Type         string `json:"type"`
	Token        string `json:"token"`
	MCPServerURL string `json:"mcp_server_url"`
}

type mcpOAuthCreateRequest struct {
	Type         string                                       `json:"type"`
	AccessToken  string                                       `json:"access_token"`
	MCPServerURL string                                       `json:"mcp_server_url"`
	ExpiresAt    optionalJSONField[time.Time]                 `json:"expires_at"`
	Refresh      optionalJSONField[mcpOAuthRefreshCreateBody] `json:"refresh"`
}

type mcpOAuthRefreshCreateBody struct {
	ClientID          string                         `json:"client_id"`
	RefreshToken      string                         `json:"refresh_token"`
	TokenEndpoint     string                         `json:"token_endpoint"`
	TokenEndpointAuth tokenEndpointAuthCreateRequest `json:"token_endpoint_auth"`
	Resource          optionalJSONField[string]      `json:"resource"`
	Scope             optionalJSONField[string]      `json:"scope"`
}

type tokenEndpointAuthCreateRequest struct {
	Type         string                    `json:"type"`
	ClientSecret optionalJSONField[string] `json:"client_secret"`
}

type staticBearerUpdateRequest struct {
	Type  string                    `json:"type"`
	Token optionalJSONField[string] `json:"token"`
}

type mcpOAuthUpdateRequest struct {
	Type        string                                       `json:"type"`
	AccessToken optionalJSONField[string]                    `json:"access_token"`
	ExpiresAt   optionalJSONField[time.Time]                 `json:"expires_at"`
	Refresh     optionalJSONField[mcpOAuthRefreshUpdateBody] `json:"refresh"`
}

type mcpOAuthRefreshUpdateBody struct {
	RefreshToken      optionalJSONField[string]                         `json:"refresh_token"`
	Scope             optionalJSONField[string]                         `json:"scope"`
	TokenEndpointAuth optionalJSONField[tokenEndpointAuthUpdateRequest] `json:"token_endpoint_auth"`
}

type tokenEndpointAuthUpdateRequest struct {
	Type         string                    `json:"type"`
	ClientSecret optionalJSONField[string] `json:"client_secret"`
}

func (s *Server) createVault(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	var body vaultCreateRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Metadata.Null {
		writeError(w, domain.Validation("metadata cannot be null"))
		return
	}
	var metadata map[string]string
	if body.Metadata.Present {
		metadata = body.Metadata.Value
	}
	item, err := s.deps.Vaults.CreateVault(r.Context(), app.VaultCreateInput{
		DisplayName: body.DisplayName, Metadata: metadata,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vaultToJSON(item))
}

func (s *Server) getVault(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	item, err := s.deps.Vaults.GetVault(r.Context(), r.PathValue("vault_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vaultToJSON(item))
}

func (s *Server) updateVault(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	var body vaultUpdateRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, err)
		return
	}
	item, err := s.deps.Vaults.UpdateVault(r.Context(), r.PathValue("vault_id"), app.VaultUpdateInput{
		DisplayName: nullablePatchFromJSON(body.DisplayName), Metadata: nullablePatchFromJSON(body.Metadata),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vaultToJSON(item))
}

func (s *Server) listVaults(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	query, filter, err := parseVaultListQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Vaults.ListVaults(r.Context(), query)
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(page.Vaults))
	for _, item := range page.Vaults {
		data = append(data, vaultToJSON(item))
	}
	var next any
	if page.HasNext && len(page.Vaults) > 0 {
		last := page.Vaults[len(page.Vaults)-1]
		next = encodeResourceCursor(resourceCursor{
			Kind: vaultListCursorKind, CreatedAt: last.CreatedAt.Format(timeFmt),
			ID: last.ID, Filter: filter.fingerprint(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "next_page": next})
}

func (s *Server) archiveVault(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	item, err := s.deps.Vaults.ArchiveVault(r.Context(), r.PathValue("vault_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vaultToJSON(item))
}

func (s *Server) deleteVault(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	id := r.PathValue("vault_id")
	if err := s.deps.Vaults.DeleteVault(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "type": "vault_deleted"})
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	var body credentialCreateRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Metadata.Null || len(bytes.TrimSpace(body.Auth)) == 0 || bytes.Equal(bytes.TrimSpace(body.Auth), []byte("null")) {
		writeError(w, domain.Validation("auth is required and metadata cannot be null"))
		return
	}
	auth, err := parseCredentialAuthCreate(body.Auth)
	if err != nil {
		writeError(w, err)
		return
	}
	var displayName *string
	if body.DisplayName.Present && !body.DisplayName.Null {
		displayName = &body.DisplayName.Value
	}
	var metadata map[string]string
	if body.Metadata.Present {
		metadata = body.Metadata.Value
	}
	item, err := s.deps.Vaults.CreateCredential(r.Context(), r.PathValue("vault_id"), app.CredentialCreateInput{
		DisplayName: displayName, Metadata: metadata, Auth: auth,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialToJSON(item))
}

func (s *Server) getCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	item, err := s.deps.Vaults.GetCredential(r.Context(), r.PathValue("vault_id"), r.PathValue("credential_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialToJSON(item))
}

func (s *Server) updateCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	var body credentialUpdateRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Auth.Null {
		writeError(w, domain.Validation("auth cannot be null"))
		return
	}
	var auth *app.CredentialAuthUpdateInput
	if body.Auth.Present {
		parsed, err := parseCredentialAuthUpdate(body.Auth.Value)
		if err != nil {
			writeError(w, err)
			return
		}
		auth = &parsed
	}
	item, err := s.deps.Vaults.UpdateCredential(r.Context(), r.PathValue("vault_id"), r.PathValue("credential_id"), app.CredentialUpdateInput{
		DisplayName: nullablePatchFromJSON(body.DisplayName), Metadata: nullablePatchFromJSON(body.Metadata), Auth: auth,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialToJSON(item))
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	vaultID := r.PathValue("vault_id")
	query, filter, err := parseCredentialListQuery(r, vaultID)
	if err != nil {
		writeError(w, err)
		return
	}
	page, err := s.deps.Vaults.ListCredentials(r.Context(), vaultID, query)
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(page.Credentials))
	for _, item := range page.Credentials {
		data = append(data, credentialToJSON(item))
	}
	var next any
	if page.HasNext && len(page.Credentials) > 0 {
		last := page.Credentials[len(page.Credentials)-1]
		next = encodeResourceCursor(resourceCursor{
			Kind: credentialListCursorKind, CreatedAt: last.CreatedAt.Format(timeFmt),
			ID: last.ID, Filter: filter.fingerprint(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "next_page": next})
}

func (s *Server) archiveCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	item, err := s.deps.Vaults.ArchiveCredential(r.Context(), r.PathValue("vault_id"), r.PathValue("credential_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialToJSON(item))
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	id := r.PathValue("credential_id")
	if err := s.deps.Vaults.DeleteCredential(r.Context(), r.PathValue("vault_id"), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "type": "vault_credential_deleted"})
}

func (s *Server) validateMCPOAuthCredential(w http.ResponseWriter, r *http.Request) {
	if !s.vaultsConfigured(w) {
		return
	}
	result, err := s.deps.Vaults.ValidateMCPOAuthCredential(
		r.Context(),
		r.PathValue("vault_id"),
		r.PathValue("credential_id"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type":              "vault_credential_validation",
		"credential_id":     result.CredentialID,
		"vault_id":          result.VaultID,
		"validated_at":      result.ValidatedAt.Format(timeFmt),
		"has_refresh_token": result.HasRefreshToken,
		"status":            result.Status,
		"mcp_probe":         result.MCPProbe,
		"refresh":           result.Refresh,
	})
}

func parseCredentialAuthCreate(raw json.RawMessage) (app.CredentialAuthCreateInput, error) {
	var discriminator authTypeRequest
	if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.Type == "" {
		return app.CredentialAuthCreateInput{}, domain.Validation("auth.type is required")
	}
	switch discriminator.Type {
	case domain.CredentialAuthStaticBearer:
		var body staticBearerCreateRequest
		if err := decodeStrictJSON(raw, &body); err != nil {
			return app.CredentialAuthCreateInput{}, err
		}
		return app.CredentialAuthCreateInput{Type: body.Type, Token: body.Token, MCPServerURL: body.MCPServerURL}, nil
	case domain.CredentialAuthMCPOAuth:
		var body mcpOAuthCreateRequest
		if err := decodeStrictJSON(raw, &body); err != nil {
			return app.CredentialAuthCreateInput{}, err
		}
		input := app.CredentialAuthCreateInput{Type: body.Type, AccessToken: body.AccessToken, MCPServerURL: body.MCPServerURL}
		if body.ExpiresAt.Present && !body.ExpiresAt.Null {
			input.ExpiresAt = &body.ExpiresAt.Value
		}
		if body.Refresh.Present && !body.Refresh.Null {
			refresh, err := parseOAuthRefreshCreate(body.Refresh.Value)
			if err != nil {
				return app.CredentialAuthCreateInput{}, err
			}
			input.Refresh = &refresh
		}
		return input, nil
	case "environment_variable":
		return app.CredentialAuthCreateInput{}, domain.Unsupported("environment_variable credentials require a SecretEgress-capable sandbox provider")
	default:
		return app.CredentialAuthCreateInput{}, domain.Validation("auth.type must be mcp_oauth, static_bearer, or environment_variable")
	}
}

func parseOAuthRefreshCreate(body mcpOAuthRefreshCreateBody) (app.OAuthRefreshCreateInput, error) {
	input := app.OAuthRefreshCreateInput{
		ClientID: body.ClientID, RefreshToken: body.RefreshToken,
		TokenEndpoint: body.TokenEndpoint, TokenEndpointAuth: body.TokenEndpointAuth.Type,
	}
	if body.TokenEndpointAuth.ClientSecret.Present && !body.TokenEndpointAuth.ClientSecret.Null {
		input.ClientSecret = &body.TokenEndpointAuth.ClientSecret.Value
	}
	if body.Resource.Present && !body.Resource.Null {
		input.Resource = &body.Resource.Value
	}
	if body.Scope.Present && !body.Scope.Null {
		input.Scope = &body.Scope.Value
	}
	return input, nil
}

func parseCredentialAuthUpdate(raw json.RawMessage) (app.CredentialAuthUpdateInput, error) {
	var discriminator authTypeRequest
	if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.Type == "" {
		return app.CredentialAuthUpdateInput{}, domain.Validation("auth.type is required")
	}
	switch discriminator.Type {
	case domain.CredentialAuthStaticBearer:
		var body staticBearerUpdateRequest
		if err := decodeStrictJSON(raw, &body); err != nil {
			return app.CredentialAuthUpdateInput{}, err
		}
		input := app.CredentialAuthUpdateInput{Type: body.Type, Token: nullablePatchFromJSON(body.Token)}
		return input, nil
	case domain.CredentialAuthMCPOAuth:
		var body mcpOAuthUpdateRequest
		if err := decodeStrictJSON(raw, &body); err != nil {
			return app.CredentialAuthUpdateInput{}, err
		}
		input := app.CredentialAuthUpdateInput{
			Type: body.Type, AccessToken: nullablePatchFromJSON(body.AccessToken), ExpiresAt: nullablePatchFromJSON(body.ExpiresAt),
		}
		if body.Refresh.Present {
			input.Refresh.Present = true
			if !body.Refresh.Null {
				refresh, err := parseOAuthRefreshUpdate(body.Refresh.Value)
				if err != nil {
					return app.CredentialAuthUpdateInput{}, err
				}
				input.Refresh.Value = &refresh
			}
		}
		return input, nil
	case "environment_variable":
		return app.CredentialAuthUpdateInput{}, domain.Unsupported("environment_variable credentials require a SecretEgress-capable sandbox provider")
	default:
		return app.CredentialAuthUpdateInput{}, domain.Validation("auth.type must be mcp_oauth, static_bearer, or environment_variable")
	}
}

func parseOAuthRefreshUpdate(body mcpOAuthRefreshUpdateBody) (app.OAuthRefreshUpdateInput, error) {
	if body.TokenEndpointAuth.Null {
		return app.OAuthRefreshUpdateInput{}, domain.Validation("auth.refresh.token_endpoint_auth cannot be null")
	}
	input := app.OAuthRefreshUpdateInput{
		RefreshToken: nullablePatchFromJSON(body.RefreshToken), Scope: nullablePatchFromJSON(body.Scope),
	}
	if body.TokenEndpointAuth.Present {
		auth := body.TokenEndpointAuth.Value
		input.TokenEndpointAuth = &app.OAuthTokenEndpointAuthUpdateInput{
			Type: auth.Type, ClientSecret: nullablePatchFromJSON(auth.ClientSecret),
		}
	}
	return input, nil
}

func nullablePatchFromJSON[T any](field optionalJSONField[T]) app.NullablePatch[T] {
	patch := app.NullablePatch[T]{Present: field.Present}
	if field.Present && !field.Null {
		value := field.Value
		patch.Value = &value
	}
	return patch
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.Validation("invalid JSON body: " + err.Error())
	}
	return nil
}

type vaultCursorFilter struct {
	IncludeArchived bool `json:"include_archived"`
}

func (f vaultCursorFilter) fingerprint() string { return resourceFilterFingerprint(f) }

type credentialCursorFilter struct {
	VaultID         string `json:"vault_id"`
	IncludeArchived bool   `json:"include_archived"`
}

func (f credentialCursorFilter) fingerprint() string { return resourceFilterFingerprint(f) }

func parseVaultListQuery(r *http.Request) (app.VaultListQuery, vaultCursorFilter, error) {
	values := r.URL.Query()
	query := app.VaultListQuery{Limit: app.DefaultVaultListLimit}
	filter := vaultCursorFilter{}
	if values.Has("limit") {
		limit, err := parseResourceListLimit(values.Get("limit"))
		if err != nil || limit > app.MaxVaultListLimit {
			return query, filter, domain.Validation("limit must be between 1 and 100")
		}
		query.Limit = limit
	}
	if values.Has("include_archived") {
		value, err := parseResourceListBool(values.Get("include_archived"), "include_archived")
		if err != nil {
			return query, filter, err
		}
		query.IncludeArchived, filter.IncludeArchived = value, value
	}
	if values.Has("page") {
		cursor, ok := decodeResourceCursor(values.Get("page"), vaultListCursorKind)
		if !ok || cursor.Filter != filter.fingerprint() {
			return query, filter, domain.Validation("invalid page cursor")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return query, filter, domain.Validation("invalid page cursor")
		}
		query.After = &app.ResourcePageBoundary{CreatedAt: createdAt.UTC(), ID: cursor.ID}
	}
	return query, filter, nil
}

func parseCredentialListQuery(r *http.Request, vaultID string) (app.CredentialListQuery, credentialCursorFilter, error) {
	values := r.URL.Query()
	query := app.CredentialListQuery{Limit: app.DefaultCredentialListLimit}
	filter := credentialCursorFilter{VaultID: vaultID}
	if values.Has("limit") {
		limit, err := parseResourceListLimit(values.Get("limit"))
		if err != nil || limit > app.MaxCredentialListLimit {
			return query, filter, domain.Validation("limit must be between 1 and 100")
		}
		query.Limit = limit
	}
	if values.Has("include_archived") {
		value, err := parseResourceListBool(values.Get("include_archived"), "include_archived")
		if err != nil {
			return query, filter, err
		}
		query.IncludeArchived, filter.IncludeArchived = value, value
	}
	if values.Has("page") {
		cursor, ok := decodeResourceCursor(values.Get("page"), credentialListCursorKind)
		if !ok || cursor.Filter != filter.fingerprint() {
			return query, filter, domain.Validation("invalid page cursor")
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			return query, filter, domain.Validation("invalid page cursor")
		}
		query.After = &app.ResourcePageBoundary{CreatedAt: createdAt.UTC(), ID: cursor.ID}
	}
	return query, filter, nil
}

func vaultToJSON(item domain.Vault) map[string]any {
	var archived any
	if item.ArchivedAt != nil {
		archived = item.ArchivedAt.Format(timeFmt)
	}
	metadata := item.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	return map[string]any{
		"id": item.ID, "type": "vault", "display_name": item.DisplayName,
		"metadata": metadata, "created_at": item.CreatedAt.Format(timeFmt),
		"updated_at": item.UpdatedAt.Format(timeFmt), "archived_at": archived,
	}
}

func credentialToJSON(item domain.VaultCredential) map[string]any {
	var archived, displayName any
	if item.ArchivedAt != nil {
		archived = item.ArchivedAt.Format(timeFmt)
	}
	if item.DisplayName != nil {
		displayName = *item.DisplayName
	}
	metadata := item.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	return map[string]any{
		"id": item.ID, "type": "vault_credential", "vault_id": item.VaultID,
		"display_name": displayName, "metadata": metadata, "auth": credentialAuthToJSON(item.Auth),
		"created_at": item.CreatedAt.Format(timeFmt), "updated_at": item.UpdatedAt.Format(timeFmt),
		"archived_at": archived,
	}
}

func credentialAuthToJSON(auth domain.CredentialAuth) map[string]any {
	result := map[string]any{"type": auth.Type, "mcp_server_url": auth.MCPServerURL}
	if auth.Type == domain.CredentialAuthMCPOAuth {
		var expiresAt, refresh any
		if auth.ExpiresAt != nil {
			expiresAt = auth.ExpiresAt.Format(timeFmt)
		}
		if auth.Refresh != nil {
			var resource, scope any
			if auth.Refresh.Resource != nil {
				resource = *auth.Refresh.Resource
			}
			if auth.Refresh.Scope != nil {
				scope = *auth.Refresh.Scope
			}
			refresh = map[string]any{
				"client_id": auth.Refresh.ClientID, "token_endpoint": auth.Refresh.TokenEndpoint,
				"token_endpoint_auth": map[string]any{"type": auth.Refresh.TokenEndpointAuth},
				"resource":            resource, "scope": scope,
			}
		}
		result["expires_at"] = expiresAt
		result["refresh"] = refresh
	}
	return result
}

func (s *Server) vaultsConfigured(w http.ResponseWriter) bool {
	if s.deps.Vaults != nil {
		return true
	}
	writeError(w, domain.Unsupported("Vault API is not configured"))
	return false
}
