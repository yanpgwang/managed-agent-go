package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// maxOutput caps the bytes captured from stdout and stderr, independently.
const maxOutput = 100_000

// localProvider provisions sandboxes backed by the host filesystem and a plain
// child process. See the package doc: DEV-GRADE GUARDRAIL, not a security
// boundary.
type localProvider struct{}

// NewLocalProvider returns a Provider that runs commands as local child
// processes confined (best-effort) to a working directory.
//
// This is a dev-grade guardrail, NOT a security boundary: it shares the host
// kernel and filesystem namespace, and offers no network isolation. Do NOT run
// untrusted code with it.
func NewLocalProvider() Provider { return &localProvider{} }

func (p *localProvider) Provision(ctx context.Context, spec Spec) (Sandbox, error) {
	root := spec.WorkDir
	if root == "" {
		dir, err := os.MkdirTemp("", "mas-sbx-*")
		if err != nil {
			return nil, fmt.Errorf("sandbox: create root: %w", err)
		}
		root = dir
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("sandbox: create root: %w", err)
	}
	// Resolve symlinks so confinement checks compare canonical paths (e.g. on
	// darwin /tmp is a symlink to /private/tmp).
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve root: %w", err)
	}
	return &localSandbox{root: resolved, timeout: spec.Timeout}, nil
}

type localSandbox struct {
	root    string
	timeout time.Duration
}

func (s *localSandbox) Root() string { return s.root }

// resolve joins path onto root and verifies the cleaned result stays within
// root, rejecting ".." escapes and absolute paths that point outside root.
func (s *localSandbox) resolve(path string) (string, error) {
	clean := filepath.Clean(filepath.Join(s.root, path))
	sep := string(filepath.Separator)
	if clean != s.root && !strings.HasPrefix(clean+sep, s.root+sep) {
		return "", fmt.Errorf("sandbox: path %q escapes root", path)
	}
	return clean, nil
}

func (s *localSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

func (s *localSandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := s.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o600)
}

func (s *localSandbox) Exec(ctx context.Context, cmd Command) (*Result, error) {
	// Bound the command by the sandbox timeout, while still honoring an
	// already-cancelled parent context.
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	// exec.CommandContext kills the child process when ctx is done, so
	// cancellation and timeout both terminate the subprocess.
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = s.root
	// Minimal, cleared environment: nothing from the host is inherited.
	c.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + s.root}
	if len(cmd.Stdin) > 0 {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	stdout := &cappedBuffer{cap: maxOutput}
	stderr := &cappedBuffer{cap: maxOutput}
	c.Stdout = stdout
	c.Stderr = stderr

	runErr := c.Run()

	res := &Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}

	// Timeout / cancellation: the context deadline elapsing means the child was
	// killed by CommandContext.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return res, ctx.Err()
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("sandbox: exec: %w", runErr)
	}
	res.ExitCode = c.ProcessState.ExitCode()
	return res, nil
}

func (s *localSandbox) Destroy(ctx context.Context) error {
	// Idempotent: RemoveAll returns nil if root is already gone.
	return os.RemoveAll(s.root)
}

// cappedBuffer accumulates up to cap bytes, then drops the rest and records a
// truncation note appended once at the tail.
type cappedBuffer struct {
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := w.cap - w.buf.Len(); remaining > 0 {
		if len(p) <= remaining {
			return w.buf.Write(p)
		}
		w.buf.Write(p[:remaining])
		w.truncated = true
	} else {
		w.truncated = true
	}
	// Report the full length as consumed so the child process is not blocked by
	// a short write once the cap is reached.
	return len(p), nil
}

func (w *cappedBuffer) Bytes() []byte {
	if w.truncated {
		return append(w.buf.Bytes(), []byte("\n[output truncated]")...)
	}
	return w.buf.Bytes()
}
