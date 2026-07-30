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

// BindingStore persists sandbox ownership independently of worker memory.
// PutSandboxBinding is insert-if-absent and returns the authoritative binding,
// which may have been won by another worker. DeleteSandboxBinding must delete
// only when the persisted Ref still matches the caller's binding.
type BindingStore interface {
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

// NewSessionManager wraps a provider with durable session ownership. The
// optional store keeps deprecated single-process callers source-compatible; a
// process-local store is used when none is supplied. Production Temporal
// workers always pass PostgreSQL.
func NewSessionManager(provider Provider, stores ...BindingStore) *SessionManager {
	var bindings BindingStore = newMemoryBindingStore()
	if len(stores) > 0 && stores[0] != nil {
		bindings = stores[0]
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
		SpecHash:  specHash(spec),
	}
	authoritative, err := m.bindings.PutSandboxBinding(ctx, proposed)
	if err != nil {
		// The write result is ambiguous: destroying here could remove a
		// resource whose binding actually committed. Provider-side idempotency
		// lets the next attempt recover it; orphan reconciliation handles the
		// true no-commit case.
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
		if box != nil {
			return box.Destroy(ctx)
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

func specHash(spec Spec) string {
	body, _ := json.Marshal(spec)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum[:])
}

type memoryBindingStore struct {
	mu       sync.Mutex
	bindings map[string]Binding
}

func newMemoryBindingStore() *memoryBindingStore {
	return &memoryBindingStore{bindings: make(map[string]Binding)}
}

func (s *memoryBindingStore) GetSandboxBinding(
	_ context.Context,
	sessionID string,
) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[sessionID]
	return binding, ok, nil
}

func (s *memoryBindingStore) PutSandboxBinding(
	_ context.Context,
	binding Binding,
) (Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bindings[binding.SessionID]; ok {
		return current, nil
	}
	s.bindings[binding.SessionID] = binding
	return binding, nil
}

func (s *memoryBindingStore) DeleteSandboxBinding(
	_ context.Context,
	binding Binding,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bindings[binding.SessionID]; ok && current.Ref == binding.Ref {
		delete(s.bindings, binding.SessionID)
	}
	return nil
}
