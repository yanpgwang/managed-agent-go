// Package sandbox provides isolated execution for agent tools.
//
// The local provider is a DEV-GRADE GUARDRAIL, NOT A SECURITY BOUNDARY: it
// confines file paths to a working directory, clears the environment, applies a
// timeout, and caps output — but it shares the host kernel and filesystem
// namespace. Do NOT run untrusted code with it. Real isolation (Docker/gVisor)
// is a later slice behind the same interface.
package sandbox

import (
	"context"
	"time"
)

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

// Sandbox is a provisioned execution environment. File paths passed to
// ReadFile/WriteFile are relative to (and confined within) Root.
type Sandbox interface {
	Exec(ctx context.Context, cmd Command) (*Result, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	Root() string
	Destroy(ctx context.Context) error
}

// Provider provisions sandboxes.
type Provider interface {
	Provision(ctx context.Context, spec Spec) (Sandbox, error)
}
