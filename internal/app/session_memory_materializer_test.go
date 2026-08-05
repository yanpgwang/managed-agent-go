package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

type sessionMemoryResourceRepositoryStub struct {
	resources []domain.SessionResource
	finalized []string
}

func (r *sessionMemoryResourceRepositoryStub) SessionResourcesForReconcile(
	context.Context,
	string,
) ([]domain.SessionResource, error) {
	return append([]domain.SessionResource(nil), r.resources...), nil
}

func (r *sessionMemoryResourceRepositoryStub) FinalizeSessionResourceDeletion(
	_ context.Context,
	_ string,
	resourceID string,
) error {
	r.finalized = append(r.finalized, resourceID)
	return nil
}

func TestSessionMemoryMaterializer_MountsForReleaseIncludeDeleting(t *testing.T) {
	repository := &sessionMemoryResourceRepositoryStub{resources: []domain.SessionResource{
		{
			ID: "sesrsc_active", ResourceType: domain.SessionResourceTypeMemoryStore,
			MemoryStoreID: "memstore_active", MountPath: "/mnt/memory/active",
			MemoryAccess: domain.MemoryAccessReadWrite, State: domain.SessionResourceActive,
		},
		{
			ID: "sesrsc_deleting", ResourceType: domain.SessionResourceTypeMemoryStore,
			MemoryStoreID: "memstore_deleting", MountPath: "/mnt/memory/deleting",
			MemoryAccess: domain.MemoryAccessReadOnly, State: domain.SessionResourceDeleting,
		},
		{ID: "sesrsc_file", ResourceType: domain.SessionResourceTypeFile},
	}}
	materializer := NewSessionMemoryMaterializer(repository, nil)

	mounts, err := materializer.MemoryStoreMountsForRelease(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	want := []sandbox.MemoryStoreMount{
		{
			Identity: "sesrsc_active", StoreID: "memstore_active",
			RuntimePath: "/mnt/memory/active", Access: domain.MemoryAccessReadWrite,
		},
		{
			Identity: "sesrsc_deleting", StoreID: "memstore_deleting",
			RuntimePath: "/mnt/memory/deleting", Access: domain.MemoryAccessReadOnly,
		},
	}
	if !reflect.DeepEqual(mounts, want) {
		t.Fatalf("mounts = %#v, want %#v", mounts, want)
	}
}

func TestSessionMemoryMaterializer_CleanupRequiresPreparedDeletion(t *testing.T) {
	repository := &sessionMemoryResourceRepositoryStub{resources: []domain.SessionResource{
		{
			ID: "sesrsc_active", ResourceType: domain.SessionResourceTypeMemoryStore,
			State: domain.SessionResourceActive,
		},
	}}
	materializer := NewSessionMemoryMaterializer(repository, nil)
	if err := materializer.CleanupSession(context.Background(), "session"); err == nil {
		t.Fatal("CleanupSession succeeded for an active Memory Store attachment")
	}
	if len(repository.finalized) != 0 {
		t.Fatalf("finalized active resources: %v", repository.finalized)
	}

	repository.resources[0].State = domain.SessionResourceDeleting
	if err := materializer.CleanupSession(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.finalized, []string{"sesrsc_active"}) {
		t.Fatalf("finalized = %v", repository.finalized)
	}
}
