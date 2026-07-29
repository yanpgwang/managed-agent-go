package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/httpapi"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

// defaultAddr binds to loopback by default so a fresh `serve` never exposes the
// unauthenticated API on all interfaces. Operators who want a public bind must
// pass -addr explicitly (e.g. -addr :8080).
const defaultAddr = "127.0.0.1:8080"

// unsafeLocalSandboxEnv, when set to "1", permits running a real
// (network-backed) model against the local, non-isolating sandbox. See
// guardModelSandbox.
const unsafeLocalSandboxEnv = "MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX"

// resolveRuntime returns the self-hosted agent core and whether it is backed by
// a real (network-backed) model. It uses the real Messages API client when
// MANAGED_AGENT_MODEL_BASE_URL and MANAGED_AGENT_MODEL_API_KEY are set,
// otherwise the offline deterministic fake so the binary runs (and tests stay
// offline) with no configuration.
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

func resolveRuntime() (rt agentruntime.AgentRuntime, realModel bool, err error) {
	client, realModel, err := resolveModelClient()
	if err != nil {
		return nil, false, err
	}
	return agentruntime.NewAgentCore(client, domain.NewRandomIDGen()), realModel, nil
}

// resolveSandboxProvider selects the sandbox backend from the environment and
// reports whether the selection is the local (non-isolating) provider. The
// default is the offline, dev-grade local provider so the binary runs (and
// tests stay offline) with no configuration. Set MANAGED_AGENT_SANDBOX=docker
// to opt into the Docker-backed provider, which gives real isolation (Linux
// namespaces/cgroups + --network none). MANAGED_AGENT_SANDBOX_IMAGE overrides
// the container image (NewDockerProvider defaults to alpine:latest when empty).
func resolveSandboxProvider() (p sandbox.Provider, isLocal bool, err error) {
	if os.Getenv("MANAGED_AGENT_SANDBOX") == "docker" {
		dp, err := sandbox.NewDockerProvider(sandbox.DockerConfig{
			DefaultImage: os.Getenv("MANAGED_AGENT_SANDBOX_IMAGE"),
		})
		if err != nil {
			return nil, false, err
		}
		log.Printf("sandbox: docker provider (real isolation)")
		return dp, false, nil
	}
	log.Printf("sandbox: local provider (dev-grade guardrail, not a security boundary)")
	return sandbox.NewLocalProvider(), true, nil
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

func buildHandler(db *store.DB, cfg httpapi.Config, rt agentruntime.AgentRuntime, sbx sandbox.Provider) (http.Handler, *app.SessionService) {
	ids := domain.NewRandomIDGen()
	clk := realClock{}
	hub := app.NewHub(256)
	es := app.NewEventService(store.NewEventStore(db, ids, clk), hub)
	agents := app.NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := app.NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := app.NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db),
		store.NewEnvironmentRepo(db), es, store.NewRunStore(db, ids, clk),
		rt, sbx, ids, clk)
	srv := httpapi.NewServer(httpapi.Deps{
		Agents: agents, Envs: envs, Sessions: sessions, Events: es, Hub: hub,
	}, cfg)
	return srv.Handler(), sessions
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
	dbPath := fs.String("db", "managed-agent.db", "sqlite path")
	strict := fs.Bool("strict", false, "require Claude API wire headers (auth, version, beta, content-type) to be present and valid; this is header validation, NOT authentication")
	_ = fs.Parse(os.Args[2:])

	// resolveRuntime selects the real Messages-API-backed agent core when the
	// model env is configured, otherwise the offline deterministic fake.
	rt, realModel, err := resolveRuntime()
	if err != nil {
		log.Fatalf("runtime: %v", err)
	}

	sbx, localSandbox, err := resolveSandboxProvider()
	if err != nil {
		log.Fatalf("sandbox: %v", err)
	}

	// Refuse the unsafe real-model + local-sandbox pairing unless explicitly
	// overridden. The zero-config fake + local default is always allowed.
	allowUnsafe := os.Getenv(unsafeLocalSandboxEnv) == "1"
	if err := guardModelSandbox(realModel, localSandbox, allowUnsafe); err != nil {
		log.Fatalf("startup: %v", err)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	handler, sessions := buildHandler(db, httpapi.Config{
		RequireBeta: *strict, RequireAuth: *strict, RequireVersion: *strict, RequireContentType: *strict,
	}, rt, sbx)
	if err := sessions.Recover(context.Background()); err != nil {
		log.Printf("recovery: %v", err)
	}

	srv := newHTTPServer(*addr, handler)
	go func() {
		log.Printf("listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
