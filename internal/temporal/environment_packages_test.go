package temporal

import (
	"slices"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestSandboxSpecForSessionMapsEnvironmentPackages(t *testing.T) {
	spec, err := sandboxSpecForSession(domain.Session{EnvironmentConfig: map[string]any{
		"type": "cloud",
		"packages": map[string]any{
			"apt": []any{"git"},
			"go":  []string{"golang.org/x/tools/gopls@v0.20.0"},
			"pip": []any{"httpx==0.28.1"},
		},
	}})
	if err != nil {
		t.Fatalf("build sandbox spec: %v", err)
	}
	if spec.Network != defaultCloudSandboxNetwork || spec.Timeout != sandboxTurnTimeout {
		t.Fatalf("sandbox defaults = %+v", spec)
	}
	if len(spec.Packages.Apt) != 1 || spec.Packages.Apt[0] != "git" ||
		len(spec.Packages.Go) != 1 || spec.Packages.Go[0] != "golang.org/x/tools/gopls@v0.20.0" ||
		len(spec.Packages.Pip) != 1 || spec.Packages.Pip[0] != "httpx==0.28.1" {
		t.Fatalf("package spec = %+v", spec.Packages)
	}
}

func TestSandboxSpecForSessionMapsLimitedNetworking(t *testing.T) {
	spec, err := sandboxSpecForSession(domain.Session{
		EnvironmentConfig: map[string]any{
			"type": "cloud",
			"networking": map[string]any{
				"type":                   "limited",
				"allow_mcp_servers":      true,
				"allow_package_managers": false,
				"allowed_hosts":          []any{"API.Example.com", "*.assets.example.com"},
			},
			"packages": map[string]any{"pip": []any{"httpx==0.28.1"}},
		},
		AgentSnapshot: domain.Agent{MCPServers: []any{map[string]any{
			"type": "url", "name": "tools", "url": "https://mcp.example.com/events",
		}}},
	})
	if err != nil {
		t.Fatalf("build limited sandbox spec: %v", err)
	}
	if spec.Network != "limited" {
		t.Fatalf("sandbox network = %q, want limited", spec.Network)
	}
	wantFinal := []string{"*.assets.example.com", "api.example.com", "mcp.example.com"}
	if !slices.Equal(spec.NetworkAllowedHosts, wantFinal) {
		t.Fatalf("final allowed hosts = %v, want %v", spec.NetworkAllowedHosts, wantFinal)
	}
	for _, host := range append(wantFinal, publicPackageRegistryHosts...) {
		if !slices.Contains(spec.SetupNetworkAllowedHosts, host) {
			t.Errorf("setup allowed hosts omit %q: %v", host, spec.SetupNetworkAllowedHosts)
		}
	}
}

func TestSandboxSpecForSessionAllowsPackageRegistriesAtRuntime(t *testing.T) {
	spec, err := sandboxSpecForSession(domain.Session{EnvironmentConfig: map[string]any{
		"networking": map[string]any{
			"type": "limited", "allow_package_managers": true,
		},
	}})
	if err != nil {
		t.Fatalf("build limited sandbox spec: %v", err)
	}
	if !slices.Equal(spec.NetworkAllowedHosts, publicPackageRegistryHosts) {
		t.Fatalf("runtime package registry hosts = %v", spec.NetworkAllowedHosts)
	}
	if len(spec.SetupNetworkAllowedHosts) != 0 {
		t.Fatalf("empty package plan has setup hosts: %v", spec.SetupNetworkAllowedHosts)
	}
}

func TestSandboxSpecForSessionRejectsCorruptNetworkSnapshot(t *testing.T) {
	for _, networking := range []any{
		"limited",
		map[string]any{"type": "future"},
		map[string]any{"type": "limited", "allow_mcp_servers": "yes"},
		map[string]any{"type": "limited", "allowed_hosts": []any{42}},
	} {
		_, err := sandboxSpecForSession(domain.Session{EnvironmentConfig: map[string]any{
			"networking": networking,
		}})
		if err == nil {
			t.Fatalf("corrupt network snapshot was accepted: %#v", networking)
		}
	}
}

func TestSandboxSpecForSessionRejectsCorruptPackageSnapshot(t *testing.T) {
	_, err := sandboxSpecForSession(domain.Session{EnvironmentConfig: map[string]any{
		"packages": map[string]any{"pip": []any{42}},
	}})
	if err == nil {
		t.Fatal("non-string package entry was accepted")
	}
}
