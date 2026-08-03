package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/controlplane"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/httpapi"
	"github.com/yanpgwang/managed-agent-go/internal/live"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// defaultAddr binds to loopback by default so a fresh `serve` never exposes the
// unauthenticated API on all interfaces. Operators who want a public bind must
// pass -addr explicitly (e.g. -addr :8080).
const defaultAddr = "127.0.0.1:8080"

// unsafeLocalSandboxEnv, when set to "1", permits running a real
// (network-backed) model against the local, non-isolating sandbox. See
// guardModelSandbox.
const unsafeLocalSandboxEnv = "MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX"

const (
	sandboxProviderEnv = "MANAGED_AGENT_SANDBOX"
	sandboxImageEnv    = "MANAGED_AGENT_SANDBOX_IMAGE"

	e2bAPIKeyEnv      = "E2B_API_KEY"
	e2bAPIURLEnv      = "E2B_API_URL"
	e2bTemplateEnv    = "E2B_TEMPLATE_ID"
	e2bDomainEnv      = "E2B_DOMAIN"
	e2bIdleTimeoutEnv = "E2B_IDLE_TIMEOUT"

	cubeAPIKeyEnv      = "CUBE_API_KEY"
	cubeAPIURLEnv      = "CUBE_API_URL"
	cubeTemplateEnv    = "CUBE_TEMPLATE_ID"
	cubeDomainEnv      = "CUBE_SANDBOX_DOMAIN"
	cubeProxyNodeIPEnv = "CUBE_PROXY_NODE_IP"
	cubeProxyPortEnv   = "CUBE_PROXY_PORT_HTTP"
	cubeProxySchemeEnv = "CUBE_PROXY_SCHEME"
	cubeIdleTimeoutEnv = "CUBE_IDLE_TIMEOUT"

	openSandboxDomainEnv   = "OPEN_SANDBOX_DOMAIN"
	openSandboxAPIKeyEnv   = "OPEN_SANDBOX_API_KEY"
	openSandboxImageEnv    = "OPEN_SANDBOX_IMAGE"
	openSandboxUseProxyEnv = "OPEN_SANDBOX_USE_SERVER_PROXY"

	daytonaAPIKeyEnv    = "DAYTONA_API_KEY"
	daytonaAPIURLEnv    = "DAYTONA_API_URL"
	daytonaTargetEnv    = "DAYTONA_TARGET"
	daytonaSnapshotEnv  = "DAYTONA_SNAPSHOT"
	daytonaImageEnv     = "DAYTONA_IMAGE"
	daytonaAutoPauseEnv = "DAYTONA_AUTO_PAUSE_MINUTES"
)

// resolveModelClient returns the worker model client and reports whether it is
// backed by a real, network-connected model.
func resolveModelClient() (client model.Client, realModel bool, err error) {
	if client, ok, err := model.AnthropicFromEnv(); err != nil {
		return nil, false, err
	} else if ok {
		log.Printf("runtime: agent core using real Messages API")
		return client, true, nil
	}
	log.Printf("runtime: agent core using offline fake model")
	return model.NewFake(), false, nil
}

// sandboxProviderRegistry declares the adapters compiled into this worker.
// Factories are lazy: selecting local never initializes Docker, and future
// optional remote adapters will not require credentials unless selected.
func sandboxProviderRegistry() (*sandbox.ProviderRegistry, error) {
	return sandbox.NewProviderRegistry(
		sandbox.ProviderRegistration{
			Name: sandbox.LocalProviderName,
			Factory: func() (sandbox.Provider, error) {
				return sandbox.NewLocalProvider(), nil
			},
		},
		sandbox.ProviderRegistration{
			Name: sandbox.DockerProviderName,
			Factory: func() (sandbox.Provider, error) {
				return sandbox.NewDockerProvider(sandbox.DockerConfig{
					DefaultImage: os.Getenv(sandboxImageEnv),
				})
			},
		},
		sandbox.ProviderRegistration{
			Name: sandbox.E2BProviderName,
			Factory: func() (sandbox.Provider, error) {
				idleTimeout, err := envDuration(e2bIdleTimeoutEnv)
				if err != nil {
					return nil, err
				}
				return sandbox.NewE2BProvider(sandbox.E2BConfig{
					APIURL:      os.Getenv(e2bAPIURLEnv),
					APIKey:      os.Getenv(e2bAPIKeyEnv),
					TemplateID:  os.Getenv(e2bTemplateEnv),
					Domain:      os.Getenv(e2bDomainEnv),
					IdleTimeout: idleTimeout,
				})
			},
		},
		sandbox.ProviderRegistration{
			Name: sandbox.CubeProviderName,
			Factory: func() (sandbox.Provider, error) {
				proxyPort, err := envPositiveInt(cubeProxyPortEnv)
				if err != nil {
					return nil, err
				}
				idleTimeout, err := envDuration(cubeIdleTimeoutEnv)
				if err != nil {
					return nil, err
				}
				return sandbox.NewCubeProvider(sandbox.CubeConfig{
					APIURL:      os.Getenv(cubeAPIURLEnv),
					APIKey:      os.Getenv(cubeAPIKeyEnv),
					TemplateID:  os.Getenv(cubeTemplateEnv),
					Domain:      os.Getenv(cubeDomainEnv),
					ProxyNodeIP: os.Getenv(cubeProxyNodeIPEnv),
					ProxyPort:   proxyPort,
					ProxyScheme: os.Getenv(cubeProxySchemeEnv),
					IdleTimeout: idleTimeout,
				})
			},
		},
		sandbox.ProviderRegistration{
			Name: sandbox.OpenSandboxProviderName,
			Factory: func() (sandbox.Provider, error) {
				useProxy, err := envBool(openSandboxUseProxyEnv)
				if err != nil {
					return nil, err
				}
				return sandbox.NewOpenSandboxProvider(sandbox.OpenSandboxConfig{
					BaseURL: os.Getenv(openSandboxDomainEnv),
					APIKey:  os.Getenv(openSandboxAPIKeyEnv),
					Image: firstNonEmpty(
						os.Getenv(openSandboxImageEnv),
						os.Getenv(sandboxImageEnv),
					),
					UseProxy: useProxy,
				})
			},
		},
		sandbox.ProviderRegistration{
			Name: sandbox.DaytonaProviderName,
			Factory: func() (sandbox.Provider, error) {
				autoPauseMinutes, err := envPositiveInt(daytonaAutoPauseEnv)
				if err != nil {
					return nil, err
				}
				return sandbox.NewDaytonaProvider(sandbox.DaytonaConfig{
					APIURL:   os.Getenv(daytonaAPIURLEnv),
					APIKey:   os.Getenv(daytonaAPIKeyEnv),
					Target:   os.Getenv(daytonaTargetEnv),
					Snapshot: os.Getenv(daytonaSnapshotEnv),
					Image: firstNonEmpty(
						os.Getenv(daytonaImageEnv),
						os.Getenv(sandboxImageEnv),
					),
					AutoPauseMinutes: autoPauseMinutes,
				})
			},
		},
	)
}

func envDuration(name string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf(
			"configuration: %s must be a positive Go duration, got %q",
			name,
			value,
		)
	}
	return parsed, nil
}

func envPositiveInt(name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf(
			"configuration: %s must be a positive integer, got %q",
			name,
			value,
		)
	}
	return parsed, nil
}

func envBool(name string) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"configuration: %s must be a boolean, got %q",
			name,
			value,
		)
	}
	return parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolveSandboxProvider selects the process-wide sandbox backend from the
// internal registry and reports whether it is the local non-isolating provider.
// Provider choice is deployment configuration, not part of the Managed Agents
// public API. Unknown names fail closed instead of silently falling back to
// host execution.
func resolveSandboxProvider() (p sandbox.Provider, isLocal bool, err error) {
	name := strings.TrimSpace(os.Getenv(sandboxProviderEnv))
	if name == "" {
		name = sandbox.LocalProviderName
	}
	registry, err := sandboxProviderRegistry()
	if err != nil {
		return nil, false, err
	}
	provider, err := registry.Open(name)
	if err != nil {
		return nil, false, err
	}
	if name == sandbox.LocalProviderName {
		log.Printf("sandbox: local provider (dev-grade guardrail, not a security boundary)")
		return provider, true, nil
	}
	log.Printf("sandbox: %s provider", name)
	return provider, false, nil
}

// guardModelSandbox refuses to start when a real, network-backed model is paired
// with the local sandbox, which is a dev-grade guardrail and not a security
// boundary: a real model can be steered into executing tool commands that run on
// the host with no isolation. The pairing is permitted only when the operator
// explicitly sets MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1. The deterministic
// fake model + local sandbox (the zero-config default) is always allowed, as is
// any model against the Docker sandbox.
func guardModelSandbox(realModel, localSandbox, allowUnsafe bool) error {
	if realModel && localSandbox && !allowUnsafe {
		return errors.New("refusing to run a real model against the local sandbox: " +
			"the local sandbox is a dev-grade guardrail, not a security boundary, and a " +
			"real model can run tool commands on the host with no isolation. " +
			"Set MANAGED_AGENT_SANDBOX=docker for real isolation, or set " +
			unsafeLocalSandboxEnv + "=1 to override this check at your own risk")
	}
	return nil
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// newHTTPServer builds the serving http.Server with conservative connection
// bounds. ReadHeaderTimeout guards against slow-header (Slowloris) clients,
// IdleTimeout closes idle keep-alive connections, and MaxHeaderBytes caps
// header size. There is deliberately NO global WriteTimeout: it would abort the
// long-lived SSE event stream (GET /v1/sessions/{id}/events/stream with
// text/event-stream), so per-response deadlines belong at the handler layer.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: managed-agent <serve|orchestrate> [flags]")
	}
	switch os.Args[1] {
	case "serve":
		runServe()
	case "orchestrate":
		runOrchestrate()
	default:
		log.Fatal("usage: managed-agent <serve|orchestrate> [flags]")
	}
}

func runServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", defaultAddr, "listen address (default binds to loopback; use e.g. :8080 to expose on all interfaces)")
	strict := fs.Bool("strict", false, "require the Claude API wire headers (version, beta, content-type) to be present and valid; authentication is configured separately with "+envAPIKeys)
	_ = fs.Parse(os.Args[2:])

	cfg, warning, err := apiConfigFromEnv(*strict)
	if err != nil {
		log.Fatalf("serve: %v", err)
	}
	if warning != "" {
		log.Print(warning)
	} else {
		// Key ids are non-secret labels; key material is never logged.
		log.Printf("serve: API key authentication enabled (%d key id(s): %s)",
			cfg.APIKeys.Len(), strings.Join(cfg.APIKeys.IDs(), ", "))
		if cfg.AllowAuthorizationHeader {
			log.Printf("serve: accepting `authorization: Bearer` as well as x-api-key " +
				"(non-upstream extension)")
		}
	}
	shutdownTimeout, err := envDurationOr(envShutdownTimeout, defaultShutdownTimeout)
	if err != nil {
		log.Fatalf("serve: %v", err)
	}
	runPostgresAPI(*addr, cfg, shutdownTimeout)
}

// apiConfigFromEnv resolves the HTTP wire configuration.
//
// Authentication is driven by configured key material, not by -strict: a flag
// cannot conjure credentials, and header presence is not authentication. Three
// outcomes are possible:
//
//   - keys configured: authentication is enforced, with or without -strict.
//   - no keys and no -strict: the zero-config local development path. Auth is
//     disabled and a warning is returned for the caller to log.
//   - no keys with -strict: an error. -strict is an explicit request for
//     production-shaped behavior, and failing closed beats serving an
//     unauthenticated API that looks hardened.
func apiConfigFromEnv(strict bool) (cfg httpapi.Config, warning string, err error) {
	apiKeys, err := httpapi.ParseAPIKeys(os.Getenv(envAPIKeys))
	if err != nil {
		return httpapi.Config{}, "", err
	}
	allowAuthorizationHeader, err := envBool(envAllowAuthorizationHeader)
	if err != nil {
		return httpapi.Config{}, "", err
	}
	cfg = httpapi.Config{
		RequireBeta:              strict,
		RequireVersion:           strict,
		RequireContentType:       strict,
		RequireAuth:              apiKeys.Len() > 0,
		APIKeys:                  apiKeys,
		AllowAuthorizationHeader: allowAuthorizationHeader,
	}
	if cfg.RequireAuth {
		return cfg, "", nil
	}
	if strict {
		return httpapi.Config{}, "", fmt.Errorf(
			"-strict requires API keys; set %s to a list of \"<key-id>:<secret>\" entries, "+
				"or drop -strict for local development", envAPIKeys)
	}
	return cfg, fmt.Sprintf(
		"serve: WARNING authentication is DISABLED: %s is not set, so every request is "+
			"served unauthenticated. This is the local development default; set %s before "+
			"binding anything other than loopback.", envAPIKeys, envAPIKeys), nil
}

func runPostgresAPI(addr string, cfg httpapi.Config, shutdownTimeout time.Duration) {
	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		log.Fatalf("serve: %s is required", envDatabaseURL)
	}
	poolCfg, err := poolConfigFromEnv()
	if err != nil {
		log.Fatalf("serve: %v", err)
	}
	ctx := context.Background()
	pool, err := pg.Pool(ctx, databaseURL, poolCfg)
	if err != nil {
		log.Fatalf("serve: postgres: %v", err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		log.Fatalf("serve: migrate: %v", err)
	}

	ids := domain.NewRandomIDGen()
	clock := realClock{}
	pgStore := pg.NewStore(pool, ids, clock)
	broker, err := live.Connect(os.Getenv(envNATSURL))
	if err != nil {
		log.Fatalf("serve: nats: %v", err)
	}
	defer broker.Close()
	pgStore.SetEventNotifier(broker)
	agentsRepo := pg.NewAgentRepository(pgStore)
	environmentsRepo := pg.NewEnvironmentRepository(pgStore)
	agents := app.NewAgentService(agentsRepo, ids, clock)
	environments := app.NewEnvironmentService(environmentsRepo, ids, clock)

	temporalClient, err := temporalpkg.Dial(temporalpkg.ClientConfig{
		HostPort:  os.Getenv(envTemporalHostPort),
		Namespace: os.Getenv(envTemporalNamespace),
	})
	if err != nil {
		log.Fatalf("serve: temporal: %v", err)
	}
	defer temporalClient.Close()
	// Event admission remains correct through a Temporal outage because it
	// commits the PostgreSQL outbox first and treats the direct signal as a
	// best-effort latency path. Lifecycle operations such as physical deletion
	// use the client to stop the Workflow before removing its projection.
	orchestrator := temporalpkg.NewOrchestrator(
		pgStore,
		temporalpkg.NewSignaler(temporalClient),
	)
	sessions := controlplane.NewSessionService(
		pgStore, agentsRepo, environmentsRepo, orchestrator, ids, clock,
	)
	events := controlplane.NewEventService(pgStore)
	stream := live.NewStream(pgStore, broker, ids, clock, 0)
	api := httpapi.NewServer(httpapi.Deps{
		Agents: agents, Envs: environments, Sessions: sessions,
		Events: events, Stream: stream,
	}, cfg)
	log.Printf("serve: PostgreSQL control plane, Temporal client, and NATS live channel connected")
	serveHTTP(addr, api, shutdownTimeout)
}

// serveHTTP runs the API until SIGINT/SIGTERM, then drains.
//
// The drain has two steps. api.BeginShutdown tells long-lived SSE handlers to
// end at a frame boundary, which turns their connections idle; srv.Shutdown then
// waits out ordinary in-flight requests within shutdownTimeout. Without the
// first step an open stream would hold the server non-idle for the whole window
// and then be severed at the deadline.
func serveHTTP(addr string, api *httpapi.Server, shutdownTimeout time.Duration) {
	srv := newHTTPServer(addr, api.Handler())
	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("serve: draining for up to %s", shutdownTimeout)
	api.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("serve: drain incomplete after %s: %v", shutdownTimeout, err)
		return
	}
	log.Printf("serve: drained")
}
