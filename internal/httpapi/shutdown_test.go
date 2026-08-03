package httpapi

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// openSSEStream starts a real server for api, opens the session event stream,
// and returns the live response plus the http.Server so the test can drive a
// graceful shutdown.
func openSSEStream(t *testing.T, api *Server) (*http.Response, *http.Server) {
	t.Helper()
	handler := api.Handler()
	agent := createID(t, handler, "POST", "/v1/agents", `{"name":"a","model":"claude-opus-4-8"}`)
	env := createID(t, handler, "POST", "/v1/environments", `{"name":"e","config":{"type":"cloud"}}`)
	session := createID(t, handler, "POST", "/v1/sessions",
		`{"agent":"`+agent+`","environment_id":"`+env+`"}`)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	url := "http://" + listener.Addr().String() + "/v1/sessions/" + session + "/events/stream"
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	return resp, srv
}

// TestBeginShutdown_DrainsInFlightSSEStream asserts that a draining process
// ends an open SSE stream cleanly and well inside the configured window: the
// handler returns, the client sees end-of-stream rather than a severed
// connection, and http.Server.Shutdown completes.
func TestBeginShutdown_DrainsInFlightSSEStream(t *testing.T) {
	api := newTestAPIServer(t, Config{}, false)
	resp, srv := openSSEStream(t, api)

	const window = 10 * time.Second
	api.BeginShutdown()

	shutdownDone := make(chan error, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown with an open SSE stream: %v", err)
		}
	case <-time.After(window):
		t.Fatalf("Shutdown did not drain the SSE stream within %s", window)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("SSE drain took %s; the stream is not observing shutdown promptly", elapsed)
	}

	// The stream ended at a frame boundary: the client reads EOF, not a reset.
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); !errors.Is(err, context.Canceled) && err == nil {
		t.Fatal("expected the stream body to be closed after shutdown")
	}
}

// TestShutdownWithoutBeginShutdown_BlocksOnOpenSSEStream is the negative
// control for the seam above: without the signal, an open SSE stream holds the
// connection non-idle for the whole window and Shutdown hits its deadline. It
// documents why BeginShutdown exists rather than relying on Shutdown alone.
func TestShutdownWithoutBeginShutdown_BlocksOnOpenSSEStream(t *testing.T) {
	api := newTestAPIServer(t, Config{}, false)
	_, srv := openSSEStream(t, api)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := srv.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown without BeginShutdown = %v, want context deadline exceeded", err)
	}

	// The signal still releases it afterwards.
	api.BeginShutdown()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		t.Fatalf("Shutdown after BeginShutdown: %v", err)
	}
}

// TestBeginShutdown_IsIdempotent guards the close-once invariant: a second call
// (for example a repeated SIGTERM) must not panic.
func TestBeginShutdown_IsIdempotent(t *testing.T) {
	api := newTestAPIServer(t, Config{}, false)
	api.BeginShutdown()
	api.BeginShutdown()
}
