package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

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

// runOrchestrate boots the Temporal execution role: it runs PostgreSQL
// migrations, starts the SessionWorkflow worker, and runs the outbox relay.
// HTTP is served by a separate `serve` process so API and worker capacity can be
// scaled independently.
func runOrchestrate() {
	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		log.Fatalf("orchestrate: %s is required", envDatabaseURL)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pg.Pool(ctx, databaseURL)
	if err != nil {
		log.Fatalf("orchestrate: postgres: %v", err)
	}
	defer pool.Close()
	if err := pg.Migrate(ctx, pool); err != nil {
		log.Fatalf("orchestrate: migrate: %v", err)
	}
	log.Printf("orchestrate: postgres connected and migrated")

	ids := domain.NewRandomIDGen()
	store := pg.NewStore(pool, ids, realClock{})
	broker, err := live.Connect(os.Getenv(envNATSURL))
	if err != nil {
		log.Fatalf("orchestrate: nats: %v", err)
	}
	defer broker.Close()
	store.SetEventNotifier(broker)
	log.Printf("orchestrate: NATS live channel connected")

	// Workflow executions call the selected model through granular model/tool
	// Activities. The offline fake model needs no configuration.
	modelClient, realModel, err := resolveModelClient()
	if err != nil {
		log.Fatalf("orchestrate: runtime: %v", err)
	}
	provider, localSandbox, err := resolveSandboxProvider()
	if err != nil {
		log.Fatalf("orchestrate: sandbox: %v", err)
	}
	if err := guardModelSandbox(realModel, localSandbox, os.Getenv(unsafeLocalSandboxEnv) == "1"); err != nil {
		log.Fatalf("orchestrate: %v", err)
	}

	client, err := temporalpkg.Dial(temporalpkg.ClientConfig{
		HostPort:  os.Getenv(envTemporalHostPort),
		Namespace: os.Getenv(envTemporalNamespace),
	})
	if err != nil {
		log.Fatalf("orchestrate: temporal: %v", err)
	}
	defer client.Close()
	log.Printf("orchestrate: temporal connected")

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
		log.Fatalf("orchestrate: worker start: %v", err)
	}
	defer runtime.Worker.Stop()
	log.Printf("orchestrate: session worker started on task queue %s", temporalpkg.TaskQueue)

	relayErr := make(chan error, 1)
	go func() { relayErr <- runtime.Relay.Run(ctx) }()
	log.Printf("orchestrate: outbox relay running")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		log.Printf("orchestrate: shutting down")
	case err := <-relayErr:
		if err != nil && ctx.Err() == nil {
			log.Printf("orchestrate: relay stopped: %v", err)
		}
	}
	cancel()
}
