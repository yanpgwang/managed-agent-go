package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	daytonaerrors "github.com/daytona/clients/sdk-go/pkg/errors"
	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

func TestRemoteProviderConformance(t *testing.T) {
	tests := []struct {
		name string
		open func(*fakeRemoteStore) Provider
	}{
		{
			name: E2BProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					E2BProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: CubeProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					CubeProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: OpenSandboxProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newOpenSandboxProvider(
					&fakeOpenSandboxService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: DaytonaProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newDaytonaProvider(
					&fakeDaytonaService{store: store},
					defaultDaytonaRoot,
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeRemoteStore(t.TempDir())
			runFakeRemoteContract(t, func() Provider { return test.open(store) })
		})
	}
}

func TestRemoteFileResourceConformance(t *testing.T) {
	tests := []struct {
		name string
		open func(*fakeRemoteStore) Provider
	}{
		{
			name: E2BProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					E2BProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: CubeProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					CubeProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: OpenSandboxProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newOpenSandboxProvider(
					&fakeOpenSandboxService{store: store}, remoteDefaultRoot,
				)
			},
		},
		{
			name: DaytonaProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newDaytonaProvider(
					&fakeDaytonaService{store: store}, defaultDaytonaRoot,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeRemoteStore(t.TempDir())
			runFakeRemoteFileResourceContract(
				t, store, func() Provider { return test.open(store) },
			)
		})
	}
}

func TestRemoteSessionOutputConformance(t *testing.T) {
	tests := []struct {
		name string
		open func(*fakeRemoteStore) Provider
	}{
		{
			name: E2BProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					E2BProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: CubeProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newE2BLikeProvider(
					CubeProviderName,
					&fakeE2BService{store: store},
					remoteDefaultRoot,
				)
			},
		},
		{
			name: OpenSandboxProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newOpenSandboxProvider(
					&fakeOpenSandboxService{store: store}, remoteDefaultRoot,
				)
			},
		},
		{
			name: DaytonaProviderName,
			open: func(store *fakeRemoteStore) Provider {
				return newDaytonaProvider(
					&fakeDaytonaService{store: store}, defaultDaytonaRoot,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeRemoteStore(t.TempDir())
			runFakeRemoteSessionOutputContract(
				t, store, func() Provider { return test.open(store) },
			)
		})
	}
}

func TestRemoteSessionOutputUsesOperationDeadline(t *testing.T) {
	const sandboxTimeout = 5 * time.Second
	tests := []struct {
		name string
		box  func(*fakeRemoteStore, *fakeRemoteResource) SessionOutputSandbox
	}{
		{
			name: E2BProviderName,
			box: func(
				store *fakeRemoteStore,
				resource *fakeRemoteResource,
			) SessionOutputSandbox {
				return newE2BLikeSandbox(
					E2BProviderName,
					&fakeE2BRemote{fakeRemoteHandle: fakeRemoteHandle{
						store: store, resource: resource,
					}},
					remoteDefaultRoot,
					sandboxTimeout,
				)
			},
		},
		{
			name: CubeProviderName,
			box: func(
				store *fakeRemoteStore,
				resource *fakeRemoteResource,
			) SessionOutputSandbox {
				return newE2BLikeSandbox(
					CubeProviderName,
					&fakeE2BRemote{fakeRemoteHandle: fakeRemoteHandle{
						store: store, resource: resource,
					}},
					remoteDefaultRoot,
					sandboxTimeout,
				)
			},
		},
		{
			name: OpenSandboxProviderName,
			box: func(
				store *fakeRemoteStore,
				resource *fakeRemoteResource,
			) SessionOutputSandbox {
				return newOpenSandboxBox(
					&fakeOpenSandboxRemote{fakeRemoteHandle: fakeRemoteHandle{
						store: store, resource: resource,
					}},
					remoteDefaultRoot,
					sandboxTimeout,
				)
			},
		},
		{
			name: DaytonaProviderName,
			box: func(
				store *fakeRemoteStore,
				resource *fakeRemoteResource,
			) SessionOutputSandbox {
				return newDaytonaBox(
					&fakeDaytonaRemote{fakeRemoteHandle: fakeRemoteHandle{
						store: store, resource: resource,
					}},
					defaultDaytonaRoot,
					sandboxTimeout,
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeRemoteStore(t.TempDir())
			resource := store.create("", nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			stream, err := test.box(store, resource).OpenSessionOutputs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if got := store.lastExecTimeout(resource.id); got < 30*time.Second {
				t.Fatalf(
					"Session output command timeout = %v, want operation deadline",
					got,
				)
			}
		})
	}
}

func TestRemoteSessionOutputArchiveTimeoutIsRetryable(t *testing.T) {
	store := newFakeRemoteStore(t.TempDir())
	resource := store.create("", nil)
	handle := &fakeRemoteHandle{store: store, resource: resource}
	resources := newRemoteFileResources("test", handle)

	stream, err := openRemoteSessionOutputs(
		context.Background(),
		"test",
		resources,
		func(context.Context, Command) (*Result, error) {
			return &Result{ExitCode: -1, TimedOut: true}, nil
		},
	)
	if stream != nil {
		_ = stream.Close()
		t.Fatal("timed-out Session output archive returned a stream")
	}
	if err == nil || IsPermanent(err) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timed-out Session output archive error = %v, want retryable timeout", err)
	}
	controlPath, pathErr := handle.fullPath(remoteSessionOutputControlRoot)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	entries, readErr := os.ReadDir(controlPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("timed-out Session output archive retained %d snapshot(s)", len(entries))
	}
}

func runFakeRemoteSessionOutputContract(
	t *testing.T,
	store *fakeRemoteStore,
	open func() Provider,
) {
	t.Helper()
	ctx := context.Background()
	provider := open()
	capability, ok := provider.(SessionOutputProvider)
	if !ok || !capability.SupportsSessionOutputs() {
		t.Fatalf("provider %q does not advertise Session outputs", provider.Name())
	}
	sessionKey := "sesn-outputs-" + strings.NewReplacer("/", "-", " ", "-").
		Replace(strings.ToLower(t.Name()))
	ref, box, err := provider.Create(ctx, sessionKey, Spec{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })
	exporter, ok := box.(SessionOutputSandbox)
	if !ok {
		t.Fatalf("sandbox %T does not expose Session outputs", box)
	}
	locker, ok := box.(ResourceSynchronizationSandbox)
	if !ok {
		t.Fatalf("sandbox %T does not expose resource synchronization", box)
	}
	if err := box.WriteFile(
		ctx, SessionOutputsRoot+"/nested/tool.txt", []byte("tool"),
	); err != nil {
		t.Fatalf("write output through tool boundary: %v", err)
	}
	result, err := box.Exec(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "printf shell > /mnt/session/outputs/shell.txt"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("write output through shell boundary: result=%+v err=%v", result, err)
	}
	if err := box.WriteFile(
		ctx, remoteSessionOutputControlRoot+"/forbidden", []byte("x"),
	); err == nil {
		t.Fatal("WriteFile accepted adapter-owned Session output staging path")
	}
	unlock, err := locker.LockResourceOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first := readFakeRemoteSessionOutputArchive(t, ctx, exporter)
	second := readFakeRemoteSessionOutputArchive(t, ctx, exporter)
	unlock()
	want := map[string]string{"nested/tool.txt": "tool", "shell.txt": "shell"}
	if !equalStringMaps(first, want) || !equalStringMaps(second, want) {
		t.Fatalf("repeatable output snapshots = first:%v second:%v want:%v", first, second, want)
	}
	controlPath, err := (&fakeRemoteHandle{resource: store.resources[ref.ID]}).fullPath(
		remoteSessionOutputControlRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Session output staging retained %d archive(s)", len(entries))
	}
}

func readFakeRemoteSessionOutputArchive(
	t *testing.T,
	ctx context.Context,
	exporter SessionOutputSandbox,
) map[string]string {
	t.Helper()
	stream, err := exporter.OpenSessionOutputs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string)
	reader := tar.NewReader(stream)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		content, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatal(readErr)
		}
		files[strings.TrimPrefix(header.Name, "./")] = string(content)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	return files
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func runFakeRemoteFileResourceContract(
	t *testing.T,
	store *fakeRemoteStore,
	open func() Provider,
) {
	t.Helper()
	ctx := context.Background()
	spec := Spec{Timeout: 5 * time.Second}
	sessionKey := "sesn-files-" + strings.NewReplacer("/", "-", " ", "-").
		Replace(strings.ToLower(t.Name()))
	provider := open()
	capability, ok := provider.(FileResourceProvider)
	if !ok || !capability.SupportsFileResources() {
		t.Fatalf("provider %q does not advertise File Resources", provider.Name())
	}
	ref, box, err := provider.Create(ctx, sessionKey, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })
	resources, ok := box.(FileResourceSandbox)
	if !ok {
		t.Fatalf("sandbox %T does not expose File Resources", box)
	}
	content := []byte("remote resource\n")
	mount := testFileResourceMount(SessionUploadsRoot+"/nested/data.txt", content)
	mount.Identity = "sesrsc_remote_first"
	execCalls := store.execCallCount(ref.ID)
	if err := resources.ImportFileResource(ctx, mount, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if calls := store.execCallCount(ref.ID); calls != execCalls {
		t.Fatalf(
			"File Resource import used Exec: got %d call(s), want %d",
			calls,
			execCalls,
		)
	}
	assertFakeRemoteResource(t, ctx, box, resources, mount, content)
	edited := []byte("agent-edited resource\n")
	if err := box.WriteFile(ctx, mount.RuntimePath, edited); err != nil {
		t.Fatalf("edit materialized File Resource: %v", err)
	}
	assertFakeRemoteResource(t, ctx, box, resources, mount, edited)

	execCalls = store.execCallCount(ref.ID)
	replacementContent := []byte("remote replacement\n")
	replacement := testFileResourceMount(mount.RuntimePath, replacementContent)
	replacement.Identity = "sesrsc_remote_second"
	if err := resources.ImportFileResource(
		ctx, replacement, &readerThatFails{data: []byte("partial")},
	); err == nil {
		t.Fatal("partial replacement unexpectedly succeeded")
	}
	if err := resources.RemoveFileResource(ctx, mount.RuntimePath, mount.Identity); err != nil {
		t.Fatalf("stale removal during interrupted replacement: %v", err)
	}
	partial, err := box.ReadFile(ctx, mount.RuntimePath)
	if err != nil || string(partial) != "partial" {
		t.Fatalf("stale removal changed pending replacement = %q, %v", partial, err)
	}
	if err := resources.RemoveFileResource(
		ctx, replacement.RuntimePath, replacement.Identity,
	); err != nil {
		t.Fatalf("remove interrupted replacement: %v", err)
	}
	if calls := store.execCallCount(ref.ID); calls != execCalls {
		t.Fatalf(
			"failed File Resource import used Exec: got %d call(s), want %d",
			calls, execCalls,
		)
	}
	execCalls = store.execCallCount(ref.ID)
	if err := resources.ImportFileResource(
		ctx, replacement, bytes.NewReader(replacementContent),
	); err != nil {
		t.Fatal(err)
	}
	if err := resources.RemoveFileResource(ctx, mount.RuntimePath, mount.Identity); err != nil {
		t.Fatalf("stale removal: %v", err)
	}
	if calls := store.execCallCount(ref.ID); calls != execCalls {
		t.Fatalf(
			"File Resource replacement used Exec: got %d call(s), want %d",
			calls, execCalls,
		)
	}
	assertFakeRemoteResource(t, ctx, box, resources, replacement, replacementContent)

	attached, err := open().Attach(ctx, sessionKey, ref, spec)
	if err != nil {
		t.Fatal(err)
	}
	attachedResources := attached.(FileResourceSandbox)
	assertFakeRemoteResource(
		t, ctx, attached, attachedResources, replacement, replacementContent,
	)
	if err := attachedResources.RemoveFileResource(
		ctx, replacement.RuntimePath, replacement.Identity,
	); err != nil {
		t.Fatal(err)
	}
	if err := attachedResources.RemoveFileResource(
		ctx, replacement.RuntimePath, replacement.Identity,
	); err != nil {
		t.Fatalf("idempotent removal: %v", err)
	}
}

func assertFakeRemoteResource(
	t *testing.T,
	ctx context.Context,
	box Sandbox,
	resources FileResourceSandbox,
	mount FileResourceMount,
	want []byte,
) {
	t.Helper()
	present, err := resources.HasFileResource(ctx, mount)
	if err != nil || !present {
		t.Fatalf("HasFileResource = %t, %v", present, err)
	}
	got, err := box.ReadFile(ctx, mount.RuntimePath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile mounted resource = %q, %v", got, err)
	}
	result, err := box.Exec(ctx, Command{
		Path: "/bin/sh", Args: []string{"-c", "cat " + mount.RuntimePath},
	})
	if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stdout, want) {
		t.Fatalf("shell read mounted resource: result=%+v err=%v", result, err)
	}
}

func TestDaytonaResourcesAcceptDocumentedSpecFormat(t *testing.T) {
	resources := daytonaResources(Spec{CPUs: "1.0", Memory: "512m"})
	if resources == nil || resources.CPU != 1 || resources.Memory != 512 {
		t.Fatalf("resources = %+v, want CPU=1 Memory=512", resources)
	}
}

func runFakeRemoteContract(t *testing.T, open func() Provider) {
	t.Helper()
	ctx := context.Background()
	spec := Spec{Timeout: 5 * time.Second}
	sessionKey := "sesn-" + strings.NewReplacer("/", "-", " ", "-").
		Replace(strings.ToLower(t.Name()))

	firstProvider := open()
	ref, first, err := firstProvider.Create(ctx, sessionKey, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Destroy(context.Background()) })
	if ref.Provider != firstProvider.Name() || ref.ID == "" {
		t.Fatalf("invalid durable reference: %+v", ref)
	}
	content := []byte{'d', 'u', 'r', 'a', 'b', 'l', 'e', 0, '\n'}
	if err := first.WriteFile(ctx, "nested/state.bin", content); err != nil {
		t.Fatal(err)
	}
	got, err := first.ReadFile(ctx, "nested/state.bin")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("file round trip = %q, %v", got, err)
	}
	result, err := first.Exec(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "printf conformance-exec"},
	})
	if err != nil || result.ExitCode != 0 ||
		string(result.Stdout) != "conformance-exec" {
		t.Fatalf("Exec result = %+v, %v", result, err)
	}
	result, err = first.Exec(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 7"},
	})
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("non-zero Exec result = %+v, %v", result, err)
	}
	if _, err := first.ReadFile(ctx, "../escape"); err == nil {
		t.Fatal("ReadFile accepted a path outside the workspace")
	}
	if err := first.WriteFile(ctx, "../escape", []byte("x")); err == nil {
		t.Fatal("WriteFile accepted a path outside the workspace")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := first.Exec(cancelled, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "true"},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Exec = %v, want context.Canceled", err)
	}

	restarted := open()
	sameRef, same, err := restarted.Create(ctx, sessionKey, spec)
	if err != nil {
		t.Fatal(err)
	}
	if sameRef != ref {
		t.Fatalf("repeated Create ref = %+v, want %+v", sameRef, ref)
	}
	got, err = same.ReadFile(ctx, "nested/state.bin")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("repeated Create lost workspace: %q, %v", got, err)
	}
	attached, err := restarted.Attach(ctx, sessionKey, ref, spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err = attached.ReadFile(ctx, "nested/state.bin")
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("Attach lost workspace: %q, %v", got, err)
	}
	if _, err := restarted.Attach(
		ctx,
		sessionKey+"-other",
		ref,
		spec,
	); err == nil || !IsPermanent(err) {
		t.Fatalf("cross-session Attach = %v, want permanent error", err)
	}
	if _, err := restarted.Attach(
		ctx,
		sessionKey,
		Ref{Provider: "wrong-provider", ID: ref.ID},
		spec,
	); err == nil || !IsPermanent(err) {
		t.Fatalf("wrong-provider Attach = %v, want permanent error", err)
	}
	if err := first.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Destroy(ctx); err != nil {
		t.Fatalf("repeated Destroy: %v", err)
	}
	if _, err := open().Attach(
		ctx,
		sessionKey,
		ref,
		spec,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Attach after Destroy = %v, want ErrNotFound", err)
	}
}

type fakeRemoteStore struct {
	mu        sync.Mutex
	baseDir   string
	nextID    int
	resources map[string]*fakeRemoteResource
	names     map[string]string
}

type fakeRemoteResource struct {
	id              string
	name            string
	metadata        map[string]string
	execCalls       int
	lastExecTimeout time.Duration
	root            string
	destroyed       bool
}

func newFakeRemoteStore(baseDir string) *fakeRemoteStore {
	return &fakeRemoteStore{
		baseDir:   baseDir,
		resources: map[string]*fakeRemoteResource{},
		names:     map[string]string{},
	}
}

func (s *fakeRemoteStore) create(
	name string,
	metadata map[string]string,
) *fakeRemoteResource {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	id := fmt.Sprintf("sbx-%04d", s.nextID)
	root := filepath.Join(s.baseDir, id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		panic(err)
	}
	resource := &fakeRemoteResource{
		id:       id,
		name:     name,
		metadata: cloneStringMap(metadata),
		root:     root,
	}
	s.resources[id] = resource
	if name != "" {
		s.names[name] = id
	}
	return resource
}

func (s *fakeRemoteStore) list() []*fakeRemoteResource {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*fakeRemoteResource, 0, len(s.resources))
	for _, resource := range s.resources {
		if !resource.destroyed {
			items = append(items, resource)
		}
	}
	return items
}

func (s *fakeRemoteStore) get(idOrName string) (*fakeRemoteResource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.names[idOrName]; ok {
		idOrName = id
	}
	resource, ok := s.resources[idOrName]
	if !ok || resource.destroyed {
		return nil, errFakeRemoteNotFound
	}
	return resource, nil
}

func (s *fakeRemoteStore) execCallCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	resource := s.resources[id]
	if resource == nil {
		return 0
	}
	return resource.execCalls
}

func (s *fakeRemoteStore) recordExecTimeout(id string, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resource := s.resources[id]; resource != nil {
		resource.lastExecTimeout = timeout
	}
}

func (s *fakeRemoteStore) lastExecTimeout(id string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resource := s.resources[id]; resource != nil {
		return resource.lastExecTimeout
	}
	return 0
}

func (s *fakeRemoteStore) destroy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resource, ok := s.resources[id]
	if !ok || resource.destroyed {
		return errFakeRemoteNotFound
	}
	resource.destroyed = true
	_ = filepath.Walk(resource.root, func(
		filePath string,
		info os.FileInfo,
		err error,
	) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(filePath, 0o700)
		} else {
			_ = os.Chmod(filePath, 0o600)
		}
		return nil
	})
	return os.RemoveAll(resource.root)
}

var errFakeRemoteNotFound = errors.New("fake remote not found")

func cloneStringMap(value map[string]string) map[string]string {
	cloned := make(map[string]string, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

type fakeRemoteHandle struct {
	store    *fakeRemoteStore
	resource *fakeRemoteResource
}

func (h *fakeRemoteHandle) ID() string { return h.resource.id }

func (h *fakeRemoteHandle) exec(
	ctx context.Context,
	command string,
) (string, string, int, error) {
	h.store.mu.Lock()
	h.resource.execCalls++
	h.store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", "", -1, err
	}
	if strings.HasPrefix(command, "mkdir -p ") {
		return "", "", 0, nil
	}
	uploads, _ := h.fullPath(SessionUploadsRoot)
	command = strings.ReplaceAll(command, SessionUploadsRoot, uploads)
	outputs, _ := h.fullPath(SessionOutputsRoot)
	command = strings.ReplaceAll(command, SessionOutputsRoot, outputs)
	outputControl, _ := h.fullPath(remoteSessionOutputControlRoot)
	command = strings.ReplaceAll(command, remoteSessionOutputControlRoot, outputControl)
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Dir = h.resource.root
	process.Env = []string{"PATH=/usr/bin:/bin", "COPYFILE_DISABLE=1"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = &stderr
	err := process.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), -1, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), process.ProcessState.ExitCode(), nil
}

func (h *fakeRemoteHandle) readFile(value string) ([]byte, error) {
	relative, err := fakeRelativePath(value)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(h.resource.root, filepath.FromSlash(relative)))
}

func (h *fakeRemoteHandle) writeFile(value string, data []byte) error {
	relative, err := fakeRelativePath(value)
	if err != nil {
		return err
	}
	full := filepath.Join(h.resource.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o600)
}

func (h *fakeRemoteHandle) fullPath(value string) (string, error) {
	relative, err := fakeRelativePath(value)
	if err != nil {
		return "", err
	}
	return filepath.Join(h.resource.root, filepath.FromSlash(relative)), nil
}

func (h *fakeRemoteHandle) ResourceCreateDirectory(
	_ context.Context,
	directory string,
	permissions remoteFilePermissions,
) error {
	full, err := h.fullPath(directory)
	if err != nil {
		return err
	}
	if err := fakePrivilegedMkdirAll(h.resource.root, full); err != nil {
		return err
	}
	if err := os.Chmod(full, os.FileMode(permissions.Mode)); err != nil {
		return err
	}
	return nil
}

func fakePrivilegedMkdirAll(root string, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("fake remote directory escapes resource root")
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		parentInfo, err := os.Stat(current)
		if err != nil {
			return err
		}
		parentMode := parentInfo.Mode().Perm()
		if parentMode&0o200 == 0 {
			if err := os.Chmod(current, parentMode|0o200); err != nil {
				return err
			}
		}
		next := filepath.Join(current, component)
		mkdirErr := os.Mkdir(next, 0o755)
		if parentMode&0o200 == 0 {
			if err := os.Chmod(current, parentMode); err != nil {
				return err
			}
		}
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return mkdirErr
		}
		current = next
	}
	return nil
}

func (h *fakeRemoteHandle) ResourceUpload(
	_ context.Context,
	filePath string,
	content io.Reader,
	permissions remoteFilePermissions,
) error {
	full, err := h.fullPath(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(permissions.Mode))
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, content)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(full, os.FileMode(permissions.Mode)); err != nil {
		return err
	}
	return nil
}

func (h *fakeRemoteHandle) ResourceOpen(
	_ context.Context,
	filePath string,
) (io.ReadCloser, error) {
	full, err := h.fullPath(filePath)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (h *fakeRemoteHandle) ResourceStat(
	_ context.Context,
	filePath string,
) (remoteFileInfo, error) {
	full, err := h.fullPath(filePath)
	if err != nil {
		return remoteFileInfo{}, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return remoteFileInfo{}, err
	}
	return remoteFileInfo{
		SizeBytes: info.Size(),
		Regular:   info.Mode().IsRegular(),
		Directory: info.IsDir(),
	}, nil
}

func (h *fakeRemoteHandle) ResourceRemoveFile(
	_ context.Context,
	filePath string,
) error {
	full, err := h.fullPath(filePath)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

func (*fakeRemoteHandle) ResourceIsNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, errFakeRemoteNotFound)
}

func fakeRelativePath(value string) (string, error) {
	for _, root := range []string{remoteDefaultRoot, defaultDaytonaRoot} {
		if value == root {
			return ".", nil
		}
		if strings.HasPrefix(value, root+"/") {
			return strings.TrimPrefix(value, root+"/"), nil
		}
	}
	if path.IsAbs(value) {
		return strings.TrimPrefix(path.Clean(value), "/"), nil
	}
	return "", fmt.Errorf("unexpected fake remote path %q", value)
}

type fakeE2BService struct {
	store *fakeRemoteStore
}

func (s *fakeE2BService) List(
	_ context.Context,
	metadata map[string]string,
) ([]e2bResource, error) {
	items := s.store.list()
	resources := make([]e2bResource, 0, len(items))
	for _, item := range items {
		matches := true
		for key, value := range metadata {
			if item.metadata[key] != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		resources = append(resources, e2bResource{
			id:       item.id,
			metadata: cloneStringMap(item.metadata),
		})
	}
	return resources, nil
}

func (s *fakeE2BService) Get(
	_ context.Context,
	id string,
) (e2bResource, error) {
	resource, err := s.store.get(id)
	if err != nil {
		if errors.Is(err, errFakeRemoteNotFound) {
			return e2bResource{}, cubesandbox.ErrSandboxNotFound
		}
		return e2bResource{}, err
	}
	return e2bResource{
		id:       resource.id,
		metadata: cloneStringMap(resource.metadata),
	}, nil
}

func (s *fakeE2BService) Create(
	_ context.Context,
	sessionKey string,
	_ Spec,
) (e2bServiceSandbox, error) {
	resource := s.store.create("", remoteMetadata(sessionKey))
	return &fakeE2BRemote{
		fakeRemoteHandle: fakeRemoteHandle{store: s.store, resource: resource},
	}, nil
}

func (s *fakeE2BService) Connect(
	_ context.Context,
	id string,
) (e2bServiceSandbox, error) {
	resource, err := s.store.get(id)
	if err != nil {
		return nil, err
	}
	return &fakeE2BRemote{
		fakeRemoteHandle: fakeRemoteHandle{store: s.store, resource: resource},
	}, nil
}

type fakeE2BRemote struct {
	fakeRemoteHandle
}

func (s *fakeE2BRemote) Exec(
	ctx context.Context,
	command string,
	_ string,
	timeout time.Duration,
) (string, string, int, error) {
	s.store.recordExecTimeout(s.resource.id, timeout)
	return s.exec(ctx, command)
}

func (s *fakeE2BRemote) ReadFile(
	_ context.Context,
	value string,
) ([]byte, error) {
	return s.readFile(value)
}

func (s *fakeE2BRemote) WriteFile(
	_ context.Context,
	value string,
	data []byte,
) error {
	return s.writeFile(value, data)
}

func (s *fakeE2BRemote) Destroy(context.Context) error {
	err := s.store.destroy(s.resource.id)
	if errors.Is(err, errFakeRemoteNotFound) {
		return cubesandbox.ErrSandboxNotFound
	}
	return err
}

type fakeOpenSandboxService struct {
	store *fakeRemoteStore
}

func (s *fakeOpenSandboxService) List(
	_ context.Context,
	metadata map[string]string,
) ([]openSandboxResource, error) {
	items := s.store.list()
	resources := make([]openSandboxResource, 0, len(items))
	for _, item := range items {
		matches := true
		for key, value := range metadata {
			if item.metadata[key] != value {
				matches = false
			}
		}
		if matches {
			resources = append(resources, openSandboxResource{
				id:       item.id,
				metadata: cloneStringMap(item.metadata),
			})
		}
	}
	return resources, nil
}

func (s *fakeOpenSandboxService) Get(
	_ context.Context,
	id string,
) (openSandboxResource, error) {
	resource, err := s.store.get(id)
	if err != nil {
		if errors.Is(err, errFakeRemoteNotFound) {
			return openSandboxResource{}, &opensandbox.APIError{
				StatusCode: 404,
			}
		}
		return openSandboxResource{}, err
	}
	return openSandboxResource{
		id:       resource.id,
		metadata: cloneStringMap(resource.metadata),
	}, nil
}

func (s *fakeOpenSandboxService) Create(
	_ context.Context,
	sessionKey string,
	_ Spec,
) (openSandboxRemote, error) {
	resource := s.store.create("", remoteMetadata(sessionKey))
	return &fakeOpenSandboxRemote{
		fakeRemoteHandle: fakeRemoteHandle{store: s.store, resource: resource},
	}, nil
}

func (s *fakeOpenSandboxService) Connect(
	_ context.Context,
	id string,
) (openSandboxRemote, error) {
	resource, err := s.store.get(id)
	if err != nil {
		return nil, err
	}
	return &fakeOpenSandboxRemote{
		fakeRemoteHandle: fakeRemoteHandle{store: s.store, resource: resource},
	}, nil
}

type fakeOpenSandboxRemote struct {
	fakeRemoteHandle
}

func (s *fakeOpenSandboxRemote) Exec(
	ctx context.Context,
	command string,
	_ string,
	timeout time.Duration,
) (string, string, int, error) {
	s.store.recordExecTimeout(s.resource.id, timeout)
	return s.exec(ctx, command)
}

func (s *fakeOpenSandboxRemote) ReadFile(
	_ context.Context,
	value string,
) ([]byte, error) {
	return s.readFile(value)
}

func (s *fakeOpenSandboxRemote) WriteFile(
	_ context.Context,
	value string,
	data []byte,
) error {
	return s.writeFile(value, data)
}

func (*fakeOpenSandboxRemote) ApplyLimitedNetwork(context.Context, []string) error {
	return nil
}

func (s *fakeOpenSandboxRemote) Destroy(context.Context) error {
	err := s.store.destroy(s.resource.id)
	if errors.Is(err, errFakeRemoteNotFound) {
		return &opensandbox.APIError{StatusCode: 404}
	}
	return err
}

type fakeDaytonaService struct {
	store *fakeRemoteStore
}

func (s *fakeDaytonaService) Get(
	_ context.Context,
	idOrName string,
) (daytonaResource, error) {
	resource, err := s.store.get(idOrName)
	if err != nil {
		if errors.Is(err, errFakeRemoteNotFound) {
			return daytonaResource{}, daytonaerrors.ErrNotFound
		}
		return daytonaResource{}, err
	}
	return fakeDaytonaResource(s.store, resource), nil
}

func (s *fakeDaytonaService) Create(
	_ context.Context,
	name string,
	sessionKey string,
	_ Spec,
) (daytonaResource, error) {
	resource := s.store.create(name, remoteMetadata(sessionKey))
	return fakeDaytonaResource(s.store, resource), nil
}

func fakeDaytonaResource(
	store *fakeRemoteStore,
	resource *fakeRemoteResource,
) daytonaResource {
	return daytonaResource{
		id:     resource.id,
		labels: cloneStringMap(resource.metadata),
		remote: &fakeDaytonaRemote{
			fakeRemoteHandle: fakeRemoteHandle{store: store, resource: resource},
		},
	}
}

type fakeDaytonaRemote struct{ fakeRemoteHandle }

func (s *fakeDaytonaRemote) Exec(
	ctx context.Context,
	command string,
	_ string,
	timeout time.Duration,
) (string, string, int, error) {
	s.store.recordExecTimeout(s.resource.id, timeout)
	return s.exec(ctx, command)
}

func (s *fakeDaytonaRemote) ReadFile(
	_ context.Context,
	value string,
) ([]byte, error) {
	return s.readFile(value)
}

func (s *fakeDaytonaRemote) WriteFile(
	_ context.Context,
	value string,
	data []byte,
) error {
	return s.writeFile(value, data)
}

func (s *fakeDaytonaRemote) MakeDirectory(
	_ context.Context,
	directory string,
) error {
	return s.ResourceCreateDirectory(context.Background(), directory, remoteFilePermissions{
		Mode: 0o755,
	})
}

func (s *fakeDaytonaRemote) Start(context.Context) error {
	_, err := s.store.get(s.resource.id)
	return err
}

func (s *fakeDaytonaRemote) Destroy(context.Context) error {
	err := s.store.destroy(s.resource.id)
	if errors.Is(err, errFakeRemoteNotFound) {
		return daytonaerrors.ErrNotFound
	}
	return err
}
