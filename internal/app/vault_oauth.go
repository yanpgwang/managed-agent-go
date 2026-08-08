package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/yanpgwang/managed-agent-go/internal/credentialruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/secretcrypto"
)

// ValidateMCPOAuthCredential live-probes an OAuth credential and, when the
// current access token is expired or rejected, attempts the configured refresh
// exchange before probing once more. Successful refreshes are persisted so the
// next Session resolution observes the rotated token.
func (s *VaultService) ValidateMCPOAuthCredential(
	ctx context.Context,
	vaultID string,
	credentialID string,
) (CredentialValidation, error) {
	item, secret, err := s.loadOAuthCredential(ctx, vaultID, credentialID)
	if err != nil {
		return CredentialValidation{}, err
	}
	defer secret.zero()
	if s.validator == nil {
		return CredentialValidation{}, domain.Unsupported("MCP OAuth validation is unavailable")
	}

	validated := CredentialValidation{
		CredentialID: credentialID, VaultID: vaultID,
		ValidatedAt:     s.clock.Now().UTC(),
		HasRefreshToken: item.Auth.Refresh != nil && len(secret.RefreshToken) != 0,
	}
	expired := item.Auth.ExpiresAt != nil && !item.Auth.ExpiresAt.After(s.clock.Now())
	// An expired token with refresh metadata can go straight to refresh. Without
	// a refresh token, still live-probe it so the diagnostic includes the MCP
	// handshake failure (and accepts it if the server still considers it valid).
	probeCurrent := !expired || !validated.HasRefreshToken
	if probeCurrent && len(secret.AccessToken) != 0 {
		probe, err := s.validator.ValidateBearer(
			ctx, item.Auth.MCPServerURL, string(secret.AccessToken),
		)
		if err != nil {
			return CredentialValidation{}, err
		}
		if probe.Verdict == credentialruntime.VerdictValid {
			validated.Status = credentialruntime.VerdictValid
			return validated, nil
		}
		validated.MCPProbe = probe.Failure
		if probe.Verdict == credentialruntime.VerdictUnknown {
			validated.Status = credentialruntime.VerdictUnknown
			return validated, nil
		}
	}

	if !validated.HasRefreshToken {
		validated.Status = credentialruntime.VerdictInvalid
		validated.Refresh = &CredentialRefreshValidation{
			Status: credentialruntime.OAuthRefreshUnavailable,
		}
		return validated, nil
	}
	refresh, err := s.refreshOAuthCredential(ctx, item, secret)
	if err != nil {
		return CredentialValidation{}, err
	}
	validated.Refresh = &CredentialRefreshValidation{
		Status: refresh.Result.Status, HTTPResponse: refresh.Result.HTTPResponse,
	}
	if refresh.Result.Status != credentialruntime.OAuthRefreshSucceeded {
		validated.Status = refresh.Result.Verdict
		if validated.Status == "" {
			validated.Status = credentialruntime.VerdictUnknown
		}
		return validated, nil
	}

	probe, err := s.validator.ValidateBearer(
		ctx, item.Auth.MCPServerURL, refresh.Result.AccessToken,
	)
	if err != nil {
		return CredentialValidation{}, err
	}
	validated.Status = probe.Verdict
	validated.MCPProbe = probe.Failure
	return validated, nil
}

type oauthRefreshAttempt struct {
	Result credentialruntime.OAuthRefreshResult
}

var errRefreshCredentialChanged = errors.New("credential changed during OAuth refresh")

func (s *VaultService) loadOAuthCredential(
	ctx context.Context,
	vaultID string,
	credentialID string,
) (domain.VaultCredential, credentialSecret, error) {
	item, err := s.repo.GetCredential(ctx, vaultID, credentialID)
	if err != nil {
		return domain.VaultCredential{}, credentialSecret{}, err
	}
	if item.ArchivedAt != nil || item.SecretEnvelope == nil {
		return domain.VaultCredential{}, credentialSecret{}, domain.Validation(
			"archived credentials cannot be validated or refreshed",
		)
	}
	if item.Auth.Type != domain.CredentialAuthMCPOAuth {
		return domain.VaultCredential{}, credentialSecret{}, domain.Validation(
			"credential auth type must be mcp_oauth",
		)
	}
	secret, err := s.openCredentialSecret(item)
	return item, secret, err
}

func (s *VaultService) openCredentialSecret(
	item domain.VaultCredential,
) (credentialSecret, error) {
	if item.SecretEnvelope == nil {
		return credentialSecret{}, errors.New("selected credential secret is unavailable")
	}
	plaintext, err := s.cipher.Open(
		*item.SecretEnvelope,
		credentialAAD(item.VaultID, item.ID, item.CredentialKey, item.Auth),
	)
	if err != nil {
		return credentialSecret{}, errors.New("selected credential failed its integrity check")
	}
	defer secretcrypto.Zero(plaintext)
	var secret credentialSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		return credentialSecret{}, errors.New("selected credential payload is invalid")
	}
	return secret, nil
}

func (s *VaultService) refreshOAuthCredential(
	ctx context.Context,
	item domain.VaultCredential,
	secret credentialSecret,
) (oauthRefreshAttempt, error) {
	if s.refresher == nil {
		return oauthRefreshAttempt{}, errors.New("OAuth refresher is unavailable")
	}
	if item.Auth.Refresh == nil || len(secret.RefreshToken) == 0 {
		return oauthRefreshAttempt{Result: credentialruntime.OAuthRefreshResult{
			Status: credentialruntime.OAuthRefreshUnavailable,
		}}, nil
	}
	refresh := item.Auth.Refresh
	request := credentialruntime.OAuthRefreshRequest{
		TokenEndpoint: refresh.TokenEndpoint, ClientID: refresh.ClientID,
		RefreshToken:      string(secret.RefreshToken),
		TokenEndpointAuth: refresh.TokenEndpointAuth,
		ClientSecret:      string(secret.ClientSecret),
		Resource:          optionalString(refresh.Resource), Scope: optionalString(refresh.Scope),
	}
	result, err := s.refresher.Refresh(ctx, request)
	if err != nil {
		return oauthRefreshAttempt{}, err
	}
	if result.Status != credentialruntime.OAuthRefreshSucceeded {
		return oauthRefreshAttempt{Result: result}, nil
	}
	if result.AccessToken == "" {
		return oauthRefreshAttempt{}, errors.New("OAuth refresher returned an empty access token")
	}
	if _, err := s.persistOAuthRefresh(
		ctx, item.VaultID, item.ID, request.RefreshToken, result,
	); errors.Is(err, errRefreshCredentialChanged) {
		// A concurrent rotation or refresh is authoritative. Reuse it when it
		// already produced a current access token; otherwise ask the caller to
		// retry instead of overwriting a new refresh grant.
		current, currentSecret, loadErr := s.loadOAuthCredential(ctx, item.VaultID, item.ID)
		if loadErr != nil {
			return oauthRefreshAttempt{}, loadErr
		}
		defer currentSecret.zero()
		if len(currentSecret.AccessToken) != 0 &&
			(current.Auth.ExpiresAt == nil || current.Auth.ExpiresAt.After(s.clock.Now())) {
			result.AccessToken = string(currentSecret.AccessToken)
			return oauthRefreshAttempt{Result: result}, nil
		}
		return oauthRefreshAttempt{}, domain.Conflict(
			"credential changed during OAuth refresh; retry validation",
		)
	} else if err != nil {
		return oauthRefreshAttempt{}, err
	}
	return oauthRefreshAttempt{Result: result}, nil
}

func (s *VaultService) persistOAuthRefresh(
	ctx context.Context,
	vaultID string,
	credentialID string,
	usedRefreshToken string,
	result credentialruntime.OAuthRefreshResult,
) (domain.VaultCredential, error) {
	now := s.clock.Now().UTC()
	return s.repo.UpdateCredential(
		ctx, vaultID, credentialID,
		func(current domain.VaultCredential) (domain.VaultCredential, bool, error) {
			if current.ArchivedAt != nil || current.SecretEnvelope == nil ||
				current.Auth.Type != domain.CredentialAuthMCPOAuth || current.Auth.Refresh == nil {
				return domain.VaultCredential{}, false, errRefreshCredentialChanged
			}
			currentSecret, err := s.openCredentialSecret(current)
			if err != nil {
				return domain.VaultCredential{}, false, err
			}
			defer currentSecret.zero()
			if string(currentSecret.RefreshToken) != usedRefreshToken {
				return domain.VaultCredential{}, false, errRefreshCredentialChanged
			}

			next := current
			next.Auth = cloneCredentialAuth(current.Auth)
			secretcrypto.Zero(currentSecret.AccessToken)
			currentSecret.AccessToken = []byte(result.AccessToken)
			if result.RefreshToken != nil {
				secretcrypto.Zero(currentSecret.RefreshToken)
				currentSecret.RefreshToken = []byte(*result.RefreshToken)
			}
			next.Auth.ExpiresAt = nil
			if result.ExpiresIn != nil {
				expiresAt := now.Add(*result.ExpiresIn)
				next.Auth.ExpiresAt = &expiresAt
			}
			envelope, err := sealCredentialSecret(
				s.cipher, vaultID, credentialID, next.CredentialKey, next.Auth, currentSecret,
			)
			if err != nil {
				return domain.VaultCredential{}, false, err
			}
			next.SecretEnvelope = &envelope
			next.Version = current.Version + 1
			next.UpdatedAt = now
			return next, true, nil
		},
	)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
