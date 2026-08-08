package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/live"
	"github.com/yanpgwang/managed-agent-go/internal/mcpclient"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

type unavailableSessionResourceReconciler struct {
	store  *pg.Store
	cause  error
	memory *app.SessionMemoryMaterializer
}

type retryingSessionResourceReconciler struct {
	store   *pg.Store
	resolve func(context.Context) (*resolvedFiles, error)

	mu           sync.Mutex
	materializer *app.SessionRuntimeMaterializer
	memory       *app.SessionMemoryMaterializer
}

func (r *retryingSessionResourceReconciler) resolveMaterializer(
	ctx context.Context,
) (*app.SessionRuntimeMaterializer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.materializer != nil {
		return r.materializer, nil
	}
	runtime, err := r.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		return nil, sandbox.Permanent(errors.New(
			fileS3BucketEnv + " is not configured",
		))
	}
	r.materializer = app.NewSessionRuntimeMaterializer(
		app.NewSessionResourceMaterializer(
			r.store, runtime.repository, runtime.blobs,
		),
		app.NewSessionSkillMaterializer(r.store, runtime.blobs),
		r.memory,
	)
	return r.materializer, nil
}

func (r *retryingSessionResourceReconciler) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	resources, err := r.store.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	skills, err := r.store.SessionSkillsForRuntime(ctx, sessionID)
	if err != nil || len(resources) == 0 && len(skills) == 0 {
		return err
	}
	needsObjectStore := len(skills) > 0
	for _, resource := range resources {
		if resource.Type() == domain.SessionResourceTypeFile {
			needsObjectStore = true
			break
		}
	}
	if !needsObjectStore {
		if r.memory == nil {
			return nil
		}
		return r.memory.Reconcile(ctx, sessionID, box)
	}
	materializer, err := r.resolveMaterializer(ctx)
	if err != nil {
		return err
	}
	return materializer.Reconcile(ctx, sessionID, box)
}

func (r *retryingSessionResourceReconciler) Writeback(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if r.memory == nil {
		return nil
	}
	return r.memory.Writeback(ctx, sessionID, box)
}

func (r *retryingSessionResourceReconciler) WritebackForRelease(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if r.memory == nil {
		return nil
	}
	return r.memory.WritebackForRelease(ctx, sessionID, box)
}

func (r *retryingSessionResourceReconciler) MemoryStoreMountsForRelease(
	ctx context.Context,
	sessionID string,
) ([]sandbox.MemoryStoreMount, error) {
	if r.memory == nil {
		return nil, nil
	}
	return r.memory.MemoryStoreMountsForRelease(ctx, sessionID)
}

func (r *retryingSessionResourceReconciler) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	resources, err := r.store.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil || len(resources) == 0 {
		return err
	}
	needsObjectStore := false
	for _, resource := range resources {
		if resource.Type() == domain.SessionResourceTypeFile {
			needsObjectStore = true
			break
		}
	}
	if !needsObjectStore {
		if r.memory == nil {
			return nil
		}
		return r.memory.CleanupSession(ctx, sessionID)
	}
	materializer, err := r.resolveMaterializer(ctx)
	if err != nil {
		return err
	}
	return materializer.CleanupSession(ctx, sessionID)
}

func (r unavailableSessionResourceReconciler) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	resources, err := r.store.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	skills, err := r.store.SessionSkillsForRuntime(ctx, sessionID)
	if err != nil || len(resources) == 0 && len(skills) == 0 {
		return err
	}
	if r.memory != nil {
		if err := r.memory.Reconcile(ctx, sessionID, box); err != nil {
			return err
		}
	}
	needsObjectStore := len(skills) > 0
	for _, resource := range resources {
		if resource.Type() == domain.SessionResourceTypeFile {
			needsObjectStore = true
			break
		}
	}
	if !needsObjectStore {
		return nil
	}
	return sandbox.Permanent(fmt.Errorf(
		"session File Resources or custom Skills are unavailable on this worker: %w",
		r.cause,
	))
}

func (r unavailableSessionResourceReconciler) Writeback(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if r.memory == nil {
		return nil
	}
	return r.memory.Writeback(ctx, sessionID, box)
}

func (r unavailableSessionResourceReconciler) WritebackForRelease(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if r.memory == nil {
		return nil
	}
	return r.memory.WritebackForRelease(ctx, sessionID, box)
}

func (r unavailableSessionResourceReconciler) MemoryStoreMountsForRelease(
	ctx context.Context,
	sessionID string,
) ([]sandbox.MemoryStoreMount, error) {
	if r.memory == nil {
		return nil, nil
	}
	return r.memory.MemoryStoreMountsForRelease(ctx, sessionID)
}

func (r unavailableSessionResourceReconciler) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	resources, err := r.store.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil || len(resources) == 0 {
		return err
	}
	if r.memory != nil {
		if err := r.memory.CleanupSession(ctx, sessionID); err != nil {
			return err
		}
	}
	for _, resource := range resources {
		if resource.Type() == domain.SessionResourceTypeFile {
			return fmt.Errorf(
				"session File Resources are unavailable on this worker: %w",
				r.cause,
			)
		}
	}
	return nil
}

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
	vaults, err := resolveVaultService(store, ids, realClock{})
	if err != nil {
		log.Fatalf("orchestrate: Vault runtime keyring: %v", err)
	}
	var mcpAuth mcpclient.AuthSource
	if vaults == nil {
		mcpAuth = app.NewUnavailableVaultAuthSource(pg.NewVaultRepository(store))
		log.Printf("orchestrate: Vault-backed MCP authentication disabled; %s is not configured", vaultKeyringFileEnv)
	} else {
		mcpAuth = vaults
		log.Printf("orchestrate: Vault-backed MCP authentication enabled")
	}
	memory := app.NewMemoryService(pg.NewMemoryRepository(store), ids, realClock{})
	memoryMaterializer := app.NewSessionMemoryMaterializer(store, memory)
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
	fileRuntime, err := resolveFiles(ctx, store, ids, realClock{}, false)
	var resourceReconciler temporalpkg.SandboxResourceReconciler
	switch {
	case err != nil:
		log.Printf("orchestrate: object store unavailable; File/Skill turns will retry connection: %v", err)
		resourceReconciler = &retryingSessionResourceReconciler{
			store:  store,
			memory: memoryMaterializer,
			resolve: func(resolveCtx context.Context) (*resolvedFiles, error) {
				return resolveFiles(resolveCtx, store, ids, realClock{}, false)
			},
		}
	case fileRuntime == nil:
		cause := errors.New(fileS3BucketEnv + " is not configured")
		resourceReconciler = unavailableSessionResourceReconciler{
			store: store, cause: cause, memory: memoryMaterializer,
		}
		log.Printf("orchestrate: Session File Resources and custom Skill runtime disabled: %v", cause)
	default:
		resourceReconciler = app.NewSessionRuntimeMaterializer(
			app.NewSessionResourceMaterializer(
				store, fileRuntime.repository, fileRuntime.blobs,
			),
			app.NewSessionSkillMaterializer(store, fileRuntime.blobs),
			memoryMaterializer,
		)
		log.Printf("orchestrate: Session File Resource and custom Skill materializers enabled")
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

	runtime := temporalpkg.NewRuntimeWithResourcesAndMCPAuth(
		client,
		store,
		modelClient,
		provider,
		ids,
		temporalpkg.RelayConfig{},
		resourceReconciler,
		mcpAuth,
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
	cancel()
}
