package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/workspace"
)

type fakeDeletionStore struct {
	mu          sync.Mutex
	pending     []string
	finalized   []string
	finalizeErr map[string]error
}

func (s *fakeDeletionStore) GetSession(_ context.Context, sessionID string) (domain.Session, error) {
	return domain.Session{ID: sessionID, WorkspaceID: workspace.DefaultID}, nil
}

func (s *fakeDeletionStore) ListDeletingSessionIDs(
	_ context.Context,
	limit int,
) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) < limit {
		limit = len(s.pending)
	}
	return append([]string(nil), s.pending[:limit]...), nil
}

func (s *fakeDeletionStore) FinalizeSessionDeletion(
	_ context.Context,
	sessionID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.finalizeErr[sessionID]; err != nil {
		return err
	}
	s.finalized = append(s.finalized, sessionID)
	for i, pending := range s.pending {
		if pending == sessionID {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			break
		}
	}
	return nil
}

type fakeSessionTerminator struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (t *fakeSessionTerminator) TerminateSession(
	_ context.Context,
	sessionID string,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, sessionID)
	return t.fail[sessionID]
}

type fakeProvisioningReconciler struct {
	completed int
	err       error
	calls     int
	limit     int
}

type fakeSessionResourceDeletionReconciler struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (r *fakeSessionResourceDeletionReconciler) CleanupSession(
	_ context.Context,
	sessionID string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sessionID)
	return r.fail[sessionID]
}

func (r *fakeProvisioningReconciler) ReconcileProvisioning(
	_ context.Context,
	limit int,
) (int, error) {
	r.calls++
	r.limit = limit
	return r.completed, r.err
}

func TestLifecycleReconciler_ResumesFencedDeletion(t *testing.T) {
	store := &fakeDeletionStore{pending: []string{"sesn_a", "sesn_b"}}
	terminator := &fakeSessionTerminator{}
	provisioning := &fakeProvisioningReconciler{completed: 1}
	reconciler := NewLifecycleReconciler(
		store,
		terminator,
		provisioning,
		LifecycleReconcilerConfig{BatchSize: 10, AttemptTimeout: time.Second},
	)

	result, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if result.Provisioning != 1 || result.Deletions != 2 {
		t.Fatalf("result = %+v, want provisioning=1 deletions=2", result)
	}
	if got := terminator.calls; !equalStrings(got, []string{"sesn_a", "sesn_b"}) {
		t.Fatalf("cleanup calls = %v", got)
	}
	if got := store.finalized; !equalStrings(got, []string{"sesn_a", "sesn_b"}) {
		t.Fatalf("finalized = %v", got)
	}
	if provisioning.calls != 1 || provisioning.limit != 10 {
		t.Fatalf("provisioning calls=%d limit=%d", provisioning.calls, provisioning.limit)
	}
}

func TestLifecycleReconciler_FailureStaysDurableAndDoesNotBlockBatch(t *testing.T) {
	store := &fakeDeletionStore{pending: []string{"sesn_stuck", "sesn_ready"}}
	terminator := &fakeSessionTerminator{
		fail: map[string]error{"sesn_stuck": errors.New("provider unavailable")},
	}
	reconciler := NewLifecycleReconciler(
		store,
		terminator,
		nil,
		LifecycleReconcilerConfig{BatchSize: 10, AttemptTimeout: time.Second},
	)

	result, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("run once succeeded despite cleanup failure")
	}
	if result.Deletions != 1 {
		t.Fatalf("completed deletions = %d, want 1", result.Deletions)
	}
	if !equalStrings(store.finalized, []string{"sesn_ready"}) {
		t.Fatalf("finalized = %v, want ready session only", store.finalized)
	}
	if !equalStrings(store.pending, []string{"sesn_stuck"}) {
		t.Fatalf("pending = %v, want stuck session retained", store.pending)
	}

	delete(terminator.fail, "sesn_stuck")
	result, err = reconciler.RunOnce(context.Background())
	if err != nil || result.Deletions != 1 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if len(store.pending) != 0 {
		t.Fatalf("pending after recovery = %v", store.pending)
	}
}

func TestLifecycleReconciler_ResourceCleanupFailurePreventsFinalization(t *testing.T) {
	store := &fakeDeletionStore{pending: []string{"sesn_resource"}}
	terminator := &fakeSessionTerminator{}
	resources := &fakeSessionResourceDeletionReconciler{
		fail: map[string]error{"sesn_resource": errors.New("object store unavailable")},
	}
	reconciler := NewLifecycleReconciler(
		store,
		terminator,
		nil,
		LifecycleReconcilerConfig{AttemptTimeout: time.Second},
		resources,
	)

	result, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("run once succeeded despite File Resource cleanup failure")
	}
	if result.Deletions != 0 || len(store.finalized) != 0 {
		t.Fatalf("deletion finalized early: result=%+v finalized=%v", result, store.finalized)
	}
	if !equalStrings(resources.calls, []string{"sesn_resource"}) ||
		!equalStrings(store.pending, []string{"sesn_resource"}) {
		t.Fatalf("cleanup calls=%v pending=%v", resources.calls, store.pending)
	}

	delete(resources.fail, "sesn_resource")
	result, err = reconciler.RunOnce(context.Background())
	if err != nil || result.Deletions != 1 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
	if !equalStrings(resources.calls, []string{"sesn_resource", "sesn_resource"}) ||
		!equalStrings(store.finalized, []string{"sesn_resource"}) {
		t.Fatalf("cleanup calls=%v finalized=%v", resources.calls, store.finalized)
	}
}

func TestLifecycleReconciler_ProvisioningFailureDoesNotBlockDeletion(t *testing.T) {
	store := &fakeDeletionStore{pending: []string{"sesn_delete"}}
	terminator := &fakeSessionTerminator{}
	provisioning := &fakeProvisioningReconciler{
		err: errors.New("sandbox service unavailable"),
	}
	reconciler := NewLifecycleReconciler(
		store,
		terminator,
		provisioning,
		LifecycleReconcilerConfig{AttemptTimeout: time.Second},
	)

	result, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("run once hid provisioning failure")
	}
	if result.Deletions != 1 || !equalStrings(store.finalized, []string{"sesn_delete"}) {
		t.Fatalf("deletion did not progress: result=%+v finalized=%v", result, store.finalized)
	}
}

func TestLifecycleReconciler_DrainDelayHasPositiveFloor(t *testing.T) {
	reconciler := NewLifecycleReconciler(
		nil,
		nil,
		nil,
		LifecycleReconcilerConfig{PollInterval: 5 * time.Second},
	)

	if got := reconciler.nextDelay(LifecycleReconcileResult{Provisioning: 1}); got != lifecycleDrainDelay {
		t.Fatalf("active delay = %v, want %v", got, lifecycleDrainDelay)
	}
	if got := reconciler.nextDelay(LifecycleReconcileResult{}); got != 5*time.Second {
		t.Fatalf("idle delay = %v, want 5s", got)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
