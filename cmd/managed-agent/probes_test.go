package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/health"
	"github.com/yanpgwang/managed-agent-go/internal/obs"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestStartHealthListener_WorkerIsObservable proves the orchestrate (worker)
// role now answers liveness and readiness. Before this the worker exposed no
// HTTP surface at all, so an orchestrator could not distinguish "process alive"
// from "dependencies usable" and Compose could only gate on the API.
func TestStartHealthListener_WorkerIsObservable(t *testing.T) {
	checker := health.NewChecker(health.Config{CacheTTL: -1},
		health.Probe{Name: "postgres", Check: func(context.Context) error { return nil }},
	)
	server, addr, err := startHealthListener("127.0.0.1:0", checker, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	resp := getWithRetry(t, "http://"+addr+"/healthz")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("worker /healthz = %d, want 200", resp.StatusCode)
	}

	ready, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ready.Body.Close() }()
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("worker /readyz = %d, want 200 with healthy dependencies", ready.StatusCode)
	}
}

// TestStartHealthListener_ReadinessFailsClosed proves the worker listener
// reports a dependency outage rather than a blanket 200.
func TestStartHealthListener_ReadinessFailsClosed(t *testing.T) {
	checker := health.NewChecker(health.Config{CacheTTL: -1},
		health.Probe{Name: "postgres", Check: func(context.Context) error { return nil }},
		health.Probe{Name: "temporal", Check: func(context.Context) error {
			return errors.New("frontend unavailable")
		}},
	)
	server, addr, err := startHealthListener("127.0.0.1:0", checker, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	live := getWithRetry(t, "http://"+addr+"/healthz")
	defer func() { _ = live.Body.Close() }()
	if live.StatusCode != http.StatusOK {
		t.Fatalf("worker /healthz = %d, want 200 while a dependency is down", live.StatusCode)
	}

	resp, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("worker /readyz = %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["dependency"] != "temporal" {
		t.Fatalf("worker /readyz dependency = %q, want temporal", body["dependency"])
	}
}

// TestDefaultWorkerHealthAddr_BindsLoopback mirrors the serve default: the
// worker health listener must not be exposed on all interfaces implicitly.
func TestDefaultWorkerHealthAddr_BindsLoopback(t *testing.T) {
	if defaultWorkerHealthAddr != "127.0.0.1:8081" {
		t.Fatalf("defaultWorkerHealthAddr = %q; want 127.0.0.1:8081", defaultWorkerHealthAddr)
	}
}

func TestNewReadinessChecker_RejectsInvalidConfiguration(t *testing.T) {
	t.Setenv(envHealthTimeout, "soon")
	if _, err := newReadinessChecker(); err == nil {
		t.Fatalf("newReadinessChecker accepted an invalid %s", envHealthTimeout)
	}
	t.Setenv(envHealthTimeout, "2s")
	t.Setenv(envHealthCacheTTL, "often")
	if _, err := newReadinessChecker(); err == nil {
		t.Fatalf("newReadinessChecker accepted an invalid %s", envHealthCacheTTL)
	}
	t.Setenv(envHealthCacheTTL, "1s")
	if _, err := newReadinessChecker(); err != nil {
		t.Fatalf("newReadinessChecker rejected valid configuration: %v", err)
	}
}

func TestConfigureLogging_ValidatesEnvironment(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	t.Setenv(envLogFormatName, "logfmt")
	if _, err := configureLogging("serve"); err == nil {
		t.Fatalf("configureLogging accepted an invalid %s", envLogFormatName)
	}
	t.Setenv(envLogFormatName, "json")
	t.Setenv(envLogLevelName, "loud")
	if _, err := configureLogging("serve"); err == nil {
		t.Fatalf("configureLogging accepted an invalid %s", envLogLevelName)
	}
	t.Setenv(envLogLevelName, "debug")
	logger, err := configureLogging("serve")
	if err != nil {
		t.Fatalf("configureLogging rejected valid configuration: %v", err)
	}
	if logger == nil {
		t.Fatal("configureLogging returned a nil logger")
	}
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatalf("%s=debug did not lower the handler level", envLogLevelName)
	}
}

func TestEnvLogParsers_DefaultToTextInfo(t *testing.T) {
	t.Setenv(envLogFormatName, "")
	t.Setenv(envLogLevelName, "")
	format, err := envLogFormat(envLogFormatName)
	if err != nil {
		t.Fatal(err)
	}
	if format != obs.FormatText {
		t.Fatalf("default log format = %q, want %q", format, obs.FormatText)
	}
	level, err := envLogLevel(envLogLevelName)
	if err != nil {
		t.Fatal(err)
	}
	if level != slog.LevelInfo {
		t.Fatalf("default log level = %v, want info", level)
	}
}

// getWithRetry tolerates the brief window between Listen returning and Serve
// accepting connections.
func getWithRetry(t *testing.T, url string) *http.Response {
	t.Helper()
	var lastErr error
	for range 50 {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("GET %s never succeeded: %v", url, lastErr)
	return nil
}
