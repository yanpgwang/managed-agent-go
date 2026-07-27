package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/httpapi"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

// TestResolveSandboxProvider_DefaultsToLocal asserts that, with no
// MANAGED_AGENT_SANDBOX set, resolveSandboxProvider returns the offline
// local provider. localProvider is unexported, so we cannot type-assert;
// instead we smoke-test the observable behavior: provision a sandbox and run
// echo. The local provider does this with a host child process and no docker
// daemon, so success here (offline, no docker) proves the default is local.
func TestResolveSandboxProvider_DefaultsToLocal(t *testing.T) {
	t.Setenv("MANAGED_AGENT_SANDBOX", "")
	p, isLocal, err := resolveSandboxProvider()
	if err != nil {
		t.Fatal(err)
	}
	if !isLocal {
		t.Fatalf("default resolveSandboxProvider isLocal=false; want true")
	}
	sb, err := p.Provision(context.Background(), sandbox.Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("default provider Provision: %v", err)
	}
	defer sb.Destroy(context.Background())
	// The local provider's root is a host temp dir; the Docker provider's root
	// is the constant "/workspace". Asserting on Root() proves the default is
	// local deterministically, independent of whether a Docker daemon is present
	// on the machine running the test.
	if sb.Root() == "/workspace" {
		t.Fatalf("default provider Root()=%q looks like the Docker provider; want local", sb.Root())
	}
	res, err := sb.Exec(context.Background(), sandbox.Command{Path: "/bin/sh", Args: []string{"-c", "echo hello"}})
	if err != nil {
		t.Fatalf("default provider Exec: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != "hello" {
		t.Fatalf("default provider = %+v; want local (echo hello, exit 0)", res)
	}
}

func TestResolveRuntime_UsesFakeModelWithoutEnv(t *testing.T) {
	t.Setenv("MANAGED_AGENT_MODEL_BASE_URL", "")
	t.Setenv("MANAGED_AGENT_MODEL_API_KEY", "")
	rt, realModel, err := resolveRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if realModel {
		t.Fatalf("resolveRuntime realModel=true without model env; want false (offline fake)")
	}
	if _, ok := rt.(*agentruntime.AgentCore); !ok {
		t.Fatalf("runtime = %T, want *agentruntime.AgentCore", rt)
	}
}

// TestGuardModelSandbox_Matrix covers the safe-defaults startup guard: a real
// model against the local (non-isolating) sandbox must fail unless the operator
// explicitly opts in via MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1. Every other
// combination — including the zero-config fake + local default — must start.
func TestGuardModelSandbox_Matrix(t *testing.T) {
	cases := []struct {
		name                            string
		realModel, localSandbox, unsafe bool
		wantErr                         bool
	}{
		{"fake+local (default) allowed", false, true, false, false},
		{"fake+docker allowed", false, false, false, false},
		{"real+docker allowed", true, false, false, false},
		{"real+local blocked", true, true, false, true},
		{"real+local override allowed", true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardModelSandbox(tc.realModel, tc.localSandbox, tc.unsafe)
			if tc.wantErr && err == nil {
				t.Fatalf("guardModelSandbox(%v,%v,%v) = nil; want error", tc.realModel, tc.localSandbox, tc.unsafe)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("guardModelSandbox(%v,%v,%v) = %v; want nil", tc.realModel, tc.localSandbox, tc.unsafe, err)
			}
			if tc.wantErr {
				// The error must guide the operator to both remedies.
				msg := err.Error()
				if !strings.Contains(msg, "MANAGED_AGENT_SANDBOX=docker") {
					t.Errorf("error does not mention MANAGED_AGENT_SANDBOX=docker: %q", msg)
				}
				if !strings.Contains(msg, unsafeLocalSandboxEnv) {
					t.Errorf("error does not mention %s override: %q", unsafeLocalSandboxEnv, msg)
				}
			}
		})
	}
}

// TestDefaultAddr_BindsLoopback asserts the serve default listen address binds
// to loopback so a fresh serve never exposes the unauthenticated API on all
// interfaces.
func TestDefaultAddr_BindsLoopback(t *testing.T) {
	if defaultAddr != "127.0.0.1:8080" {
		t.Fatalf("defaultAddr = %q; want 127.0.0.1:8080", defaultAddr)
	}
}

// TestNewHTTPServer_Timeouts asserts the serving server sets slow-header and
// idle bounds and a header-size cap, and deliberately leaves WriteTimeout unset
// so long-lived SSE streams are not aborted mid-response.
func TestNewHTTPServer_Timeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v; want 10s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v; want 120s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d; want %d", srv.MaxHeaderBytes, 1<<20)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v; want 0 (unset, so SSE streams are not aborted)", srv.WriteTimeout)
	}
}

// TestResolveRuntime_ReportsRealModelWithEnv proves resolveRuntime reports
// realModel=true when both the model base URL and API key are configured. It
// performs no network call: resolveRuntime only constructs the client
// (AnthropicFromEnv → NewAnthropic), which does not contact the endpoint. The
// base URL is a non-routable placeholder to make that guarantee explicit.
func TestResolveRuntime_ReportsRealModelWithEnv(t *testing.T) {
	t.Setenv("MANAGED_AGENT_MODEL_BASE_URL", "https://model.invalid")
	t.Setenv("MANAGED_AGENT_MODEL_API_KEY", "sk-test")
	rt, realModel, err := resolveRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if !realModel {
		t.Fatalf("resolveRuntime realModel=false with model env configured; want true")
	}
	if _, ok := rt.(*agentruntime.AgentCore); !ok {
		t.Fatalf("runtime = %T, want *agentruntime.AgentCore", rt)
	}
}

func TestBuildHandler_Health(t *testing.T) {
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, _ := buildHandler(db, httpapi.Config{}, agentruntime.NewFake(), sandbox.NewLocalProvider())
	ts := httptest.NewServer(h)
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v status=%v", err, resp)
	}
}
