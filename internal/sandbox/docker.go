package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"
)

// Docker provider notes on isolation:
//
// Unlike the local provider, the Docker provider gives REAL isolation: each
// sandbox is a container with its own kernel view via Linux namespaces and
// cgroups, its filesystem is separate from the host, and networking defaults to
// --network none. This is a genuine security boundary for ordinary untrusted
// code, not merely a dev-grade guardrail.
//
// It has NOT been audited for hostile multi-tenant use: a container shares the
// host kernel, so a kernel-level exploit could still cross the boundary. When
// that threat matters, gVisor can be layered under the same interface by adding
// --runtime=runsc to the create arguments; no interface change is required.

// DockerConfig configures the Docker-backed Provider.
type DockerConfig struct {
	// DockerPath is the docker CLI binary. Empty resolves via exec.LookPath.
	DockerPath string
	// DefaultImage is used when Spec.Image is empty. Empty defaults to
	// "alpine:latest".
	DefaultImage string
}

// dockerRoot is the working directory inside every container.
const dockerRoot = "/workspace"

// keepAlive holds the container open so exec can attach repeatedly. sleep with a
// huge argument keeps PID 1 alive until the container is removed.
const keepAlive = "sleep 2147483647"

// dockerProvider provisions container-backed sandboxes by shelling out to the
// docker CLI (no docker Go client, no extra module dependency).
type dockerProvider struct {
	dockerPath   string
	defaultImage string
}

// NewDockerProvider returns a Provider backed by the docker CLI. It resolves the
// docker binary eagerly so a missing docker fails fast at construction.
func NewDockerProvider(cfg DockerConfig) (Provider, error) {
	path := cfg.DockerPath
	if path == "" {
		p, err := exec.LookPath("docker")
		if err != nil {
			return nil, fmt.Errorf("sandbox: docker not found: %w", err)
		}
		path = p
	}
	image := cfg.DefaultImage
	if image == "" {
		image = "alpine:latest"
	}
	return &dockerProvider{dockerPath: path, defaultImage: image}, nil
}

// runDocker invokes the docker CLI with the given args, capturing stdout/stderr
// (capped) and the process exit code. The passed ctx bounds the whole call.
func (p *dockerProvider) runDocker(ctx context.Context, stdin []byte, args ...string) (stdout, stderr []byte, exitCode int, err error) {
	c := exec.CommandContext(ctx, p.dockerPath, args...)
	if len(stdin) > 0 {
		c.Stdin = bytes.NewReader(stdin)
	}
	outBuf := &cappedBuffer{cap: maxOutput}
	errBuf := &cappedBuffer{cap: maxOutput}
	c.Stdout = outBuf
	c.Stderr = errBuf

	runErr := c.Run()
	stdout = outBuf.Bytes()
	stderr = errBuf.Bytes()

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return stdout, stderr, exitErr.ExitCode(), nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

func (p *dockerProvider) Provision(ctx context.Context, spec Spec) (Sandbox, error) {
	image := spec.Image
	if image == "" {
		image = p.defaultImage
	}
	network := spec.Network
	if network == "" {
		network = "none"
	}

	args := []string{"create", "--network", network, "-w", dockerRoot}
	if spec.Memory != "" {
		args = append(args, "-m", spec.Memory)
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	if spec.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", spec.PidsLimit))
	}
	args = append(args, image, "sh", "-c", keepAlive)

	// Known narrow window: if the caller's ctx is cancelled while `docker
	// create` is mid-flight, exec.CommandContext kills the CLI but the daemon
	// may have already created the container. We never learn its id, so we
	// cannot clean it up. This is an inherent edge of the CLI-via-exec approach
	// and is rare; a container-lifecycle audit/reaper would be the general fix.
	stdout, stderr, code, err := p.runDocker(ctx, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker create: %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("sandbox: docker create failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}
	cid := strings.TrimSpace(string(stdout))
	if cid == "" {
		return nil, errors.New("sandbox: docker create returned empty container id")
	}

	// From here on, any failure must remove the created container so we never
	// leak it.
	if _, startErr, startCode, rerr := p.runDocker(ctx, nil, "start", cid); rerr != nil || startCode != 0 {
		p.forceRemove(cid)
		if rerr != nil {
			return nil, fmt.Errorf("sandbox: docker start: %w", rerr)
		}
		return nil, fmt.Errorf("sandbox: docker start failed (exit %d): %s", startCode, strings.TrimSpace(string(startErr)))
	}

	return &dockerSandbox{provider: p, cid: cid, timeout: spec.Timeout}, nil
}

// bestEffortDocker runs a docker CLI command with a fresh, bounded context so it
// still executes even when the caller's context is already cancelled, ignoring
// the result. It backs cleanup/teardown paths where failures are non-actionable.
func (p *dockerProvider) bestEffortDocker(args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, _, _ = p.runDocker(ctx, nil, args...)
}

// forceRemove best-effort removes a container so we never leak it.
func (p *dockerProvider) forceRemove(cid string) {
	p.bestEffortDocker("rm", "-f", cid)
}

// kill best-effort SIGKILLs the container (PID 1), tearing down any exec'd
// processes so a timed-out command does not linger. The container remains
// present (stopped) so Destroy can still remove it.
func (p *dockerProvider) kill(cid string) {
	p.bestEffortDocker("kill", cid)
}

type dockerSandbox struct {
	provider *dockerProvider
	cid      string
	timeout  time.Duration

	// mu guards dead. Once the container is torn down (timed-out and killed, or
	// destroyed), the sandbox is permanently dead: further calls must fail fast
	// rather than shell out to docker against a stopped/removed container, which
	// would otherwise surface as a confusing non-zero exit with err==nil.
	mu   sync.Mutex
	dead bool
}

// errDead is returned by operations on a sandbox whose container has been torn
// down. The caller must provision a fresh sandbox.
var errDead = errors.New("sandbox: container terminated (timed out or destroyed); provision a new sandbox")

func (s *dockerSandbox) markDead() {
	s.mu.Lock()
	s.dead = true
	s.mu.Unlock()
}

func (s *dockerSandbox) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

func (s *dockerSandbox) Root() string { return dockerRoot }

func (s *dockerSandbox) Exec(ctx context.Context, cmd Command) (*Result, error) {
	if s.isDead() {
		return nil, errDead
	}

	// Bound the command by the sandbox timeout while still honoring an
	// already-cancelled parent context.
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	args := append([]string{"exec", "-i", "-w", dockerRoot, s.cid, cmd.Path}, cmd.Args...)
	stdout, stderr, code, err := s.provider.runDocker(ctx, cmd.Stdin, args...)

	res := &Result{Stdout: stdout, Stderr: stderr, ExitCode: code}

	// Timeout: the deadline elapsing means docker exec was killed. The process
	// inside the container keeps running, so kill the container best-effort to
	// avoid a lingering runaway command. Killing PID 1 stops the whole
	// container, so the sandbox is now permanently dead.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		s.markDead()
		s.provider.kill(s.cid)
		return res, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return res, ctx.Err()
	}
	if err != nil {
		return res, fmt.Errorf("sandbox: docker exec: %w", err)
	}
	return res, nil
}

// containedPath joins a caller-supplied path onto the container root and
// verifies, by pure lexical analysis, that the cleaned result stays within
// root. It rejects ".." escapes and absolute paths that resolve outside root.
// The check is intentionally string-only: it never touches the host filesystem
// and reasons about POSIX container paths (the "path" package, not "filepath",
// so results are identical regardless of the host OS separator).
func containedPath(root, p string) (string, error) {
	clean := path.Clean(path.Join(root, p))
	if clean != root && !strings.HasPrefix(clean+"/", root+"/") {
		return "", fmt.Errorf("sandbox: path %q escapes root", p)
	}
	return clean, nil
}

// ReadFile copies a file out of the container via `docker cp` into a host temp
// file and returns its bytes. docker cp streams a tar of the raw file contents,
// so binary files round-trip faithfully (unlike exec+cat, which can mangle
// NULs and trailing newlines).
func (s *dockerSandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if s.isDead() {
		return nil, errDead
	}
	containerPath, err := containedPath(dockerRoot, path)
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp("", "mas-sbx-read-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create temp: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	_, stderr, code, err := s.provider.runDocker(ctx, nil, "cp", s.cid+":"+containerPath, tmpName)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker cp (read): %w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("sandbox: docker cp (read) failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}

	return os.ReadFile(tmpName)
}

// WriteFile writes data to a path inside the container. It creates the parent
// directory (docker exec mkdir -p), stages the bytes in a host temp file, and
// copies them in via `docker cp`, which preserves binary content exactly.
func (s *dockerSandbox) WriteFile(ctx context.Context, filePath string, data []byte) error {
	if s.isDead() {
		return errDead
	}
	containerPath, err := containedPath(dockerRoot, filePath)
	if err != nil {
		return err
	}

	parent := path.Dir(containerPath)
	if _, stderr, code, mkErr := s.provider.runDocker(ctx, nil, "exec", "-w", dockerRoot, s.cid, "mkdir", "-p", parent); mkErr != nil || code != 0 {
		if mkErr != nil {
			return fmt.Errorf("sandbox: docker exec mkdir: %w", mkErr)
		}
		return fmt.Errorf("sandbox: docker exec mkdir failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}

	tmp, err := os.CreateTemp("", "mas-sbx-write-*")
	if err != nil {
		return fmt.Errorf("sandbox: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sandbox: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sandbox: close temp: %w", err)
	}

	_, stderr, code, err := s.provider.runDocker(ctx, nil, "cp", tmpName, s.cid+":"+containerPath)
	if err != nil {
		return fmt.Errorf("sandbox: docker cp (write): %w", err)
	}
	if code != 0 {
		return fmt.Errorf("sandbox: docker cp (write) failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (s *dockerSandbox) Destroy(ctx context.Context) error {
	// A destroyed sandbox is dead regardless of the rm outcome: the intent is
	// teardown, so block any later Exec from hitting a removed container.
	s.markDead()
	// Teardown uses a fresh, bounded context (not the caller's ctx) so the
	// container is still removed even when the caller's context is already
	// cancelled — the same rule forceRemove/kill follow. Using the caller ctx
	// here would let a cancelled Run leak the container once cancellation is
	// wired into the runtime.
	rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, stderr, code, err := s.provider.runDocker(rmCtx, nil, "rm", "-f", s.cid)
	if err != nil {
		return fmt.Errorf("sandbox: docker rm: %w", err)
	}
	if code != 0 {
		msg := strings.TrimSpace(string(stderr))
		// Idempotent: a container that is already gone is not an error.
		if strings.Contains(msg, "No such container") {
			return nil
		}
		return fmt.Errorf("sandbox: docker rm failed (exit %d): %s", code, msg)
	}
	return nil
}
