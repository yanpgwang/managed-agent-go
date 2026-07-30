package sandbox_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox/sandboxtest"
)

func TestLocalProviderConformance(t *testing.T) {
	sandboxtest.Run(t, sandboxtest.Config{
		NewProvider: func(*testing.T) sandbox.Provider {
			return sandbox.NewLocalProvider()
		},
		Spec: sandbox.Spec{Timeout: 30 * time.Second},
	})
}

func TestDockerProviderConformance(t *testing.T) {
	sandboxtest.Run(t, sandboxtest.Config{
		NewProvider: func(t *testing.T) sandbox.Provider {
			t.Helper()
			if _, err := exec.LookPath("docker"); err != nil {
				t.Skip("docker not installed")
			}
			if err := exec.Command(
				"docker",
				"version",
				"--format",
				"{{.Server.Version}}",
			).Run(); err != nil {
				t.Skip("docker daemon not reachable")
			}
			provider, err := sandbox.NewDockerProvider(sandbox.DockerConfig{
				DefaultImage: "alpine:latest",
			})
			if err != nil {
				t.Fatal(err)
			}
			return provider
		},
		Spec: sandbox.Spec{Timeout: 30 * time.Second},
	})
}
