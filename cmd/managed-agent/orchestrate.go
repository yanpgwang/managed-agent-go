package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// Environment variables for the feature-gated PostgreSQL/Temporal orchestration
// path. The default `serve` command (SQLite dispatcher) does not read them.
const (
	envDatabaseURL       = "MANAGED_AGENT_DATABASE_URL"
	envTemporalHostPort  = "MANAGED_AGENT_TEMPORAL_HOSTPORT"
	envTemporalNamespace = "MANAGED_AGENT_TEMPORAL_NAMESPACE"
)

// runOrchestrate boots the feature-gated Temporal execution plane: it runs
// PostgreSQL migrations, starts the SessionWorkflow worker, and runs the outbox
// relay. This is the first vertical slice's runnable entry point; it does NOT
// serve the HTTP API or cut over the SQLite dispatcher. The two paths coexist by
// design until parity is proven.
//
// It is intentionally a separate subcommand so the default `serve` path is never
// changed by this milestone and cannot regress.
func runOrchestrate() {
	databaseURL := os.Getenv(envDatabaseURL)
	if databaseURL == "" {
		log.Fatalf("orchestrate: %s is required (feature-gated PostgreSQL/Temporal path)", envDatabaseURL)
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

	// The agent runtime is shared with the SQLite path: same AgentCore, same
	// model selection. A tool-using user.message routes a built-in step through
	// the RunTurn Activity under the durable journal; a plain message runs one
	// model round. The offline fake model is sufficient with no configuration.
	rt, realModel, err := resolveRuntime()
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

	runtime := temporalpkg.NewRuntime(client, store, rt, provider, ids, temporalpkg.RelayConfig{})

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
