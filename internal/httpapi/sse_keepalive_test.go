package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamEvents_EmitsCommentKeepalivesOnIdleStream proves an idle SSE stream
// keeps producing traffic so a reverse proxy does not silently drop it, and
// that the traffic is a *comment* frame — invisible to conformant SSE parsers
// and to the data:-only shell parsers used in the official documentation.
func TestStreamEvents_EmitsCommentKeepalivesOnIdleStream(t *testing.T) {
	h := newTestHandlerWithConfig(t, Config{SSEKeepAlive: 25 * time.Millisecond})
	ag := createID(t, h, "POST", "/v1/agents", `{"name":"a","model":"claude-opus-4-8"}`)
	env := createID(t, h, "POST", "/v1/environments", `{"name":"e","config":{"type":"cloud"}}`)
	sess := createID(t, h, "POST", "/v1/sessions", `{"agent":"`+ag+`","environment_id":"`+env+`"}`)

	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		ts.URL+"/v1/sessions/"+sess+"/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	// The session is deliberately never driven: everything read here is
	// keepalive traffic on an otherwise silent stream.
	lines := readSSELines(t, resp.Body, 3, 3*time.Second)
	comments := 0
	for _, line := range lines {
		if strings.HasPrefix(line, ":") {
			comments++
		}
	}
	if comments < 2 {
		t.Fatalf("idle stream produced %d comment keepalives in %q, want at least 2",
			comments, lines)
	}
}

// TestStreamEvents_EmitsNoIDOrRetryFrames is a hard contract guard.
//
// The documented Managed Agents SSE stream carries only data: lines. Emitting
// an "id:" line would make a browser EventSource send Last-Event-ID on
// reconnect, advertising a resumption capability the contract does not define
// (the documented recovery is: open a new stream, list history, skip seen
// ids). A "retry:" directive would likewise dictate client reconnect policy
// the contract does not specify. Neither may ever appear — including on the
// keepalive path added for proxy survival.
func TestStreamEvents_EmitsNoIDOrRetryFrames(t *testing.T) {
	h := newTestHandlerWithConfig(t, Config{SSEKeepAlive: 25 * time.Millisecond})
	ag := createID(t, h, "POST", "/v1/agents", `{"name":"a","model":"claude-opus-4-8"}`)
	env := createID(t, h, "POST", "/v1/environments", `{"name":"e","config":{"type":"cloud"}}`)
	sess := createID(t, h, "POST", "/v1/sessions", `{"agent":"`+ag+`","environment_id":"`+env+`"}`)

	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET",
		ts.URL+"/v1/sessions/"+sess+"/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Cover both an idle stream (keepalives) and a driven one (real frames).
	go func() {
		time.Sleep(80 * time.Millisecond)
		do(h, "POST", "/v1/sessions/"+sess+"/events",
			`{"events":[{"type":"user.message","content":[{"type":"text","text":"hello"}]}]}`)
	}()

	lines := readSSELines(t, resp.Body, 12, 3*time.Second)
	var sawData bool
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "id:") {
			t.Fatalf("SSE stream emitted an id: line (%q); Last-Event-ID resumption "+
				"is not part of the Managed Agents contract", line)
		}
		if strings.HasPrefix(lower, "retry:") {
			t.Fatalf("SSE stream emitted a retry: directive (%q); reconnect policy "+
				"is not part of the Managed Agents contract", line)
		}
		if strings.HasPrefix(line, "data: ") {
			sawData = true
		}
	}
	if !sawData {
		t.Fatalf("stream produced no data: frames, so the id:/retry: assertion is "+
			"vacuous; lines = %q", lines)
	}
}

// TestConfig_SSEKeepAliveResolution documents the configuration contract: zero
// means the default interval and a negative value disables keepalives.
func TestConfig_SSEKeepAliveResolution(t *testing.T) {
	if got := (Config{}).sseKeepAlive(); got != defaultSSEKeepAlive {
		t.Fatalf("zero SSEKeepAlive = %v, want %v", got, defaultSSEKeepAlive)
	}
	if got := (Config{SSEKeepAlive: time.Second}).sseKeepAlive(); got != time.Second {
		t.Fatalf("explicit SSEKeepAlive = %v, want 1s", got)
	}
	if got := (Config{SSEKeepAlive: -1}).sseKeepAlive(); got > 0 {
		t.Fatalf("negative SSEKeepAlive = %v, want a non-positive (disabled) value", got)
	}
}

// TestSSEKeepAliveFrameIsAComment pins the exact bytes written on the idle
// path: a bare SSE comment terminated by a blank line.
func TestSSEKeepAliveFrameIsAComment(t *testing.T) {
	if !strings.HasPrefix(sseKeepAliveFrame, ":") {
		t.Fatalf("keepalive frame %q must start with ':' to be an SSE comment",
			sseKeepAliveFrame)
	}
	if !strings.HasSuffix(sseKeepAliveFrame, "\n\n") {
		t.Fatalf("keepalive frame %q must terminate with a blank line",
			sseKeepAliveFrame)
	}
	if strings.Contains(sseKeepAliveFrame, "data:") {
		t.Fatalf("keepalive frame %q must not carry a data field", sseKeepAliveFrame)
	}
}

// readSSELines reads up to max non-empty lines from an SSE body. A stream that
// produces nothing within timeout is itself a failure: both callers depend on
// traffic arriving on an otherwise idle stream.
func readSSELines(t *testing.T, body interface{ Read([]byte) (int, error) }, max int, timeout time.Duration) []string {
	t.Helper()
	done := make(chan []string, 1)
	go func() {
		var lines []string
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) >= max {
				break
			}
		}
		done <- lines
	}()
	select {
	case lines := <-done:
		return lines
	case <-time.After(timeout):
		t.Fatalf("timed out reading SSE lines")
		return nil
	}
}
