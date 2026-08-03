package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	"github.com/yanpgwang/managed-agent-go/internal/obs"
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
		slog.Info("agent core model selected",
			slog.String("component", "runtime"),
			slog.String("model_client", "messages_api"),
		)
		return client, true, nil
	}
	slog.Info("agent core model selected",
		slog.String("component", "runtime"),
		slog.String("model_client", "offline_fake"),
	)
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

// envLogFormat parses the handler selection. Text is the development default;
// production deployments set json so records are machine-parsable.
func envLogFormat(name string) (string, error) {
	format, err := obs.ParseFormat(os.Getenv(name))
	if err != nil {
		return "", fmt.Errorf("configuration: %s: %w", name, err)
	}
	return format, nil
}

func envLogLevel(name string) (slog.Level, error) {
	level, err := obs.ParseLevel(os.Getenv(name))
	if err != nil {
		return 0, fmt.Errorf("configuration: %s: %w", name, err)
	}
	return level, nil
}

// configureLogging installs the process-wide structured logger before any
// component that logs is constructed. Every record carries the process role so
// a shared sink can separate API from worker output.
func configureLogging(role string) (*slog.Logger, error) {
	format, err := envLogFormat(envLogFormatName)
	if err != nil {
		return nil, err
	}
	level, err := envLogLevel(envLogLevelName)
	if err != nil {
		return nil, err
	}
	return obs.Configure(os.Stderr, obs.Options{
		Format: format, Level: level, Role: role,
	})
}

// fatal reports an unrecoverable startup or runtime condition and exits
// non-zero, replacing log.Fatalf while keeping the structured record shape.
func fatal(logger *slog.Logger, msg string, attrs ...any) {
	logger.Error(msg, attrs...)
	os.Exit(1)
}

// errAttr keeps error formatting consistent across call sites. Errors reaching
// here are already sanitized by their producing package (see
// internal/model.APIError), so no credential material is rendered.
func errAttr(err error) slog.Attr {
	return slog.String("error", err.Error())
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
		slog.Warn("sandbox provider selected",
			slog.String("component", "sandbox"),
			slog.String("provider", name),
			slog.String("isolation", "dev-grade guardrail, not a security boundary"),
		)
		return provider, true, nil
	}
	slog.Info("sandbox provider selected",
		slog.String("component", "sandbox"),
		slog.String("provider", name),
	)
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

const usage = "usage: managed-agent <serve|orchestrate> [flags]"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	role := os.Args[1]
	switch role {
	case "serve", "orchestrate":
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	// Logging is configured before any component is constructed so no startup
	// record escapes through the stdlib default logger.
	logger, err := configureLogging(role)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch role {
	case "serve":
		runServe(logger)
	case "orchestrate":
		runOrchestrate(logger)
	}
}

func runServe(logger *slog.Logger) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", defaultAddr, "listen address (default binds to loopback; use e.g. :8080 to expose on all interfaces)")
	strict := fs.Bool("strict", false, "require Claude API wire headers (auth, version, beta, content-type) to be present and valid; this is header validation, NOT authentication")
	_ = fs.Parse(os.Args[2:])

	keepAlive, err := envDuration(envSSEKeepAlive)
	if err != nil {
		fatal(logger, "invalid configuration", errAttr(err))
	}
	cfg := httpapi.Config{
		RequireBeta: *strict, RequireAuth: *strict, RequireVersion: *strict,
		RequireContentType: *strict, SSEKeepAlive: keepAlive,
	}
	runPostgresAPI(logger, *addr, cfg)
}

func runPostgresAPI(logger *slog.Logger, addr string, cfg httpapi.Config) {
	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		fatal(logger, "missing required configuration", slog.String("env", envDatabaseURL))
	}
	ctx := context.Background()
	pool, err := pg.Pool(ctx, databaseURL)
	if err != nil {
		fatal(logger, "postgres connection failed", errAttr(err))
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		fatal(logger, "postgres migration failed", errAttr(err))
	}

	ids := domain.NewRandomIDGen()
	clock := realClock{}
	pgStore := pg.NewStore(pool, ids, clock)
	broker, err := live.Connect(os.Getenv(envNATSURL))
	if err != nil {
		fatal(logger, "NATS connection failed", errAttr(err))
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
		fatal(logger, "temporal connection failed", errAttr(err))
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
	readiness, err := newReadinessChecker(
		postgresProbe(pool),
		temporalProbe(temporalClient),
		natsProbe(broker),
	)
	if err != nil {
		fatal(logger, "invalid configuration", errAttr(err))
	}
	handler := httpapi.NewServer(httpapi.Deps{
		Agents: agents, Envs: environments, Sessions: sessions,
		Events: events, Stream: stream,
		Readiness: readiness, Logger: logger,
	}, cfg).Handler()
	logger.Info("control plane connected",
		slog.String("component", "serve"),
		slog.String("store", "postgres"),
		slog.String("orchestrator", "temporal"),
		slog.String("live_channel", "nats"),
	)
	serveHTTP(logger, addr, handler)
}

func serveHTTP(logger *slog.Logger, addr string, handler http.Handler) {
	srv := newHTTPServer(addr, handler)
	go func() {
		logger.Info("http listener started",
			slog.String("component", "serve"),
			slog.String("addr", addr),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(logger, "http listener failed", errAttr(err))
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down", slog.String("component", "serve"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
