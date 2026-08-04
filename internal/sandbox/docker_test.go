package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	_, sb, err := p.Create(context.Background(), t.Name(), spec)
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

func TestDocker_CreateIsIdempotentAndAttachPreservesWorkspace(t *testing.T) {
	dockerAvailable(t)
	ctx := context.Background()
	firstProvider, err := NewDockerProvider(DockerConfig{DefaultImage: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	ref, first, err := firstProvider.Create(ctx, t.Name(), Spec{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Destroy(context.Background()) })
	if err := first.WriteFile(ctx, "state.txt", []byte("durable")); err != nil {
		t.Fatal(err)
	}
	firstResources, ok := first.(FileResourceSandbox)
	if !ok {
		t.Fatalf("first Docker sandbox does not expose FileResourceSandbox: %T", first)
	}
	resourceContent := []byte("restored mount")
	resourceMount := testReadOnlyMount(
		SessionUploadsRoot+"/restart.txt", resourceContent,
	)
	resourceMount.Identity = "sesrsc_restart"
	if err := firstResources.ImportReadOnlyFile(
		ctx, resourceMount, bytes.NewReader(resourceContent),
	); err != nil {
		t.Fatalf("import before worker restart: %v", err)
	}

	secondProvider, err := NewDockerProvider(DockerConfig{DefaultImage: "alpine:latest"})
	if err != nil {
		t.Fatal(err)
	}
	sameRef, same, err := secondProvider.Create(
		ctx,
		t.Name(),
		Spec{Timeout: 30 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sameRef != ref {
		t.Fatalf("idempotent Create ref = %+v, want %+v", sameRef, ref)
	}
	attached, err := secondProvider.Attach(
		ctx,
		t.Name(),
		ref,
		Spec{Timeout: 30 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, box := range map[string]Sandbox{"same": same, "attached": attached} {
		data, err := box.ReadFile(ctx, "state.txt")
		if err != nil || string(data) != "durable" {
			t.Fatalf("%s workspace data = %q, err=%v", name, data, err)
		}
	}
	resources, ok := attached.(FileResourceSandbox)
	if !ok {
		t.Fatalf("attached Docker sandbox does not expose FileResourceSandbox: %T", attached)
	}
	present, err := resources.HasReadOnlyFile(ctx, resourceMount)
	if err != nil || !present {
		t.Fatalf("attached sandbox did not recognize staged resource: present=%t err=%v", present, err)
	}
	result, err := attached.Exec(ctx, Command{
		Path: "sh", Args: []string{"-c", "cat /mnt/session/uploads/restart.txt"},
	})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, resourceContent) {
		t.Fatalf("attached resource mount: result=%+v err=%v", result, err)
	}
	if _, err := secondProvider.Attach(
		ctx,
		"another-session",
		ref,
		Spec{Timeout: 30 * time.Second},
	); err == nil || !IsPermanent(err) {
		t.Fatalf("cross-session Attach = %v, want permanent ownership error", err)
	}
}

func TestDocker_AttachLegacyContainerAllowsResourceDetach(t *testing.T) {
	dockerAvailable(t)
	ctx := context.Background()
	providerInterface, err := NewDockerProvider(DockerConfig{
		DefaultImage:    "alpine:latest",
		ResourceBaseDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	sessionKey := t.Name()
	name := dockerContainerName(sessionKey)
	stdout, stderr, code, err := provider.runDocker(
		ctx,
		nil,
		"create",
		"--name", name,
		"--label", dockerManagedLabel+"=true",
		"--label", dockerSessionKeyLabel+"="+sessionKey,
		"--network", "none",
		"-w", dockerRoot,
		"alpine:latest",
		"sh", "-c", keepAlive,
	)
	if err != nil || code != 0 {
		t.Fatalf("create legacy container: code=%d stderr=%q err=%v", code, stderr, err)
	}
	cid := strings.TrimSpace(string(stdout))
	if cid == "" {
		t.Fatal("create legacy container returned no id")
	}
	t.Cleanup(func() { provider.forceRemove(cid) })
	_, stderr, code, err = provider.runDocker(ctx, nil, "start", cid)
	if err != nil || code != 0 {
		t.Fatalf("start legacy container: code=%d stderr=%q err=%v", code, stderr, err)
	}

	box, err := provider.Attach(
		ctx,
		sessionKey,
		Ref{Provider: DockerProviderName, ID: cid},
		Spec{Timeout: 30 * time.Second},
	)
	if err != nil {
		t.Fatalf("attach legacy container: %v", err)
	}
	result, err := box.Exec(ctx, Command{Path: "sh", Args: []string{"-c", "printf legacy"}})
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != "legacy" {
		t.Fatalf("legacy container exec: result=%+v err=%v", result, err)
	}
	resources := box.(FileResourceSandbox)
	content := []byte("resource")
	mount := testReadOnlyMount(SessionUploadsRoot+"/legacy.txt", content)
	present, err := resources.HasReadOnlyFile(ctx, mount)
	if err != nil || present {
		t.Fatalf("legacy HasReadOnlyFile = %t, err=%v", present, err)
	}
	if err := resources.ImportReadOnlyFile(ctx, mount, bytes.NewReader(content)); err == nil || !IsPermanent(err) {
		t.Fatalf("legacy import error = %v, want permanent unsupported mount", err)
	}
	if err := resources.RemoveReadOnlyFile(ctx, mount.RuntimePath, mount.Identity); err != nil {
		t.Fatalf("legacy detach must be a no-op: %v", err)
	}
}

func TestDocker_FileResourceMountIsAtomicAndReadOnly(t *testing.T) {
	sb := newDockerSB(t, Spec{})
	resources, ok := sb.(FileResourceSandbox)
	if !ok {
		t.Fatalf("Docker sandbox does not expose FileResourceSandbox: %T", sb)
	}
	content := []byte("durable resource\n")
	mount := testReadOnlyMount("/mnt/session/uploads/nested/data.txt", content)

	present, err := resources.HasReadOnlyFile(context.Background(), mount)
	if err != nil || present {
		t.Fatalf("initial HasReadOnlyFile = %t, err=%v", present, err)
	}
	if err := resources.ImportReadOnlyFile(
		context.Background(), mount, bytes.NewReader(content),
	); err != nil {
		t.Fatal(err)
	}
	present, err = resources.HasReadOnlyFile(context.Background(), mount)
	if err != nil || !present {
		t.Fatalf("HasReadOnlyFile after import = %t, err=%v", present, err)
	}

	result, err := sb.Exec(context.Background(), Command{
		Path: "sh", Args: []string{"-c", "cat /mnt/session/uploads/nested/data.txt"},
	})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, content) {
		t.Fatalf("read mounted resource: result=%+v err=%v", result, err)
	}
	result, err = sb.Exec(context.Background(), Command{
		Path: "sh", Args: []string{"-c", "printf changed > /mnt/session/uploads/nested/data.txt"},
	})
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("write to read-only mount: result=%+v err=%v", result, err)
	}

	replacement := testReadOnlyMount(mount.RuntimePath, []byte("replacement"))
	if err := resources.ImportReadOnlyFile(
		context.Background(), replacement, &readerThatFails{data: []byte("partial")},
	); err == nil {
		t.Fatal("partial replacement unexpectedly succeeded")
	}
	result, err = sb.Exec(context.Background(), Command{
		Path: "sh", Args: []string{"-c", "cat /mnt/session/uploads/nested/data.txt"},
	})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, content) {
		t.Fatalf("failed replacement changed visible file: result=%+v err=%v", result, err)
	}

	invalid := testReadOnlyMount("/mnt/session/uploads/../escape", content)
	if err := resources.ImportReadOnlyFile(
		context.Background(), invalid, bytes.NewReader(content),
	); err == nil {
		t.Fatal("path traversal unexpectedly succeeded")
	}
	control := testReadOnlyMount("/mnt/session/uploads/line\nbreak", content)
	if err := resources.ImportReadOnlyFile(
		context.Background(), control, bytes.NewReader(content),
	); err == nil {
		t.Fatal("control character in path unexpectedly succeeded")
	}
	if err := resources.RemoveReadOnlyFile(context.Background(), mount.RuntimePath, mount.Identity); err != nil {
		t.Fatal(err)
	}
	if err := resources.RemoveReadOnlyFile(context.Background(), mount.RuntimePath, mount.Identity); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
	present, err = resources.HasReadOnlyFile(context.Background(), mount)
	if err != nil || present {
		t.Fatalf("HasReadOnlyFile after remove = %t, err=%v", present, err)
	}
	parentContent := []byte("parent replacement")
	parent := testReadOnlyMount("/mnt/session/uploads/nested", parentContent)
	parent.Identity = "sesrsc_parent"
	if err := resources.ImportReadOnlyFile(
		context.Background(), parent, bytes.NewReader(parentContent),
	); err != nil {
		t.Fatalf("import parent after nested deletion: %v", err)
	}
	if err := resources.RemoveReadOnlyFile(
		context.Background(), parent.RuntimePath, mount.Identity,
	); err != nil {
		t.Fatalf("stale identity removal: %v", err)
	}
	result, err = sb.Exec(context.Background(), Command{
		Path: "sh", Args: []string{"-c", "cat /mnt/session/uploads/nested"},
	})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, parentContent) {
		t.Fatalf("stale removal changed replacement: result=%+v err=%v", result, err)
	}
	if err := resources.RemoveReadOnlyFile(
		context.Background(), parent.RuntimePath, parent.Identity,
	); err != nil {
		t.Fatalf("remove parent replacement: %v", err)
	}
}

func TestDocker_FileResourceImportStreamsLargeContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dockerResourceFilesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, dockerResourceStateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	box := &dockerSandbox{resourceRoot: root, resourceMountReady: true}
	const size = int64(32 << 20)
	hash := sha256.New()
	if _, err := io.CopyN(hash, zeroReader{}, size); err != nil {
		t.Fatal(err)
	}
	mount := ReadOnlyFileMount{
		Identity:       "sesrsc_large",
		RuntimePath:    SessionUploadsRoot + "/large.bin",
		SizeBytes:      size,
		ChecksumSHA256: hex.EncodeToString(hash.Sum(nil)),
	}
	reader := &trackingZeroReader{remaining: size}
	if err := box.ImportReadOnlyFile(context.Background(), mount, reader); err != nil {
		t.Fatal(err)
	}
	if reader.maxRequest > 1<<20 {
		t.Fatalf("stream read buffer = %d bytes, want at most 1 MiB", reader.maxRequest)
	}
	info, err := os.Stat(filepath.Join(root, dockerResourceFilesDir, "large.bin"))
	if err != nil || info.Size() != size {
		t.Fatalf("large resource info=%+v err=%v", info, err)
	}
}

func TestDocker_FileResourceDirectoryModesIgnoreUmask(t *testing.T) {
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	providerInterface, err := NewDockerProvider(DockerConfig{
		DockerPath:      "/usr/bin/true",
		ResourceBaseDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	root, _, err := provider.ensureResourceRoot(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root) //nolint:errcheck // test cleanup

	box := &dockerSandbox{resourceRoot: root, resourceMountReady: true}
	content := []byte("mode check")
	mount := testReadOnlyMount(SessionUploadsRoot+"/nested/mode.txt", content)
	if err := box.ImportReadOnlyFile(context.Background(), mount, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]os.FileMode{
		filepath.Join(root, dockerResourceFilesDir):           0o755,
		filepath.Join(root, dockerResourceFilesDir, "nested"): 0o755,
		filepath.Join(root, dockerResourceStateDir):           0o700,
	} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode for %s = %#o, want %#o", target, got, want)
		}
	}
}

func TestDocker_ReapsOnlyStaleUnreferencedResourceRoots(t *testing.T) {
	base := t.TempDir()
	providerInterface, err := NewDockerProvider(DockerConfig{
		DockerPath:      "/usr/bin/true",
		ResourceBaseDir: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	stale := filepath.Join(base, dockerResourceRootPrefix("stale")+"abcdef")
	fresh := filepath.Join(base, dockerResourceRootPrefix("fresh")+"abcdef")
	unowned := filepath.Join(base, "other-stale-directory")
	for _, directory := range []string{stale, fresh, unowned} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unowned, old, old); err != nil {
		t.Fatal(err)
	}
	if err := provider.reapStaleResourceRoots(
		context.Background(), now.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale provider root remains: %v", err)
	}
	for _, directory := range []string{fresh, unowned} {
		if _, err := os.Stat(directory); err != nil {
			t.Fatalf("protected directory %s was removed: %v", directory, err)
		}
	}
}

func TestDocker_ReaperDoesNothingWhenContainerAuditFails(t *testing.T) {
	base := t.TempDir()
	providerInterface, err := NewDockerProvider(DockerConfig{
		DockerPath:      "/usr/bin/false",
		ResourceBaseDir: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	stale := filepath.Join(base, dockerResourceRootPrefix("stale")+"abcdef")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := provider.reapStaleResourceRoots(
		context.Background(), time.Now().Add(-time.Hour),
	); err == nil {
		t.Fatal("reaper audit unexpectedly succeeded")
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("failed audit removed staging directory: %v", err)
	}
}

func TestDocker_ResourceMountInspectFailureRemainsRetryable(t *testing.T) {
	providerInterface, err := NewDockerProvider(DockerConfig{
		DockerPath:      "/usr/bin/false",
		ResourceBaseDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	_, _, err = provider.inspectResourceMount(
		context.Background(), "container", "session",
	)
	if err == nil || IsPermanent(err) {
		t.Fatalf("inspect error = %v, want retryable failure", err)
	}
}

func TestDocker_DefaultResourceDirectoryFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	providerInterface, err := NewDockerProvider(DockerConfig{DockerPath: "/usr/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	provider := providerInterface.(*dockerProvider)
	want := filepath.Join(
		os.TempDir(), fmt.Sprintf("managed-agent-resources-%d", os.Getuid()),
	)
	if provider.resourceBaseDir != want {
		t.Fatalf("resourceBaseDir = %q, want %q", provider.resourceBaseDir, want)
	}
}

func testReadOnlyMount(runtimePath string, content []byte) ReadOnlyFileMount {
	sum := sha256.Sum256(content)
	return ReadOnlyFileMount{
		Identity: "sesrsc_test", RuntimePath: runtimePath, SizeBytes: int64(len(content)),
		ChecksumSHA256: hex.EncodeToString(sum[:]),
	}
}

type readerThatFails struct {
	data []byte
}

func (r *readerThatFails) Read(buffer []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("injected read failure")
	}
	n := copy(buffer, r.data)
	r.data = r.data[n:]
	return n, nil
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

type trackingZeroReader struct {
	remaining  int64
	maxRequest int
}

func (r *trackingZeroReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) > r.maxRequest {
		r.maxRequest = len(buffer)
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	clear(buffer)
	r.remaining -= int64(len(buffer))
	return len(buffer), nil
}
