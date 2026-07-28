package temporal

import (
	"context"
	"log"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/pg"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Orchestrator is the feature-gated PostgreSQL/Temporal session path. It admits
// events into PostgreSQL (durable, atomic with the outbox wakeup), makes a
// best-effort post-commit fast-path signal to reduce latency, and relies on the
// outbox relay for eventual, correct delivery. It deliberately does NOT replace
// the SQLite dispatcher; both coexist until parity is proven.
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

// Runtime bundles the worker and relay so cmd can run the execution plane with a
// single call. It is only constructed when the feature gate is on.
type Runtime struct {
	Client  client.Client
	Worker  worker.Worker
	Relay   *Relay
	Store   *pg.Store
	Signal  *Signaler
	Sandbox *sandbox.SessionManager
}

// NewRuntime wires the full Temporal execution plane against a PostgreSQL store,
// an agent runtime, and a sandbox provider. The store is used both as the event
// source and as the durable tool-execution journal; the provider is wrapped in a
// session-scoped SessionManager so a tool's filesystem state persists across
// turns. The returned Runtime's Worker and Relay must be started by the caller.
func NewRuntime(c client.Client, store *pg.Store, rt agentruntime.AgentRuntime, provider sandbox.Provider, ids domain.IDGenerator, relayCfg RelayConfig) *Runtime {
	sandboxes := sandbox.NewSessionManager(provider)
	src := storeSource{store: store} // satisfies both EventSource and JournalStore
	acts := NewActivities(rt, src, src, sandboxes, ids)
	w := NewWorker(c, acts)
	signaler := NewSignaler(c)
	relay := NewRelay(store, signaler, relayCfg)
	return &Runtime{Client: c, Worker: w, Relay: relay, Store: store, Signal: signaler, Sandbox: sandboxes}
}

// Orchestrator returns an admission orchestrator sharing this runtime's store
// and signaler (post-commit fast path).
func (r *Runtime) Orchestrator() *Orchestrator {
	return NewOrchestrator(r.Store, r.Signal)
}
