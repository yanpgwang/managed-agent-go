package sandbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingProvider wraps a Provider and counts Provision calls so tests can
// assert a session provisions its logical sandbox exactly once.
type countingProvider struct {
	inner        Provider
	provisions   atomic.Int64
	provisionErr error
}

func (p *countingProvider) Provision(ctx context.Context, spec Spec) (Sandbox, error) {
	p.provisions.Add(1)
	if p.provisionErr != nil {
		return nil, p.provisionErr
	}
	return p.inner.Provision(ctx, spec)
}

// countingSandbox wraps a Sandbox and counts Destroy calls so tests can assert
// a session's sandbox is torn down exactly once.
type countingSandbox struct {
	inner    Sandbox
	destroys atomic.Int64
}

func (s *countingSandbox) Exec(ctx context.Context, cmd Command) (*Result, error) {
	return s.inner.Exec(ctx, cmd)
}
func (s *countingSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return s.inner.ReadFile(ctx, path)
}
func (s *countingSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	return s.inner.WriteFile(ctx, path, data)
}
func (s *countingSandbox) Root() string { return s.inner.Root() }
func (s *countingSandbox) Destroy(ctx context.Context) error {
	s.destroys.Add(1)
	return s.inner.Destroy(ctx)
}

// destroyCountingProvider hands out countingSandbox instances and remembers the
// last one it created so a test can inspect its destroy count.
type destroyCountingProvider struct {
	inner Provider
	mu    sync.Mutex
	last  *countingSandbox
}

func (p *destroyCountingProvider) Provision(ctx context.Context, spec Spec) (Sandbox, error) {
	box, err := p.inner.Provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	cs := &countingSandbox{inner: box}
	p.mu.Lock()
	p.last = cs
	p.mu.Unlock()
	return cs, nil
}

func TestSessionManager_ReusesSandboxPerSession(t *testing.T) {
	cp := &countingProvider{inner: NewLocalProvider()}
	m := NewSessionManager(cp)
	ctx := context.Background()

	first, err := m.Acquire(ctx, "sesn_a", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(ctx, "sesn_a", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Acquire returned a different sandbox for the same session")
	}
	if got := cp.provisions.Load(); got != 1 {
		t.Fatalf("provisions = %d, want 1 (session reuses one logical sandbox)", got)
	}
	if err := m.Release(ctx, "sesn_a"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionManager_IsolatesSessions(t *testing.T) {
	m := NewSessionManager(NewLocalProvider())
	ctx := context.Background()

	a, err := m.Acquire(ctx, "sesn_a", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Acquire(ctx, "sesn_b", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a.Root() == b.Root() {
		t.Fatal("different sessions must get distinct sandboxes")
	}

	if err := a.WriteFile(ctx, "shared.txt", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadFile(ctx, "shared.txt"); err == nil {
		t.Fatal("session B must not see a file written in session A")
	}
	_ = m.Release(ctx, "sesn_a")
	_ = m.Release(ctx, "sesn_b")
}

func TestSessionManager_ReleaseDestroysExactlyOnce(t *testing.T) {
	dp := &destroyCountingProvider{inner: NewLocalProvider()}
	m := NewSessionManager(dp)
	ctx := context.Background()

	if _, err := m.Acquire(ctx, "sesn_a", Spec{}); err != nil {
		t.Fatal(err)
	}
	box := dp.last

	if err := m.Release(ctx, "sesn_a"); err != nil {
		t.Fatal(err)
	}
	// A second Release for the same (now forgotten) session must be a no-op.
	if err := m.Release(ctx, "sesn_a"); err != nil {
		t.Fatal(err)
	}
	// Releasing a session that never provisioned is also a no-op.
	if err := m.Release(ctx, "sesn_never"); err != nil {
		t.Fatal(err)
	}
	if got := box.destroys.Load(); got != 1 {
		t.Fatalf("destroys = %d, want exactly 1", got)
	}
}

func TestSessionManager_ConcurrentAcquireProvisionsOnce(t *testing.T) {
	cp := &countingProvider{inner: NewLocalProvider()}
	m := NewSessionManager(cp)
	ctx := context.Background()

	const goroutines = 32
	var wg sync.WaitGroup
	boxes := make([]Sandbox, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			box, err := m.Acquire(ctx, "sesn_race", Spec{})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			boxes[i] = box
		}(i)
	}
	wg.Wait()

	if got := cp.provisions.Load(); got != 1 {
		t.Fatalf("concurrent Acquire provisioned %d times, want 1", got)
	}
	for i := 1; i < goroutines; i++ {
		if boxes[i] != boxes[0] {
			t.Fatal("concurrent Acquire returned different sandbox instances")
		}
	}
	_ = m.Release(ctx, "sesn_race")
}

func TestSessionManager_ProvisionFailureIsNotCached(t *testing.T) {
	cp := &countingProvider{inner: NewLocalProvider(), provisionErr: errors.New("boom")}
	m := NewSessionManager(cp)
	ctx := context.Background()

	if _, err := m.Acquire(ctx, "sesn_a", Spec{}); err == nil {
		t.Fatal("expected provision error")
	}
	// The failure must not be cached: a later Acquire should retry provisioning.
	cp.provisionErr = nil
	if _, err := m.Acquire(ctx, "sesn_a", Spec{}); err != nil {
		t.Fatalf("retry after failed provision: %v", err)
	}
	if got := cp.provisions.Load(); got != 2 {
		t.Fatalf("provisions = %d, want 2 (failure retried, not cached)", got)
	}
	_ = m.Release(ctx, "sesn_a")
}

// blockingProvider makes Provision block until proceed is closed, so a test can
// drive Release into the exact window where a provision is still in flight. It
// signals entry on entered and records the countingSandbox it eventually hands
// out so the test can assert the real instance is destroyed.
type blockingProvider struct {
	inner   Provider
	entered chan struct{} // closed once Provision has started
	proceed chan struct{} // Provision blocks until this is closed
	mu      sync.Mutex
	last    *countingSandbox
}

func (p *blockingProvider) Provision(ctx context.Context, spec Spec) (Sandbox, error) {
	close(p.entered)
	<-p.proceed
	box, err := p.inner.Provision(ctx, spec)
	if err != nil {
		return nil, err
	}
	cs := &countingSandbox{inner: box}
	p.mu.Lock()
	p.last = cs
	p.mu.Unlock()
	return cs, nil
}

// TestSessionManager_ReleaseWaitsForInflightProvision guards the narrow window
// where Release races a still-provisioning Acquire. With a deliberately blocking
// provider, Release begins while Provision is blocked: it must not return early,
// and once the provision finishes it must destroy the real instance exactly
// once rather than a half-provisioned entry.
func TestSessionManager_ReleaseWaitsForInflightProvision(t *testing.T) {
	p := &blockingProvider{
		inner:   NewLocalProvider(),
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	m := NewSessionManager(p)
	ctx := context.Background()

	acquired := make(chan struct{})
	go func() {
		if _, err := m.Acquire(ctx, "sesn_a", Spec{}); err != nil {
			t.Errorf("acquire: %v", err)
		}
		close(acquired)
	}()

	// Wait until Provision is actually in flight (Acquire is blocked inside the
	// provider), so Release runs against a still-provisioning entry.
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Provision was never entered")
	}

	// Release now races the in-flight provision. It must NOT return until the
	// provision finishes, because it has to destroy the real instance.
	released := make(chan error, 1)
	go func() { released <- m.Release(ctx, "sesn_a") }()

	select {
	case <-released:
		t.Fatal("Release returned before the in-flight provision completed")
	case <-time.After(100 * time.Millisecond):
		// Expected: Release is blocked waiting on the provision to finish.
	}

	// Unblock the provision. Acquire completes and Release can now proceed to
	// destroy the freshly provisioned sandbox.
	close(p.proceed)

	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Release did not complete after the provision finished")
	}
	<-acquired

	p.mu.Lock()
	box := p.last
	p.mu.Unlock()
	if box == nil {
		t.Fatal("provider never handed out a sandbox")
	}
	if got := box.destroys.Load(); got != 1 {
		t.Fatalf("destroys = %d, want exactly 1", got)
	}
}
