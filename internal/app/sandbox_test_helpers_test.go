package app

import (
	"context"
	"sync/atomic"

	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

// provisionCountingProvider wraps a Provider and counts Create calls so a test
// can assert a session creates its logical sandbox exactly once across
// repeated runs.
type provisionCountingProvider struct {
	inner      sandbox.Provider
	provisions atomic.Int64
}

func (p *provisionCountingProvider) Name() string { return p.inner.Name() }

func (p *provisionCountingProvider) Create(
	ctx context.Context,
	sessionKey string,
	spec sandbox.Spec,
) (sandbox.Ref, sandbox.Sandbox, error) {
	p.provisions.Add(1)
	return p.inner.Create(ctx, sessionKey, spec)
}

func (p *provisionCountingProvider) Attach(
	ctx context.Context,
	sessionKey string,
	ref sandbox.Ref,
	spec sandbox.Spec,
) (sandbox.Sandbox, error) {
	return p.inner.Attach(ctx, sessionKey, ref, spec)
}

func (p *provisionCountingProvider) count() int64 { return p.provisions.Load() }

// destroyCountingProvider hands out sandboxes that count their own Destroy calls
// so a test can assert session deletion tears the sandbox down exactly once.
type destroyCountingProvider struct {
	inner    sandbox.Provider
	destroys atomic.Int64
}

func (p *destroyCountingProvider) Name() string { return p.inner.Name() }

func (p *destroyCountingProvider) Create(
	ctx context.Context,
	sessionKey string,
	spec sandbox.Spec,
) (sandbox.Ref, sandbox.Sandbox, error) {
	ref, box, err := p.inner.Create(ctx, sessionKey, spec)
	if err != nil {
		return sandbox.Ref{}, nil, err
	}
	return ref, &destroyCountingSandbox{inner: box, provider: p}, nil
}

func (p *destroyCountingProvider) Attach(
	ctx context.Context,
	sessionKey string,
	ref sandbox.Ref,
	spec sandbox.Spec,
) (sandbox.Sandbox, error) {
	box, err := p.inner.Attach(ctx, sessionKey, ref, spec)
	if err != nil {
		return nil, err
	}
	return &destroyCountingSandbox{inner: box, provider: p}, nil
}

func (p *destroyCountingProvider) destroyCount() int64 { return p.destroys.Load() }

type destroyCountingSandbox struct {
	inner    sandbox.Sandbox
	provider *destroyCountingProvider
}

func (s *destroyCountingSandbox) Exec(ctx context.Context, cmd sandbox.Command) (*sandbox.Result, error) {
	return s.inner.Exec(ctx, cmd)
}
func (s *destroyCountingSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return s.inner.ReadFile(ctx, path)
}
func (s *destroyCountingSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	return s.inner.WriteFile(ctx, path, data)
}
func (s *destroyCountingSandbox) Root() string { return s.inner.Root() }
func (s *destroyCountingSandbox) Destroy(ctx context.Context) error {
	s.provider.destroys.Add(1)
	return s.inner.Destroy(ctx)
}
