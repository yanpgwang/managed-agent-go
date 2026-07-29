package temporal

import (
	"context"
	"log"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Orchestrator bridges PostgreSQL admission to Temporal. It makes a best-effort
// post-commit fast-path signal to reduce latency and relies on the durable
// outbox relay for eventual, correct delivery.
type Orchestrator struct {
	store    *pg.Store
	signaler *Signaler
}

func NewOrchestrator(store *pg.Store, signaler *Signaler) *Orchestrator {
	return &Orchestrator{store: store, signaler: signaler}
}

// Admit admits a client event batch into PostgreSQL and returns the committed
// public events. The admission transaction has already written the coalescible
// outbox wakeup, so the returned events are durable and will be processed even
// if the fast-path signal below fails. The fast-path signal is best-effort: a
// failure is logged and left to the relay, because correctness depends on the
// outbox, not on this call.
func (o *Orchestrator) Admit(ctx context.Context, sessionID string, drafts []domain.EventDraft) ([]domain.Event, error) {
	adm, err := o.store.AdmitEvents(ctx, sessionID, drafts)
	if err != nil {
		return nil, err
	}
	if adm.Enqueued && o.signaler != nil {
		// Best-effort latency optimization. The relay is the source of correctness.
		if sigErr := o.signaler.Wake(ctx, sessionID, adm.MaxSeq); sigErr != nil {
			log.Printf("orchestrator: fast-path signal failed session_id=%s (relay will deliver): %v", sessionID, sigErr)
		}
	}
	// Echo only the caller-submitted events, matching the SQLite path's contract.
	if len(drafts) < len(adm.Events) {
		return adm.Events[:len(drafts)], nil
	}
	return adm.Events, nil
}

func (o *Orchestrator) TerminateSession(ctx context.Context, sessionID string) error {
	if o.signaler == nil {
		return nil
	}
	return o.signaler.TerminateSession(ctx, sessionID)
}

// CreateSession creates a session and admits its initial events, then fast-path
// signals as above.
func (o *Orchestrator) CreateSession(ctx context.Context, session domain.Session, initial []domain.EventDraft) (domain.Session, []domain.Event, error) {
	adm, err := o.store.CreateSession(ctx, session, initial)
	if err != nil {
		return domain.Session{}, nil, err
	}
	if adm.Enqueued && o.signaler != nil {
		if sigErr := o.signaler.Wake(ctx, session.ID, adm.MaxSeq); sigErr != nil {
			log.Printf("orchestrator: fast-path signal failed session_id=%s (relay will deliver): %v", session.ID, sigErr)
		}
	}
	return adm.Session, adm.Events, nil
}

// CreateAPISession is the HTTP control-plane variant. In addition to the
// admission/outbox transaction it requires the exact Agent version and
// Environment to remain active through the session insert.
func (o *Orchestrator) CreateAPISession(
	ctx context.Context,
	session domain.Session,
	initial []domain.EventDraft,
) (domain.Session, []domain.Event, error) {
	adm, err := o.store.CreateAPISession(ctx, session, initial)
	if err != nil {
		return domain.Session{}, nil, err
	}
	if adm.Enqueued && o.signaler != nil {
		if sigErr := o.signaler.Wake(ctx, session.ID, adm.MaxSeq); sigErr != nil {
			log.Printf(
				"orchestrator: fast-path signal failed session_id=%s (relay will deliver): %v",
				session.ID,
				sigErr,
			)
		}
	}
	return adm.Session, adm.Events, nil
}

// Runtime bundles the worker and relay so cmd can run the execution plane with a
// single call.
type Runtime struct {
	Client  client.Client
	Worker  worker.Worker
	Relay   *Relay
	Store   *pg.Store
	Signal  *Signaler
	Sandbox *sandbox.SessionManager
}

// NewRuntime wires the full Temporal execution plane against a PostgreSQL store,
// a model client, and a sandbox provider. The store is used both as the event
// source and as the durable tool-execution journal; the provider is wrapped in a
// session-scoped SessionManager so a tool's filesystem state persists across
// turns. The returned Runtime's Worker and Relay must be started by the caller.
func NewRuntime(
	c client.Client,
	store *pg.Store,
	modelClient model.Client,
	provider sandbox.Provider,
	ids domain.IDGenerator,
	relayCfg RelayConfig,
	previewPublisher ...PreviewPublisher,
) *Runtime {
	return NewRuntimeOnTaskQueue(
		c,
		store,
		modelClient,
		provider,
		ids,
		relayCfg,
		TaskQueue,
		previewPublisher...,
	)
}

// NewRuntimeOnTaskQueue is the deployment/test-isolated variant of NewRuntime.
// Production normally uses TaskQueue; integration environments can select a
// unique queue so another worker connected to the same Temporal namespace
// cannot execute Activities against the wrong PostgreSQL schema.
func NewRuntimeOnTaskQueue(
	c client.Client,
	store *pg.Store,
	modelClient model.Client,
	provider sandbox.Provider,
	ids domain.IDGenerator,
	relayCfg RelayConfig,
	taskQueue string,
	previewPublisher ...PreviewPublisher,
) *Runtime {
	sandboxes := sandbox.NewSessionManager(provider)
	src := storeSource{store: store} // satisfies both EventSource and JournalStore
	acts := NewActivities(modelClient, src, src, sandboxes, ids, previewPublisher...)
	w := NewWorkerOnTaskQueue(c, acts, taskQueue)
	signaler := NewSignalerOnTaskQueue(c, taskQueue)
	relay := NewRelay(store, signaler, relayCfg)
	return &Runtime{Client: c, Worker: w, Relay: relay, Store: store, Signal: signaler, Sandbox: sandboxes}
}

// Orchestrator returns an admission orchestrator sharing this runtime's store
// and signaler (post-commit fast path).
func (r *Runtime) Orchestrator() *Orchestrator {
	return NewOrchestrator(r.Store, r.Signal)
}
