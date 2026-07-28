package sandbox

import (
	"context"
	"sync"
)

// SessionManager gives each session a single logical sandbox that lives across
// its runs. The first run that needs tools acquires the sandbox; later runs in
// the same session get the same instance back, so filesystem state a tool
// created in one run is visible to the next. Different sessions acquire under
// different keys and never share a sandbox.
//
// Provider-specific provisioning and teardown stay behind this type: it wraps a
// Provider and owns the acquire/reuse/release lifecycle the application needs,
// without the application (or AgentRuntime) knowing how a sandbox is created or
// destroyed. Entering idle does nothing here; only Release tears a sandbox down,
// and it does so exactly once.
type SessionManager struct {
	provider Provider

	mu    sync.Mutex
	boxes map[string]*managedSandbox
}

// managedSandbox is one session's cached sandbox. ready is closed once
// provisioning finishes, so concurrent Acquire callers for the same session
// block until the single Provision call completes rather than racing to
// provision twice.
type managedSandbox struct {
	ready chan struct{}
	box   Sandbox
	err   error
}

// NewSessionManager wraps a Provider so sandboxes can be scoped to a session
// instead of a single run.
func NewSessionManager(provider Provider) *SessionManager {
	return &SessionManager{provider: provider, boxes: make(map[string]*managedSandbox)}
}

// Acquire returns the session's sandbox, provisioning it via the wrapped
// provider on first use and returning the same instance on every later call for
// the same sessionID. Concurrent first-use callers provision exactly once and
// all receive the same sandbox. A provisioning failure is not cached: the entry
// is dropped so a later Acquire can try again.
//
// spec is used only for the initial provision; once a session has a sandbox its
// spec is fixed and later specs are ignored (the existing instance is reused).
func (m *SessionManager) Acquire(ctx context.Context, sessionID string, spec Spec) (Sandbox, error) {
	m.mu.Lock()
	if entry, ok := m.boxes[sessionID]; ok {
		m.mu.Unlock()
		<-entry.ready
		return entry.box, entry.err
	}
	entry := &managedSandbox{ready: make(chan struct{})}
	m.boxes[sessionID] = entry
	m.mu.Unlock()

	entry.box, entry.err = m.provider.Provision(ctx, spec)
	if entry.err != nil {
		// Do not cache a failed provision: drop the entry so a subsequent run's
		// Acquire re-attempts rather than reusing the failure forever.
		m.mu.Lock()
		if m.boxes[sessionID] == entry {
			delete(m.boxes, sessionID)
		}
		m.mu.Unlock()
	}
	close(entry.ready)
	return entry.box, entry.err
}

// Release permanently destroys the session's sandbox and forgets it. Deleting
// the map entry under the lock guarantees the underlying Destroy runs at most
// once even under concurrent Release calls, so a session's sandbox is cleaned
// up exactly once. Releasing an unknown or already-released session is a no-op.
func (m *SessionManager) Release(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	entry, ok := m.boxes[sessionID]
	if ok {
		delete(m.boxes, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	// Wait for any in-flight provision so we destroy the real instance rather
	// than racing a half-provisioned entry.
	<-entry.ready
	if entry.err != nil || entry.box == nil {
		return nil
	}
	return entry.box.Destroy(ctx)
}
