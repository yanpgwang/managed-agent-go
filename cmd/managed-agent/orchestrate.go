package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/live"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// Environment variables shared by the PostgreSQL HTTP control plane and the
// Temporal execution worker.
const (
	envDatabaseURL       = "MANAGED_AGENT_DATABASE_URL"
	envTemporalHostPort  = "MANAGED_AGENT_TEMPORAL_HOSTPORT"
	envTemporalNamespace = "MANAGED_AGENT_TEMPORAL_NAMESPACE"
	envNATSURL           = "MANAGED_AGENT_NATS_URL"
)

// Observability configuration. These are local operational settings with no
// Claude Managed Agents wire contract.
const (
	envLogFormatName = "MANAGED_AGENT_LOG_FORMAT"
	envLogLevelName  = "MANAGED_AGENT_LOG_LEVEL"
	envSSEKeepAlive  = "MANAGED_AGENT_SSE_KEEPALIVE_INTERVAL"
)

// runOrchestrate boots the Temporal execution role: it runs PostgreSQL
// migrations, starts the SessionWorkflow worker, and runs the outbox relay.
// The CMA HTTP surface is served by a separate `serve` process so API and
// worker capacity can be scaled independently; the worker exposes only a
// health/readiness listener so an orchestrator can observe it.
func runOrchestrate(logger *slog.Logger) {
	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		fatal(logger, "missing required configuration", slog.String("env", envDatabaseURL))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pg.Pool(ctx, databaseURL)
	if err != nil {
		fatal(logger, "postgres connection failed", errAttr(err))
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		fatal(logger, "postgres migration failed", errAttr(err))
	}
	logger.Info("postgres connected and migrated", slog.String("component", "orchestrate"))

	ids := domain.NewRandomIDGen()
	store := pg.NewStore(pool, ids, realClock{})
	broker, err := live.Connect(os.Getenv(envNATSURL))
	if err != nil {
		fatal(logger, "NATS connection failed", errAttr(err))
	}
	defer broker.Close()
	store.SetEventNotifier(broker)
	logger.Info("NATS live channel connected", slog.String("component", "orchestrate"))

	// Workflow executions call the selected model through granular model/tool
	// Activities. The offline fake model needs no configuration.
	modelClient, realModel, err := resolveModelClient()
	if err != nil {
		fatal(logger, "model client configuration failed", errAttr(err))
	}
	provider, localSandbox, err := resolveSandboxProvider()
	if err != nil {
		fatal(logger, "sandbox provider configuration failed", errAttr(err))
	}
	if err := guardModelSandbox(realModel, localSandbox, os.Getenv(unsafeLocalSandboxEnv) == "1"); err != nil {
		fatal(logger, "unsafe model and sandbox pairing refused", errAttr(err))
	}

	client, err := temporalpkg.Dial(temporalpkg.ClientConfig{
		HostPort:  os.Getenv(envTemporalHostPort),
		Namespace: os.Getenv(envTemporalNamespace),
	})
	if err != nil {
		fatal(logger, "temporal connection failed", errAttr(err))
	}
	defer client.Close()
	logger.Info("temporal connected", slog.String("component", "orchestrate"))

	runtime := temporalpkg.NewRuntime(
		client,
		store,
		modelClient,
		provider,
		ids,
		temporalpkg.RelayConfig{},
		broker,
	)

	if err := runtime.Worker.Start(); err != nil {
		fatal(logger, "temporal worker start failed", errAttr(err))
	}
	defer runtime.Worker.Stop()
	logger.Info("session worker started",
		slog.String("component", "orchestrate"),
		slog.String("task_queue", temporalpkg.TaskQueue),
	)

	relayErr := make(chan error, 1)
	go func() { relayErr <- runtime.Relay.Run(ctx) }()
	logger.Info("outbox relay running", slog.String("component", "orchestrate"))
	lifecycleErr := make(chan error, 1)
	go func() { lifecycleErr <- runtime.Lifecycle.Run(ctx) }()
	logger.Info("sandbox and deletion lifecycle reconciler running",
		slog.String("component", "orchestrate"))

	// The worker has no CMA HTTP surface. This listener exists purely so an
	// orchestrator can distinguish "process alive" from "dependencies usable".
	readiness, err := newReadinessChecker(
		postgresProbe(pool),
		temporalProbe(client),
		natsProbe(broker),
	)
	if err != nil {
		fatal(logger, "invalid configuration", errAttr(err))
	}
	healthAddr := firstNonEmpty(os.Getenv(envWorkerHealthAddr), defaultWorkerHealthAddr)
	healthServer, boundAddr, err := startHealthListener(healthAddr, readiness, logger)
	if err != nil {
		fatal(logger, "worker health listener failed to bind",
			slog.String("addr", healthAddr), errAttr(err))
	}
	defer shutdownHealthListener(healthServer)
	logger.Info("worker health listener started",
		slog.String("component", "orchestrate"),
		slog.String("addr", boundAddr),
	)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		logger.Info("shutting down", slog.String("component", "orchestrate"))
	case err := <-relayErr:
		if err != nil && ctx.Err() == nil {
			logger.Error("outbox relay stopped",
				slog.String("component", "orchestrate"), errAttr(err))
		}
	case err := <-lifecycleErr:
		if err != nil && ctx.Err() == nil {
			logger.Error("lifecycle reconciler stopped",
				slog.String("component", "orchestrate"), errAttr(err))
		}
	}
	cancel()
}

func shutdownHealthListener(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
