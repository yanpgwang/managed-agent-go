package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func dockerAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	if err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skip("docker daemon not reachable")
	}
}

func newDockerSB(t *testing.T, spec Spec) Sandbox {
	t.Helper()
	dockerAvailable(t)
	p, err := NewDockerProvider(DockerConfig{DefaultImage: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Timeout == 0 {
		spec.Timeout = 30 * time.Second
	}
	sb, err := p.Provision(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sb.Destroy(context.Background()) })
	return sb
}

func TestDocker_ExecAndExitCode(t *testing.T) {
	sb := newDockerSB(t, Spec{})
	res, err := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "echo hi"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "hi" || res.ExitCode != 0 {
		t.Fatalf("exec echo: res=%+v err=%v", res, err)
	}
	res, err = sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("exit code: res=%+v err=%v", res, err)
	}
}

func TestDocker_Timeout(t *testing.T) {
	sb := newDockerSB(t, Spec{Timeout: 500 * time.Millisecond})
	res, err := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "sleep 10"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}

	// The kill on timeout stops the container: it must no longer be running.
	ds, ok := sb.(*dockerSandbox)
	if !ok {
		t.Fatalf("expected *dockerSandbox, got %T", sb)
	}
	out, _ := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", ds.cid).Output()
	if got := strings.TrimSpace(string(out)); got != "false" {
		t.Fatalf("container still running after timeout: inspect=%q", got)
	}
}

func TestDocker_FileRoundTripAndConfinement(t *testing.T) {
	sb := newDockerSB(t, Spec{})
	if err := sb.WriteFile(context.Background(), "sub/a.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	b, err := sb.ReadFile(context.Background(), "sub/a.txt")
	if err != nil || string(b) != "data" {
		t.Fatalf("read = %q err=%v", b, err)
	}
	// Path-escape confinement: assert on the specific "escapes root" signal,
	// not merely err != nil. A bare non-nil check cannot distinguish a
	// confinement rejection from an ordinary not-found error, so it would keep
	// passing even if containedPath were removed. Read and Write are checked
	// independently because they guard the boundary on separate code paths.
	if _, err := sb.ReadFile(context.Background(), "../escape"); err == nil ||
		!strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("ReadFile path escape must be rejected by confinement, got err=%v", err)
	}
	if err := sb.WriteFile(context.Background(), "../escape", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("WriteFile path escape must be rejected by confinement, got err=%v", err)
	}
	// "sub/../../escape" cleans to a path above root and must also be rejected.
	if _, err := sb.ReadFile(context.Background(), "sub/../../escape"); err == nil ||
		!strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("ReadFile nested ../ escape must be rejected by confinement, got err=%v", err)
	}
	// file written via WriteFile is visible to Exec
	res, _ := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "cat sub/a.txt"}})
	if strings.TrimSpace(string(res.Stdout)) != "data" {
		t.Fatalf("exec cat = %q", res.Stdout)
	}
}

func TestDocker_NetworkNoneByDefault(t *testing.T) {
	sb := newDockerSB(t, Spec{})
	// no network: DNS/连接应失败
	res, _ := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c",
		"wget -T2 -q -O- http://example.com >/dev/null 2>&1 && echo REACHED || echo BLOCKED"}})
	if strings.TrimSpace(string(res.Stdout)) != "BLOCKED" {
		t.Fatalf("network should be blocked by default, got %q", res.Stdout)
	}
}

func TestDocker_ExecAfterTimeoutReturnsError(t *testing.T) {
	sb := newDockerSB(t, Spec{Timeout: 500 * time.Millisecond})

	// First exec times out and kills the container.
	res, err := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "sleep 10"}})
	if err != nil {
		t.Fatalf("first exec: unexpected err=%v", err)
	}
	if !res.TimedOut {
		t.Fatalf("first exec: expected TimedOut, got %+v", res)
	}

	// The container is now dead. A subsequent exec must return an explicit
	// error rather than the silent (ExitCode=125, err=nil) result the raw
	// docker CLI would produce against a stopped container.
	res2, err := sb.Exec(context.Background(), Command{Path: "sh", Args: []string{"-c", "echo hi"}})
	if err == nil {
		t.Fatalf("exec after timeout: expected error, got res=%+v err=nil", res2)
	}
}
