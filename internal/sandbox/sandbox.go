// Package sandbox provides isolated execution for agent tools.
//
// The local provider is a DEV-GRADE GUARDRAIL, NOT A SECURITY BOUNDARY: it
// confines file paths to a working directory, clears the environment, applies a
// timeout, and caps output — but it shares the host kernel and filesystem
// namespace. Do NOT run untrusted code with it. Use an isolated Docker or remote
// provider for untrusted workloads.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound means a durable provider reference no longer resolves to a
// sandbox. Callers must not silently provision an empty replacement: doing so
// would present lost session workspace state as a successful resume.
var ErrNotFound = errors.New("sandbox: durable reference not found")

// PermanentError marks invalid ownership or configuration that retrying on the
// same worker cannot repair. Temporal Activities translate it to a
// non-retryable failure; a later public DELETE can start a fresh cleanup run
// after operators correct the worker configuration.
type PermanentError struct {
	err error
}

func (e *PermanentError) Error() string { return e.err.Error() }
func (e *PermanentError) Unwrap() error { return e.err }

// Permanent marks an adapter error as unsafe to retry on the same worker.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	var target *PermanentError
	if errors.As(err, &target) {
		return err
	}
	return &PermanentError{err: err}
}

func IsPermanent(err error) bool {
	var target *PermanentError
	return errors.As(err, &target)
}

// Spec describes the sandbox to provision.
type Spec struct {
	// WorkDir is the sandbox root. If empty, a temp dir is created.
	WorkDir string
	// Timeout bounds each Exec call. Zero means no per-command timeout.
	Timeout time.Duration

	// The following are used by container-backed providers (e.g. Docker) and
	// ignored by the local-process provider.
	Image     string // container image ref; empty uses the provider default
	Memory    string // e.g. "512m"; empty uses the provider/daemon default
	CPUs      string // e.g. "1.0"; empty uses the default
	Network   string // "none" (default) or "bridge"
	PidsLimit int    // max processes; 0 uses the default
	// NetworkAllowedHosts is the final allowlist for a limited network. Setup
	// NetworkAllowedHosts may temporarily add package registries while package
	// setup runs; the final policy is restored before binding publication.
	NetworkAllowedHosts      []string
	SetupNetworkAllowedHosts []string
	// Packages are installed once while provisioning, before the durable
	// sandbox binding becomes visible to tool execution.
	Packages PackageSet
}

// PackageSet is the normalized Managed Agents Environment package plan.
type PackageSet struct {
	Apt   []string
	Cargo []string
	Gem   []string
	Go    []string
	NPM   []string
	Pip   []string
}

func (p PackageSet) Empty() bool {
	return len(p.Apt) == 0 && len(p.Cargo) == 0 && len(p.Gem) == 0 &&
		len(p.Go) == 0 && len(p.NPM) == 0 && len(p.Pip) == 0
}

// Command is a single process invocation within a sandbox.
type Command struct {
	Path  string
	Args  []string
	Stdin []byte
}

// Result is the outcome of an Exec call.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	// TimedOut is true when the command was killed because the timeout or
	// context deadline elapsed.
	TimedOut bool
}

// Ref is the durable, provider-owned identity of one sandbox. It is safe to
// persist in the control-plane database: credentials and connection details
// remain in worker configuration, never in the reference.
type Ref struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

func (r Ref) validate() error {
	if r.Provider == "" {
		return errors.New("sandbox: reference provider is required")
	}
	if r.ID == "" {
		return errors.New("sandbox: reference id is required")
	}
	return nil
}

// Sandbox is a provisioned execution environment. File paths passed to
// ReadFile/WriteFile are relative to (and confined within) Root.
type Sandbox interface {
	Exec(ctx context.Context, cmd Command) (*Result, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	Root() string
	// Destroy is idempotent. Repeating it after the resource is already gone
	// must succeed so deletion workflows can safely retry a lost acknowledgement.
	Destroy(ctx context.Context) error
}

// Provider owns sandbox resources outside the agent loop. Create must expose a
// stable provider-side lookup key so a retry after a lost response resolves the
// same logical resource. When a provider cannot atomically create-if-absent,
// SessionManager's durable binding election destroys the losing resource from
// concurrent successful creates. Attach reconstructs a client from a persisted
// Ref after a worker restart and must verify that the provider resource still
// belongs to the given sessionKey. Destroy remains on Sandbox so execution and
// teardown use the same authenticated provider client.
type Provider interface {
	Name() string
	Create(ctx context.Context, sessionKey string, spec Spec) (Ref, Sandbox, error)
	Attach(ctx context.Context, sessionKey string, ref Ref, spec Spec) (Sandbox, error)
}

// PackageSetupProvider declares that package-manager commands execute inside
// the provider's isolation boundary rather than on the worker host. Providers
// must opt in explicitly; an unknown or local-process provider is denied.
type PackageSetupProvider interface {
	SupportsPackageSetup() bool
}

// LimitedNetworkProvider declares support for enforcing per-sandbox host
// allowlists. Providers must opt in explicitly; a bridge/none toggle alone is
// not sufficient.
type LimitedNetworkProvider interface {
	SupportsLimitedNetwork() bool
}

// LimitedNetworkSandbox reconciles the exact runtime allowlist for a live
// sandbox. It is optional because most providers cannot enforce FQDN policy.
type LimitedNetworkSandbox interface {
	ApplyLimitedNetwork(ctx context.Context, allowedHosts []string) error
}

func validateSandbox(provider Provider, ref Ref, box Sandbox) error {
	if provider == nil {
		return errors.New("sandbox: provider is required")
	}
	if box == nil {
		return errors.New("sandbox: provider returned a nil sandbox")
	}
	if err := ref.validate(); err != nil {
		return Permanent(err)
	}
	if ref.Provider != provider.Name() {
		return Permanent(fmt.Errorf(
			"sandbox: provider %q returned reference for %q",
			provider.Name(),
			ref.Provider,
		))
	}
	return nil
}
