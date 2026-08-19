// Package oauthclient implements the OAuth refresh-token exchange used by
// Vault-backed MCP credentials.
package oauthclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yanpgwang/mango/internal/credentialruntime"
	"github.com/yanpgwang/mango/internal/httpegress"
)

const (
	maxOAuthResponseBody  = 1 << 20
	maxCapturedBodyLength = 16 << 10
)

type Client struct {
	http *http.Client
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpegress.NewPublicClient(30 * time.Second)
	}
	cloned := *httpClient
	// Refresh requests can contain a client secret in the request body. Do not
	// replay them through redirects, even when the standard client would remove
	// the Authorization header.
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{http: &cloned}
}

func (c *Client) Refresh(
	ctx context.Context,
	input credentialruntime.OAuthRefreshRequest,
) (credentialruntime.OAuthRefreshResult, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {input.RefreshToken},
	}
	switch input.TokenEndpointAuth {
	case "none":
		form.Set("client_id", input.ClientID)
	case "client_secret_basic":
		// Applied to the request below.
	case "client_secret_post":
		form.Set("client_id", input.ClientID)
		form.Set("client_secret", input.ClientSecret)
	default:
		return credentialruntime.OAuthRefreshResult{}, errors.New(
			"unsupported OAuth token endpoint authentication method",
		)
	}
	if input.Resource != "" {
		form.Set("resource", input.Resource)
	}
	if input.Scope != "" {
		form.Set("scope", input.Scope)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		input.TokenEndpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return credentialruntime.OAuthRefreshResult{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if input.TokenEndpointAuth == "client_secret_basic" {
		request.SetBasicAuth(input.ClientID, input.ClientSecret)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return credentialruntime.OAuthRefreshResult{
			Status:  credentialruntime.OAuthRefreshConnectError,
			Verdict: credentialruntime.VerdictUnknown,
		}, nil
	}
	defer func() { _ = response.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponseBody+1))
	tooLarge := len(raw) > maxOAuthResponseBody
	if tooLarge {
		raw = raw[:maxOAuthResponseBody]
	}

	result := credentialruntime.OAuthRefreshResult{
		Status: credentialruntime.OAuthRefreshFailed,
		HTTPResponse: capturedResponse(
			response,
			raw,
			tooLarge,
			input.RefreshToken,
			input.ClientSecret,
		),
	}
	if readErr != nil {
		result.Status = credentialruntime.OAuthRefreshConnectError
		result.Verdict = credentialruntime.VerdictUnknown
		result.HTTPResponse.Body = ""
		result.HTTPResponse.BodyTruncated = true
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 &&
			response.StatusCode != http.StatusTooManyRequests {
			result.Verdict = credentialruntime.VerdictInvalid
		} else {
			result.Verdict = credentialruntime.VerdictUnknown
		}
		return result, nil
	}
	if tooLarge {
		result.Verdict = credentialruntime.VerdictUnknown
		return result, nil
	}

	var payload struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken *string         `json:"refresh_token"`
		ExpiresIn    json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.AccessToken == "" {
		result.Verdict = credentialruntime.VerdictUnknown
		return result, nil
	}
	result.Status = credentialruntime.OAuthRefreshSucceeded
	result.Verdict = credentialruntime.VerdictValid
	result.AccessToken = payload.AccessToken
	if payload.RefreshToken != nil && *payload.RefreshToken != "" {
		result.RefreshToken = payload.RefreshToken
	}
	if expiresIn, ok := parseExpiresIn(payload.ExpiresIn); ok {
		result.ExpiresIn = &expiresIn
	}
	// Re-scrub now that newly issued tokens are known.
	result.HTTPResponse = capturedResponse(
		response,
		raw,
		false,
		input.RefreshToken,
		input.ClientSecret,
		payload.AccessToken,
		stringValue(payload.RefreshToken),
	)
	return result, nil
}

func parseExpiresIn(raw json.RawMessage) (time.Duration, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		var text string
		if stringErr := json.Unmarshal(raw, &text); stringErr != nil {
			return 0, false
		}
		parsed, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr != nil {
			return 0, false
		}
		seconds = parsed
	}
	if seconds < 0 || seconds > int64((365*24*time.Hour)/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func capturedResponse(
	response *http.Response,
	raw []byte,
	tooLarge bool,
	secrets ...string,
) *credentialruntime.HTTPResponse {
	body := ""
	if !tooLarge {
		body = scrubResponseBody(raw, secrets...)
	}
	truncated := tooLarge || len(body) > maxCapturedBodyLength
	if len(body) > maxCapturedBodyLength {
		body = body[:maxCapturedBodyLength]
		for !utf8.ValidString(body) && len(body) > 0 {
			body = body[:len(body)-1]
		}
	}
	return &credentialruntime.HTTPResponse{
		Body:          body,
		BodyTruncated: truncated,
		ContentType:   response.Header.Get("Content-Type"),
		StatusCode:    response.StatusCode,
	}
}

func scrubResponseBody(raw []byte, secrets ...string) string {
	var value any
	if len(raw) != 0 && json.Unmarshal(raw, &value) == nil {
		scrubJSONValue(value)
		if encoded, err := json.Marshal(value); err == nil {
			raw = encoded
		}
	}
	body := strings.ToValidUTF8(string(raw), "�")
	for _, secret := range secrets {
		if secret != "" {
			body = strings.ReplaceAll(body, secret, "[REDACTED]")
		}
	}
	return body
}

func scrubJSONValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "password") || lower == "authorization" {
				typed[key] = "[REDACTED]"
				continue
			}
			scrubJSONValue(child)
		}
	case []any:
		for _, child := range typed {
			scrubJSONValue(child)
		}
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
