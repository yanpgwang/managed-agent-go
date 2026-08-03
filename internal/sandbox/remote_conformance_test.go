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
	id        string
	name      string
	metadata  map[string]string
	root      string
	destroyed bool
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

func (s *fakeRemoteStore) destroy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resource, ok := s.resources[id]
	if !ok || resource.destroyed {
		return errFakeRemoteNotFound
	}
	resource.destroyed = true
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
	if err := ctx.Err(); err != nil {
		return "", "", -1, err
	}
	if strings.HasPrefix(command, "mkdir -p ") {
		return "", "", 0, nil
	}
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Dir = h.resource.root
	process.Env = []string{"PATH=/usr/bin:/bin"}
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

func fakeRelativePath(value string) (string, error) {
	for _, root := range []string{remoteDefaultRoot, defaultDaytonaRoot} {
		if value == root {
			return ".", nil
		}
		if strings.HasPrefix(value, root+"/") {
			return strings.TrimPrefix(value, root+"/"), nil
		}
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
	_ time.Duration,
) (string, string, int, error) {
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
	_ time.Duration,
) (string, string, int, error) {
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

type fakeDaytonaRemote struct {
	fakeRemoteHandle
}

func (s *fakeDaytonaRemote) Exec(
	ctx context.Context,
	command string,
	_ string,
	_ time.Duration,
) (string, string, int, error) {
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
