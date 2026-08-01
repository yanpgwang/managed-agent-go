package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Binding is the durable association between a Managed Agents session and the
// provider resource that owns its workspace. SpecHash is diagnostic drift
// metadata; credentials and provider configuration must never be stored here.
type Binding struct {
	SessionID string
	Ref       Ref
	SpecHash  string
}

// ProvisioningIntent is the durable record written before Provider.Create.
// Spec contains sandbox configuration only; provider credentials must never be
// placed in it. Deleting is a reconciliation projection, not persisted intent
// state.
type ProvisioningIntent struct {
	SessionID string
	Provider  string
	Spec      Spec
	SpecHash  string
	Deleting  bool
}

// BindingStore persists sandbox ownership independently of worker memory.
// PutSandboxProvisioningIntent is insert-if-absent and records the
// provider-create obligation before the external call. PutSandboxBinding is
// insert-if-absent, returns the authoritative binding (which may have been won
// by another worker), and atomically removes the intent. Delete methods must
// remove only a record that still matches the caller's value.
type BindingStore interface {
	GetSandboxProvisioningIntent(
		ctx context.Context,
		sessionID string,
	) (ProvisioningIntent, bool, error)
	PutSandboxProvisioningIntent(
		ctx context.Context,
		intent ProvisioningIntent,
	) (ProvisioningIntent, error)
	ListSandboxProvisioningIntents(
		ctx context.Context,
		provider string,
		limit int,
	) ([]ProvisioningIntent, error)
	DeleteSandboxProvisioningIntent(
		ctx context.Context,
		intent ProvisioningIntent,
	) error
	GetSandboxBinding(ctx context.Context, sessionID string) (Binding, bool, error)
	PutSandboxBinding(ctx context.Context, binding Binding) (Binding, error)
	DeleteSandboxBinding(ctx context.Context, binding Binding) error
}

// SessionManager gives each session one logical sandbox across turns and worker
// restarts. Its in-memory map is only a client cache; BindingStore is the
// ownership source of truth, and Provider.Attach reconstructs a client from the
// persisted external reference.
//
// Operations for one session are serialized locally. Provider-side identity
// lookup recovers lost create responses; if separate worker processes both
// create successfully before either commits, BindingStore elects one durable
// winner and the losing resource is destroyed.
type SessionManager struct {
	provider Provider
	bindings BindingStore

	mu    sync.Mutex
	boxes map[string]Sandbox
	locks map[string]*sessionMutex
}

type sessionMutex struct {
	mu    sync.Mutex
	users int
}

// NewSessionManager wraps a provider with durable session ownership.
// BindingStore is required: sandbox identity must never fall back to process
// memory, even in a single-worker deployment.
func NewSessionManager(provider Provider, bindings BindingStore) *SessionManager {
	if bindings == nil {
		panic("sandbox: binding store is required")
	}
	return &SessionManager{
		provider: provider,
		bindings: bindings,
		boxes:    make(map[string]Sandbox),
		locks:    make(map[string]*sessionMutex),
	}
}

func (m *SessionManager) acquireSessionLock(sessionID string) func() {
	m.mu.Lock()
	lock, ok := m.locks[sessionID]
	if !ok {
		lock = &sessionMutex{}
		m.locks[sessionID] = lock
	}
	lock.users++
	m.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.mu.Lock()
		lock.users--
		if lock.users == 0 && m.locks[sessionID] == lock {
			delete(m.locks, sessionID)
		}
		m.mu.Unlock()
	}
}

// Acquire returns the session's live sandbox client. On first use it creates an
// idempotently named provider resource and commits its Ref. After a process
// restart it loads that Ref and attaches to the existing resource.
func (m *SessionManager) Acquire(
	ctx context.Context,
	sessionID string,
	spec Spec,
) (Sandbox, error) {
	if sessionID == "" {
		return nil, errors.New("sandbox: session id is required")
	}
	unlock := m.acquireSessionLock(sessionID)
	defer unlock()

	m.mu.Lock()
	if box := m.boxes[sessionID]; box != nil {
		m.mu.Unlock()
		return box, nil
	}
	m.mu.Unlock()

	binding, found, err := m.bindings.GetSandboxBinding(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox: load binding: %w", err)
	}
	if found {
		box, err := m.attach(ctx, binding, spec)
		if err != nil {
			return nil, err
		}
		m.cache(sessionID, box)
		return box, nil
	}

	proposedIntent := ProvisioningIntent{
		SessionID: sessionID,
		Provider:  m.provider.Name(),
		Spec:      spec,
		SpecHash:  specHash(spec),
	}
	intent, err := m.bindings.PutSandboxProvisioningIntent(ctx, proposedIntent)
	if err != nil {
		return nil, fmt.Errorf("sandbox: persist provisioning intent: %w", err)
	}
	if intent.Provider != proposedIntent.Provider ||
		intent.SpecHash != proposedIntent.SpecHash {
		return nil, Permanent(fmt.Errorf(
			"sandbox: session %s provisioning intent is for provider/spec %s/%s, worker requested %s/%s",
			sessionID,
			intent.Provider,
			intent.SpecHash,
			proposedIntent.Provider,
			proposedIntent.SpecHash,
		))
	}
	// A retry uses the authoritative saved Spec so provider identity does not
	// drift across a worker/configuration restart.
	spec = intent.Spec

	ref, box, err := m.provider.Create(ctx, sessionID, spec)
	if err != nil {
		return nil, err
	}
	if err := validateSandbox(m.provider, ref, box); err != nil {
		_ = box.Destroy(context.WithoutCancel(ctx))
		return nil, err
	}
	proposed := Binding{
		SessionID: sessionID,
		Ref:       ref,
		SpecHash:  intent.SpecHash,
	}
	authoritative, err := m.bindings.PutSandboxBinding(ctx, proposed)
	if err != nil {
		// The write result is ambiguous: destroying here could remove a
		// resource whose binding actually committed. Provider-side idempotency
		// lets the next attempt recover it, while the durable provisioning
		// intent keeps a true no-commit case visible to reconciliation.
		return nil, fmt.Errorf("sandbox: persist binding: %w", err)
	}
	if authoritative.Ref != proposed.Ref {
		_ = box.Destroy(context.WithoutCancel(ctx))
		box, err = m.attach(ctx, authoritative, spec)
		if err != nil {
			return nil, err
		}
	}
	m.cache(sessionID, box)
	return box, nil
}

func (m *SessionManager) attach(
	ctx context.Context,
	binding Binding,
	spec Spec,
) (Sandbox, error) {
	if binding.Ref.Provider != m.provider.Name() {
		return nil, Permanent(fmt.Errorf(
			"sandbox: session %s is bound to provider %q, worker has %q",
			binding.SessionID,
			binding.Ref.Provider,
			m.provider.Name(),
		))
	}
	box, err := m.provider.Attach(ctx, binding.SessionID, binding.Ref, spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox: attach session %s: %w", binding.SessionID, err)
	}
	if err := validateSandbox(m.provider, binding.Ref, box); err != nil {
		return nil, err
	}
	return box, nil
}

func (m *SessionManager) cache(sessionID string, box Sandbox) {
	m.mu.Lock()
	m.boxes[sessionID] = box
	m.mu.Unlock()
}

// Release idempotently tears down the provider resource and removes its
// binding. If the provider reports that the external resource is already gone,
// deleting the stale binding completes recovery. Other provider failures leave
// the binding intact so a durable cleanup retry can resume safely.
func (m *SessionManager) Release(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("sandbox: session id is required")
	}
	unlock := m.acquireSessionLock(sessionID)
	defer unlock()

	m.mu.Lock()
	box := m.boxes[sessionID]
	delete(m.boxes, sessionID)
	m.mu.Unlock()

	binding, found, err := m.bindings.GetSandboxBinding(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("sandbox: load binding for release: %w", err)
	}
	if !found {
		intent, intentFound, intentErr := m.bindings.GetSandboxProvisioningIntent(
			ctx,
			sessionID,
		)
		if intentErr != nil {
			return fmt.Errorf("sandbox: load provisioning intent for release: %w", intentErr)
		}
		if !intentFound {
			if box != nil {
				return box.Destroy(ctx)
			}
			return nil
		}
		if intent.Provider != m.provider.Name() {
			return Permanent(fmt.Errorf(
				"sandbox: cannot reconcile session %s intent for provider %q with %q",
				sessionID,
				intent.Provider,
				m.provider.Name(),
			))
		}
		// Provider.Create is idempotent by session key. It recovers a resource
		// created before a lost binding write; if the process died after writing
		// only the intent, creating then destroying an empty resource safely
		// discharges the same obligation.
		ref, orphan, createErr := m.provider.Create(ctx, sessionID, intent.Spec)
		if createErr != nil {
			return fmt.Errorf("sandbox: recover unbound resource for release: %w", createErr)
		}
		if err := validateSandbox(m.provider, ref, orphan); err != nil {
			if orphan != nil {
				_ = orphan.Destroy(context.WithoutCancel(ctx))
			}
			return err
		}
		if destroyErr := orphan.Destroy(ctx); destroyErr != nil &&
			!errors.Is(destroyErr, ErrNotFound) {
			return destroyErr
		}
		if err := m.bindings.DeleteSandboxProvisioningIntent(ctx, intent); err != nil {
			return fmt.Errorf("sandbox: delete provisioning intent: %w", err)
		}
		return nil
	}
	if binding.Ref.Provider != m.provider.Name() {
		return Permanent(fmt.Errorf(
			"sandbox: cannot release session %s bound to provider %q with %q",
			sessionID,
			binding.Ref.Provider,
			m.provider.Name(),
		))
	}
	if box == nil {
		box, err = m.provider.Attach(ctx, sessionID, binding.Ref, Spec{})
		if errors.Is(err, ErrNotFound) {
			return m.bindings.DeleteSandboxBinding(ctx, binding)
		}
		if err != nil {
			return fmt.Errorf("sandbox: attach for release: %w", err)
		}
		if err := validateSandbox(m.provider, binding.Ref, box); err != nil {
			return err
		}
	}
	if err := box.Destroy(ctx); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := m.bindings.DeleteSandboxBinding(ctx, binding); err != nil {
		return fmt.Errorf("sandbox: delete binding: %w", err)
	}
	return nil
}

// ReconcileProvisioning recovers provider resources left between the durable
// intent and binding commits. Active sessions finish binding the idempotently
// named resource; deleting sessions recover-and-destroy it. Every intent is
// attempted even if an earlier one fails.
func (m *SessionManager) ReconcileProvisioning(
	ctx context.Context,
	limit int,
) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	intents, err := m.bindings.ListSandboxProvisioningIntents(
		ctx,
		m.provider.Name(),
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("sandbox: list provisioning intents: %w", err)
	}
	var (
		completed int
		errs      []error
	)
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if intent.Deleting {
			err = m.Release(ctx, intent.SessionID)
		} else {
			_, err = m.Acquire(ctx, intent.SessionID, intent.Spec)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"session %s: %w",
				intent.SessionID,
				err,
			))
			continue
		}
		completed++
	}
	return completed, errors.Join(errs...)
}

func specHash(spec Spec) string {
	body, _ := json.Marshal(spec)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum[:])
}
