package temporal

import (
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
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

func TestSandboxSpecForSessionRejectsCorruptPackageSnapshot(t *testing.T) {
	_, err := sandboxSpecForSession(domain.Session{EnvironmentConfig: map[string]any{
		"packages": map[string]any{"pip": []any{42}},
	}})
	if err == nil {
		t.Fatal("non-string package entry was accepted")
	}
}
