package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yanpgwang/mango/internal/credentialruntime"
	"github.com/yanpgwang/mango/internal/domain"
)

// ValidateBearer performs the same initialize and tools/list handshake used by
// runtime discovery while retaining a bounded, scrubbed projection of the
// response that failed. It never returns the bearer in a result or error.
func (r *Remote) ValidateBearer(
	ctx context.Context,
	mcpServerURL string,
	bearer string,
) (credentialruntime.MCPProbeResult, error) {
	httpClient := cloneHTTPClientWithRedirectGuard(r.http)
	baseTransport := httpClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	capture := &probeCaptureTransport{base: baseTransport, secret: bearer}
	httpClient.Transport = capture
	server := domain.MCPServer{Name: "credential validation", URL: mcpServerURL}
	source := staticBearerSource{token: bearer}

	capture.setMethod("initialize")
	session, err := r.connectWithHTTPClient(ctx, "", server, source, httpClient)
	if err != nil {
		return capture.result(), nil
	}
	defer func() { _ = session.Close() }()

	capture.setMethod("tools/list")
	if _, err := session.ListTools(ctx, &mcp.ListToolsParams{}); err != nil {
		return capture.result(), nil
	}
	return credentialruntime.MCPProbeResult{
		Verdict: credentialruntime.VerdictValid,
	}, nil
}

type staticBearerSource struct{ token string }

func (s staticBearerSource) ResolveMCPBearer(
	context.Context,
	string,
	string,
) (BearerCredential, bool, error) {
	return BearerCredential{Token: s.token}, true, nil
}

const maxProbeResponseBody = 16 << 10

type probeCaptureTransport struct {
	base   http.RoundTripper
	secret string

	mu     sync.Mutex
	method string
	last   *probeCapturedResponse
}

type probeCapturedResponse struct {
	mu          sync.Mutex
	method      string
	statusCode  int
	contentType string
	body        []byte
	truncated   bool
}

func (t *probeCaptureTransport) setMethod(method string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.method = method
	t.last = nil
}

func (t *probeCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	captured := &probeCapturedResponse{
		method: t.method, statusCode: response.StatusCode,
		contentType: response.Header.Get("Content-Type"),
	}
	t.last = captured
	t.mu.Unlock()

	if response.Body == nil {
		return response, nil
	}
	if response.StatusCode >= 400 {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxProbeResponseBody+1))
		_ = response.Body.Close()
		if len(raw) > maxProbeResponseBody {
			captured.mu.Lock()
			captured.truncated = true
			captured.mu.Unlock()
			raw = raw[:maxProbeResponseBody]
		}
		captured.mu.Lock()
		captured.body = append([]byte(nil), raw...)
		captured.mu.Unlock()
		response.Body = io.NopCloser(bytes.NewReader(raw))
		if readErr != nil {
			captured.mu.Lock()
			captured.truncated = true
			captured.mu.Unlock()
		}
		return response, nil
	}
	response.Body = &probeCaptureBody{ReadCloser: response.Body, captured: captured}
	return response, nil
}

type probeCaptureBody struct {
	io.ReadCloser
	captured *probeCapturedResponse
}

func (b *probeCaptureBody) Read(buffer []byte) (int, error) {
	count, err := b.ReadCloser.Read(buffer)
	b.captured.mu.Lock()
	defer b.captured.mu.Unlock()
	remaining := maxProbeResponseBody - len(b.captured.body)
	if remaining > 0 {
		copyCount := count
		if copyCount > remaining {
			copyCount = remaining
		}
		b.captured.body = append(b.captured.body, buffer[:copyCount]...)
	}
	if count > remaining {
		b.captured.truncated = true
	}
	return count, err
}

func (t *probeCaptureTransport) result() credentialruntime.MCPProbeResult {
	t.mu.Lock()
	method := t.method
	captured := t.last
	t.mu.Unlock()
	result := credentialruntime.MCPProbeResult{Verdict: credentialruntime.VerdictUnknown}
	failure := &credentialruntime.MCPProbeFailure{Method: method}
	if captured != nil {
		captured.mu.Lock()
		method := captured.method
		statusCode := captured.statusCode
		contentType := captured.contentType
		bodyBytes := append([]byte(nil), captured.body...)
		truncated := captured.truncated
		captured.mu.Unlock()
		body := ""
		if !truncated {
			body = scrubProbeBody(bodyBytes, t.secret)
		}
		failure.Method = method
		failure.HTTPResponse = &credentialruntime.HTTPResponse{
			Body: body, BodyTruncated: truncated,
			ContentType: contentType, StatusCode: statusCode,
		}
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			result.Verdict = credentialruntime.VerdictInvalid
		}
	}
	result.Failure = failure
	return result
}

func scrubProbeBody(raw []byte, secret string) string {
	var value any
	if len(raw) != 0 && json.Unmarshal(raw, &value) == nil {
		scrubProbeJSON(value)
		if encoded, err := json.Marshal(value); err == nil {
			raw = encoded
		}
	}
	body := strings.ToValidUTF8(string(raw), "�")
	if secret != "" {
		body = strings.ReplaceAll(body, secret, "[REDACTED]")
	}
	if len(body) > maxProbeResponseBody {
		body = body[:maxProbeResponseBody]
		for !utf8.ValidString(body) && len(body) > 0 {
			body = body[:len(body)-1]
		}
	}
	return body
}

func scrubProbeJSON(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "password") || lower == "authorization" {
				typed[key] = "[REDACTED]"
				continue
			}
			scrubProbeJSON(child)
		}
	case []any:
		for _, child := range typed {
			scrubProbeJSON(child)
		}
	}
}
