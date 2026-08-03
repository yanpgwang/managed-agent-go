package main

import (
	"context"
	"log"
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

	poolCfg, err := poolConfigFromEnv()
	if err != nil {
		log.Fatalf("orchestrate: %v", err)
	}
	workerCfg, err := workerConfigFromEnv()
	if err != nil {
		log.Fatalf("orchestrate: %v", err)
	}

	pool, err := pg.Pool(ctx, databaseURL, poolCfg)
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

	runtime := temporalpkg.NewRuntimeWithOptions(
		client,
		store,
		modelClient,
		provider,
		ids,
		temporalpkg.RuntimeOptions{
			Worker:            workerCfg,
			PreviewPublishers: []temporalpkg.PreviewPublisher{broker},
		},
	)

	if err := runtime.Worker.Start(); err != nil {
		log.Fatalf("orchestrate: worker start: %v", err)
	}
	log.Printf("orchestrate: session worker started on task queue %s "+
		"(max %d concurrent Activities, %d concurrent Workflow tasks)",
		temporalpkg.TaskQueue,
		workerCfg.MaxConcurrentActivities,
		workerCfg.MaxConcurrentWorkflowTasks,
	)

	relayErr := make(chan error, 1)
	go func() { relayErr <- runtime.Relay.Run(ctx) }()
	log.Printf("orchestrate: outbox relay running")
	lifecycleErr := make(chan error, 1)
	go func() { lifecycleErr <- runtime.Lifecycle.Run(ctx) }()
	log.Printf("orchestrate: sandbox and deletion lifecycle reconciler running")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		log.Printf("orchestrate: shutting down")
	case err := <-relayErr:
		if err != nil && ctx.Err() == nil {
			log.Printf("orchestrate: relay stopped: %v", err)
		}
	case err := <-lifecycleErr:
		if err != nil && ctx.Err() == nil {
			log.Printf("orchestrate: lifecycle reconciler stopped: %v", err)
		}
	}
	// Drain before anything an in-flight Activity still depends on goes away:
	// the relay and lifecycle contexts are cancelled below, and the deferred
	// Temporal client, NATS connection, and PostgreSQL pool close after that.
	limit := workerCfg.DrainTimeout + drainGraceMargin
	log.Printf("orchestrate: draining in-flight Activities for up to %s", workerCfg.DrainTimeout)
	if drainWorker(runtime.Worker, limit) {
		log.Printf("orchestrate: worker drained")
	} else {
		log.Printf("orchestrate: worker drain exceeded %s; exiting with Activities still in "+
			"flight (Temporal will retry them on another worker)", limit)
	}
	cancel()
}

// workerStopper is the part of temporal worker.Worker that shutdown needs.
type workerStopper interface{ Stop() }

// drainWorker stops the Temporal worker and waits for in-flight Activities to
// finish, reporting whether the drain completed inside limit.
//
// worker.Stop already waits up to WorkerConfig.DrainTimeout (the SDK's
// WorkerStopTimeout) before cancelling Activity contexts, but it offers no
// upper bound of its own: a wedged Activity that ignores cancellation would
// block process exit forever. Callers pass the SDK drain plus drainGraceMargin
// so the SDK's own drain stays the primary mechanism while the process is still
// guaranteed to exit.
func drainWorker(w workerStopper, limit time.Duration) bool {
	if limit <= 0 {
		limit = temporalpkg.DefaultWorkerDrainTimeout
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Stop()
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// drainGraceMargin is the slack allowed on top of the SDK's own stop timeout so
// a drain that finishes exactly at the limit is not reported as a failure.
const drainGraceMargin = 5 * time.Second
