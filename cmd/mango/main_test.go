package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/sandbox"
)

func TestRetryingSessionResourceReconcilerRecoversAndCaches(t *testing.T) {
	attempts := 0
	reconciler := &retryingSessionResourceReconciler{
		resolve: func(context.Context) (*resolvedFiles, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("temporary object-store outage")
			}
			return &resolvedFiles{}, nil
		},
	}
	if _, err := reconciler.resolveMaterializer(context.Background()); err == nil {
		t.Fatal("first resolve unexpectedly succeeded")
	}
	first, err := reconciler.resolveMaterializer(context.Background())
	if err != nil || first == nil {
		t.Fatalf("second resolve = %v, %v", first, err)
	}
	second, err := reconciler.resolveMaterializer(context.Background())
	if err != nil || second != first {
		t.Fatalf("cached resolve = %v, %v; want %v", second, err, first)
	}
	if attempts != 2 {
		t.Fatalf("resolver attempts = %d, want 2", attempts)
	}
}

// TestResolveSandboxProvider_DefaultsToLocal asserts that, with no
// MANGO_SANDBOX set, resolveSandboxProvider returns the offline
// local provider. localProvider is unexported, so we cannot type-assert;
// instead we smoke-test the observable behavior: provision a sandbox and run
// echo. The local provider does this with a host child process and no docker
// daemon, so success here (offline, no docker) proves the default is local.
func TestResolveSandboxProvider_DefaultsToLocal(t *testing.T) {
	t.Setenv("MANGO_SANDBOX", "")
	p, isLocal, err := resolveSandboxProvider()
	if err != nil {
		t.Fatal(err)
	}
	if !isLocal {
		t.Fatalf("default resolveSandboxProvider isLocal=false; want true")
	}
	_, sb, err := p.Create(
		context.Background(),
		t.Name(),
		sandbox.Spec{Timeout: 5 * time.Second},
	)
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

func TestResolveSandboxProvider_RejectsUnknownSelection(t *testing.T) {
	t.Setenv(sandboxProviderEnv, "dockre")
	_, _, err := resolveSandboxProvider()
	if err == nil {
		t.Fatal("resolveSandboxProvider accepted an unknown provider")
	}
	if !strings.Contains(err.Error(), `unsupported provider "dockre"`) ||
		!strings.Contains(
			err.Error(),
			"available: cube, daytona, docker, e2b, local, opensandbox",
		) {
		t.Fatalf("resolveSandboxProvider error = %q", err)
	}
}

func TestSandboxProviderRegistry_IsLazy(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv(sandboxImageEnv, "unused.invalid/image")
	registry, err := sandboxProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := registry.Open(sandbox.LocalProviderName)
	if err != nil {
		t.Fatalf("opening local initialized optional Docker provider: %v", err)
	}
	if provider.Name() != sandbox.LocalProviderName {
		t.Fatalf("provider name = %q", provider.Name())
	}
}

func TestSandboxProviderRegistry_AdvertisesRuntimeCapabilities(t *testing.T) {
	registry, err := sandboxProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range registry.Names() {
		capabilities, err := registry.Capabilities(name)
		if err != nil {
			t.Fatalf("capabilities for %s: %v", name, err)
		}
		want := name != sandbox.LocalProviderName
		if capabilities.PackageSetup != want {
			t.Errorf("%s PackageSetup = %v, want %v", name, capabilities.PackageSetup, want)
		}
		wantLimitedNetwork := name == sandbox.OpenSandboxProviderName
		if capabilities.LimitedNetwork != wantLimitedNetwork {
			t.Errorf(
				"%s LimitedNetwork = %v, want %v",
				name,
				capabilities.LimitedNetwork,
				wantLimitedNetwork,
			)
		}
		wantSessionOutputs := name == sandbox.DockerProviderName
		if capabilities.SessionOutputs != wantSessionOutputs {
			t.Errorf(
				"%s SessionOutputs = %v, want %v",
				name,
				capabilities.SessionOutputs,
				wantSessionOutputs,
			)
		}
		wantFileResources := name == sandbox.DockerProviderName ||
			name == sandbox.OpenSandboxProviderName ||
			name == sandbox.DaytonaProviderName
		if capabilities.FileResources != wantFileResources {
			t.Errorf(
				"%s FileResources = %v, want %v",
				name,
				capabilities.FileResources,
				wantFileResources,
			)
		}
	}
}

func TestResolveSandboxProvider_RejectsInvalidSelectedProviderConfig(t *testing.T) {
	t.Setenv(sandboxProviderEnv, sandbox.E2BProviderName)
	t.Setenv(e2bAPIKeyEnv, "test-key")
	t.Setenv(e2bIdleTimeoutEnv, "eventually")

	_, _, err := resolveSandboxProvider()
	if err == nil || !strings.Contains(err.Error(), e2bIdleTimeoutEnv) {
		t.Fatalf("resolveSandboxProvider error = %v, want invalid %s", err, e2bIdleTimeoutEnv)
	}
}

func TestSandboxProviderRegistry_DoesNotValidateUnusedProviderConfig(t *testing.T) {
	t.Setenv(sandboxProviderEnv, sandbox.LocalProviderName)
	t.Setenv(e2bIdleTimeoutEnv, "eventually")
	t.Setenv(openSandboxUseProxyEnv, "sometimes")

	provider, _, err := resolveSandboxProvider()
	if err != nil {
		t.Fatalf("unused provider configuration affected local: %v", err)
	}
	if provider.Name() != sandbox.LocalProviderName {
		t.Fatalf("provider = %q, want local", provider.Name())
	}
}

func TestSandboxEnvironmentParsersRejectInvalidValues(t *testing.T) {
	t.Setenv(cubeProxyPortEnv, "many")
	if _, err := envPositiveInt(cubeProxyPortEnv); err == nil {
		t.Fatalf("envPositiveInt accepted invalid %s", cubeProxyPortEnv)
	}

	t.Setenv(openSandboxUseProxyEnv, "sometimes")
	if _, err := envBool(openSandboxUseProxyEnv); err == nil {
		t.Fatalf("envBool accepted invalid %s", openSandboxUseProxyEnv)
	}
}

// TestGuardModelSandbox_Matrix covers the safe-defaults startup guard: a real
// model against the local (non-isolating) sandbox must fail unless the operator
// explicitly opts in via MANGO_ALLOW_UNSAFE_LOCAL_SANDBOX=1. Every other
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
				if !strings.Contains(msg, "MANGO_SANDBOX=docker") {
					t.Errorf("error does not mention MANGO_SANDBOX=docker: %q", msg)
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

// TestResolveModelClient_ReportsRealModelWithEnv proves model selection reports
// realModel=true when both the model base URL and API key are configured. It
// performs no network call: construction does not contact the endpoint.
func TestResolveModelClient_ReportsRealModelWithEnv(t *testing.T) {
	t.Setenv("MANGO_MODEL_BASE_URL", "https://model.invalid")
	t.Setenv("MANGO_MODEL_API_KEY", "sk-test")
	client, realModel, err := resolveModelClient()
	if err != nil {
		t.Fatal(err)
	}
	if !realModel {
		t.Fatalf("resolveModelClient realModel=false with model env configured; want true")
	}
	if client == nil {
		t.Fatal("resolveModelClient returned a nil client")
	}
}

func TestResolveModelClient_UsesFakeWithoutEnv(t *testing.T) {
	t.Setenv("MANGO_MODEL_BASE_URL", "")
	t.Setenv("MANGO_MODEL_API_KEY", "")
	client, realModel, err := resolveModelClient()
	if err != nil {
		t.Fatal(err)
	}
	if realModel {
		t.Fatal("resolveModelClient realModel=true without model configuration")
	}
	if client == nil {
		t.Fatal("resolveModelClient returned a nil fake client")
	}
}
