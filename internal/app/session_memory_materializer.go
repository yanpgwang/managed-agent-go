package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

type SessionMemoryResourceRepository interface {
	SessionResourcesForReconcile(context.Context, string) ([]domain.SessionResource, error)
	FinalizeSessionResourceDeletion(context.Context, string, string) error
}

// SessionMemoryMaterializer synchronizes the filesystem representation of each
// attached Memory Store with PostgreSQL before and after every sandbox tool.
// The provider owns mount isolation; this layer owns versioning and optimistic
// concurrency semantics.
type SessionMemoryMaterializer struct {
	resources SessionMemoryResourceRepository
	memory    *MemoryService
}

func NewSessionMemoryMaterializer(
	resources SessionMemoryResourceRepository,
	memory *MemoryService,
) *SessionMemoryMaterializer {
	return &SessionMemoryMaterializer{resources: resources, memory: memory}
}

func (m *SessionMemoryMaterializer) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	return m.converge(ctx, sessionID, box, false, true)
}

func (m *SessionMemoryMaterializer) Writeback(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	return m.converge(ctx, sessionID, box, false, false)
}

func (m *SessionMemoryMaterializer) WritebackForRelease(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	return m.converge(ctx, sessionID, box, true, false)
}

func (m *SessionMemoryMaterializer) converge(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
	includeDeleting bool,
	trySync bool,
) error {
	resources, err := m.resources.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	var memoryResources []domain.SessionResource
	for _, resource := range resources {
		if resource.Type() == domain.SessionResourceTypeMemoryStore &&
			(resource.State == domain.SessionResourceActive ||
				includeDeleting && resource.State == domain.SessionResourceDeleting) {
			memoryResources = append(memoryResources, resource)
		}
	}
	if len(memoryResources) == 0 {
		return nil
	}
	mounter, supported := box.(sandbox.MemoryStoreSandbox)
	if !supported {
		return sandbox.Permanent(fmt.Errorf(
			"sandbox: provider cannot materialize Memory Stores for Session %s",
			sessionID,
		))
	}
	if locker, ok := box.(sandbox.ResourceSynchronizationSandbox); ok {
		var unlock func()
		if trySync {
			lockedCtx, release, acquired, err := locker.TryLockResourceSync(ctx)
			if err != nil {
				return err
			}
			if !acquired {
				return nil
			}
			ctx, unlock = lockedCtx, release
		} else {
			lockedCtx, release, err := locker.LockResourceSync(ctx)
			if err != nil {
				return err
			}
			ctx, unlock = lockedCtx, release
		}
		defer unlock()
	}
	for _, resource := range memoryResources {
		mount := sandbox.MemoryStoreMount{
			Identity:    resource.ID,
			StoreID:     resource.MemoryStoreID,
			RuntimePath: resource.MountPath,
			Access:      resource.MemoryAccess,
		}
		snapshot, err := mounter.ReadMemoryStore(ctx, mount)
		if err != nil {
			return err
		}
		var heads []domain.Memory
		if snapshot.Initialized && resource.MemoryAccess == domain.MemoryAccessReadWrite {
			baseline := make([]MemoryStoreSyncBaseline, 0, len(snapshot.Baseline))
			for _, item := range snapshot.Baseline {
				baseline = append(baseline, MemoryStoreSyncBaseline{
					MemoryID:      item.MemoryID,
					Path:          item.Path,
					ContentSHA256: item.ContentSHA256,
				})
			}
			current := make([]MemoryStoreSyncContent, 0, len(snapshot.Current))
			for _, item := range snapshot.Current {
				current = append(current, MemoryStoreSyncContent{
					Path:    item.Path,
					Content: string(item.Content),
				})
			}
			heads, err = m.memory.SyncRuntimeSnapshot(
				ctx,
				resource.MemoryStoreID,
				baseline,
				current,
				domain.MemoryActor{Type: "session_actor", ID: sessionID},
			)
		} else {
			heads, err = m.memory.RuntimeHeads(ctx, resource.MemoryStoreID)
		}
		if err != nil {
			var domainErr *domain.DomainError
			if snapshot.Initialized && errors.As(err, &domainErr) {
				// A stale local diff must not fence every later tool. Restore the
				// authoritative remote snapshot after reporting this writeback error.
				remote, readErr := m.memory.RuntimeHeads(ctx, resource.MemoryStoreID)
				if readErr == nil {
					_ = mounter.ReplaceMemoryStore(
						ctx, mount, sandboxMemoryFiles(remote),
					)
				}
			}
			return err
		}
		files := sandboxMemoryFiles(heads)
		if err := mounter.ReplaceMemoryStore(ctx, mount, files); err != nil {
			return err
		}
	}
	return nil
}

func (m *SessionMemoryMaterializer) MemoryStoreMountsForRelease(
	ctx context.Context,
	sessionID string,
) ([]sandbox.MemoryStoreMount, error) {
	resources, err := m.resources.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	mounts := make([]sandbox.MemoryStoreMount, 0)
	for _, resource := range resources {
		if resource.Type() != domain.SessionResourceTypeMemoryStore {
			continue
		}
		mounts = append(mounts, sandbox.MemoryStoreMount{
			Identity:    resource.ID,
			StoreID:     resource.MemoryStoreID,
			RuntimePath: resource.MountPath,
			Access:      resource.MemoryAccess,
		})
	}
	return mounts, nil
}

func sandboxMemoryFiles(heads []domain.Memory) []sandbox.MemoryStoreFile {
	files := make([]sandbox.MemoryStoreFile, 0, len(heads))
	for _, head := range heads {
		files = append(files, sandbox.MemoryStoreFile{
			MemoryID:      head.ID,
			Path:          head.Path,
			Content:       []byte(head.Content),
			ContentSHA256: head.ContentSHA256,
		})
	}
	return files
}

// CleanupSession removes only durable attachment rows after the whole sandbox
// has been destroyed. Store contents remain globally durable by design.
func (m *SessionMemoryMaterializer) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	resources, err := m.resources.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Type() != domain.SessionResourceTypeMemoryStore {
			continue
		}
		if resource.State != domain.SessionResourceDeleting {
			return fmt.Errorf(
				"memory store Session Resource %s is not prepared for deletion",
				resource.ID,
			)
		}
		if err := m.resources.FinalizeSessionResourceDeletion(
			ctx, sessionID, resource.ID,
		); err != nil {
			return err
		}
	}
	return nil
}
