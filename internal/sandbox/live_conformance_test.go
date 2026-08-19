package sandbox_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/sandbox"
	"github.com/yanpgwang/mango/internal/sandbox/sandboxtest"
)

func TestE2BLiveConformance(t *testing.T) {
	requireLive(t, "MANGO_LIVE_E2B")
	runLiveConformance(t, func(t *testing.T) sandbox.Provider {
		provider, err := sandbox.NewE2BProvider(sandbox.E2BConfig{
			APIURL:      os.Getenv("E2B_API_URL"),
			APIKey:      os.Getenv("E2B_API_KEY"),
			TemplateID:  os.Getenv("E2B_TEMPLATE_ID"),
			Domain:      os.Getenv("E2B_DOMAIN"),
			IdleTimeout: 10 * time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	})
}

func TestCubeLiveConformance(t *testing.T) {
	requireLive(t, "MANGO_LIVE_CUBE")
	runLiveConformance(t, func(t *testing.T) sandbox.Provider {
		provider, err := sandbox.NewCubeProvider(sandbox.CubeConfig{
			APIURL:      os.Getenv("CUBE_API_URL"),
			APIKey:      os.Getenv("CUBE_API_KEY"),
			TemplateID:  os.Getenv("CUBE_TEMPLATE_ID"),
			Domain:      os.Getenv("CUBE_SANDBOX_DOMAIN"),
			ProxyNodeIP: os.Getenv("CUBE_PROXY_NODE_IP"),
			ProxyPort:   liveEnvInt("CUBE_PROXY_PORT_HTTP"),
			ProxyScheme: os.Getenv("CUBE_PROXY_SCHEME"),
			IdleTimeout: 10 * time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	})
}

func TestOpenSandboxLiveConformance(t *testing.T) {
	requireLive(t, "MANGO_LIVE_OPENSANDBOX")
	runLiveConformance(t, func(t *testing.T) sandbox.Provider {
		provider, err := sandbox.NewOpenSandboxProvider(
			sandbox.OpenSandboxConfig{
				BaseURL:  os.Getenv("OPEN_SANDBOX_DOMAIN"),
				APIKey:   os.Getenv("OPEN_SANDBOX_API_KEY"),
				Image:    os.Getenv("OPEN_SANDBOX_IMAGE"),
				UseProxy: liveEnvBool("OPEN_SANDBOX_USE_SERVER_PROXY"),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return provider
	})
}

func TestDaytonaLiveConformance(t *testing.T) {
	requireLive(t, "MANGO_LIVE_DAYTONA")
	runLiveConformance(t, func(t *testing.T) sandbox.Provider {
		provider, err := sandbox.NewDaytonaProvider(sandbox.DaytonaConfig{
			APIURL:           os.Getenv("DAYTONA_API_URL"),
			APIKey:           os.Getenv("DAYTONA_API_KEY"),
			Target:           os.Getenv("DAYTONA_TARGET"),
			Snapshot:         os.Getenv("DAYTONA_SNAPSHOT"),
			Image:            os.Getenv("DAYTONA_IMAGE"),
			AutoPauseMinutes: liveEnvInt("DAYTONA_AUTO_PAUSE_MINUTES"),
		})
		if err != nil {
			t.Fatal(err)
		}
		return provider
	})
}

func runLiveConformance(t *testing.T, factory sandboxtest.Factory) {
	t.Helper()
	sandboxtest.Run(t, sandboxtest.Config{
		NewProvider: factory,
		// Provider lifecycle conformance does not require egress. "bridge"
		// avoids requiring an optional policy sidecar in local OpenSandbox
		// installations; provider-specific tests cover network policy mapping.
		Spec:      sandbox.Spec{Timeout: 2 * time.Minute, Network: "bridge"},
		ShellPath: "/bin/sh",
	})
}

func requireLive(t *testing.T, name string) {
	t.Helper()
	if !liveEnvBool(name) {
		t.Skipf("set %s=1 to run provider live conformance", name)
	}
}

func liveEnvBool(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func liveEnvInt(name string) int {
	value := strings.TrimSpace(os.Getenv(name))
	parsed, _ := strconv.Atoi(value)
	return parsed
}
