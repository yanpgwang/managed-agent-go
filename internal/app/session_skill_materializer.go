package app

import (
	"context"
	"fmt"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

// SessionSkillMountRepository exposes the immutable Version metadata retained
// by a Session's relational pins.
type SessionSkillMountRepository interface {
	SessionSkillsForRuntime(context.Context, string) ([]domain.SkillVersion, error)
}

type SessionThreadSkillMountRepository interface {
	SessionThreadSkillRuntime(
		context.Context,
		string,
		string,
	) (domain.SkillRuntime, error)
}

// SessionSkillMaterializer converges canonical custom Skill archives into the
// provider's read-only runtime tree. Version pins are immutable for a Session,
// so reconciliation only needs idempotent presence/repair, not per-Skill delete.
type SessionSkillMaterializer struct {
	skills SessionSkillMountRepository
	blobs  BlobStore
}

func NewSessionSkillMaterializer(
	skills SessionSkillMountRepository,
	blobs BlobStore,
) *SessionSkillMaterializer {
	return &SessionSkillMaterializer{skills: skills, blobs: blobs}
}

// SupportsSkillRuntime lets orchestration distinguish a Skill-aware
// reconciler from a File-only resource reconciler before advertising the
// private Skill dispatcher to the model.
func (m *SessionSkillMaterializer) SupportsSkillRuntime() bool {
	return m != nil && m.skills != nil && m.blobs != nil
}

func (m *SessionSkillMaterializer) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	versions, err := m.skills.SessionSkillsForRuntime(ctx, sessionID)
	if err != nil {
		return err
	}
	return m.reconcileRuntime(ctx, sessionID, domain.SkillRuntime{
		Root: domain.SessionSkillsRoot, Versions: versions,
	}, box)
}

// ReconcileThread materializes only the Skill bundle selected by the Thread's
// resolved Agent execution scope. Session Files and Memory remain shared.
func (m *SessionSkillMaterializer) ReconcileThread(
	ctx context.Context,
	sessionID string,
	threadID string,
	box sandbox.Sandbox,
) error {
	source, ok := m.skills.(SessionThreadSkillMountRepository)
	if !ok {
		return sandbox.Permanent(fmt.Errorf(
			"sandbox: Thread Skill runtime metadata is unavailable for Session %s",
			sessionID,
		))
	}
	runtime, err := source.SessionThreadSkillRuntime(ctx, sessionID, threadID)
	if err != nil {
		return err
	}
	return m.reconcileRuntime(ctx, sessionID, runtime, box)
}

func (m *SessionSkillMaterializer) reconcileRuntime(
	ctx context.Context,
	sessionID string,
	runtime domain.SkillRuntime,
	box sandbox.Sandbox,
) error {
	if len(runtime.Versions) == 0 {
		return nil
	}
	if runtime.Root == "" {
		return sandbox.Permanent(fmt.Errorf(
			"sandbox: Session %s Skill runtime root is missing",
			sessionID,
		))
	}
	mounter, supported := box.(sandbox.SkillBundleSandbox)
	if !supported {
		return sandbox.Permanent(fmt.Errorf(
			"sandbox: provider cannot materialize custom Skills for Session %s",
			sessionID,
		))
	}
	seenNames := make(map[string]struct{}, len(runtime.Versions))
	var expandedBytes int64
	for _, version := range runtime.Versions {
		if _, exists := seenNames[version.Name]; exists {
			return sandbox.Permanent(fmt.Errorf(
				"sandbox: Session %s has conflicting Skill runtime name %q",
				sessionID, version.Name,
			))
		}
		seenNames[version.Name] = struct{}{}
		expanded, valid := SkillExpandedBudgetBytes(version.UncompressedSizeBytes)
		if !valid || expanded > MaxSessionSkillBytes-expandedBytes {
			return sandbox.Permanent(fmt.Errorf(
				"sandbox: Session %s Skills exceed the expanded-size limit",
				sessionID,
			))
		}
		expandedBytes += expanded
		mount := sandbox.ReadOnlySkillMount{
			Identity:              version.SkillID + "@" + version.Version,
			Name:                  version.Name,
			RuntimePath:           runtime.SkillPath(version.Name),
			ArchiveRoot:           version.Directory,
			SizeBytes:             version.SizeBytes,
			UncompressedSizeBytes: version.UncompressedSizeBytes,
			ChecksumSHA256:        version.ChecksumSHA256,
		}
		present, err := mounter.HasReadOnlySkill(ctx, mount)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		body, err := m.blobs.Open(ctx, version.BlobKey)
		if err != nil {
			return err
		}
		importErr := mounter.ImportReadOnlySkill(ctx, mount, body)
		closeErr := body.Close()
		if importErr != nil {
			return importErr
		}
		if closeErr != nil {
			return fmt.Errorf("session Skill: close archive body: %w", closeErr)
		}
	}
	return nil
}

// SessionRuntimeMaterializer composes File Resource and custom Skill
// reconciliation behind Temporal's single pre-tool hook.
type SessionRuntimeMaterializer struct {
	files  *SessionResourceMaterializer
	skills *SessionSkillMaterializer
	memory *SessionMemoryMaterializer
}

func NewSessionRuntimeMaterializer(
	files *SessionResourceMaterializer,
	skills *SessionSkillMaterializer,
	memory ...*SessionMemoryMaterializer,
) *SessionRuntimeMaterializer {
	materializer := &SessionRuntimeMaterializer{files: files, skills: skills}
	if len(memory) > 0 {
		materializer.memory = memory[0]
	}
	return materializer
}

func (m *SessionRuntimeMaterializer) SupportsSkillRuntime() bool {
	return m != nil && m.skills != nil && m.skills.SupportsSkillRuntime()
}

func (m *SessionRuntimeMaterializer) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if m.files != nil {
		if err := m.files.Reconcile(ctx, sessionID, box); err != nil {
			return err
		}
	}
	if m.skills != nil {
		if err := m.skills.Reconcile(ctx, sessionID, box); err != nil {
			return err
		}
	}
	if m.memory != nil {
		return m.memory.Reconcile(ctx, sessionID, box)
	}
	return nil
}

// ReconcileThread keeps Session-shared resources converged while selecting
// custom Skills from the current Thread's resolved Agent scope.
func (m *SessionRuntimeMaterializer) ReconcileThread(
	ctx context.Context,
	sessionID string,
	threadID string,
	box sandbox.Sandbox,
) error {
	if m.files != nil {
		if err := m.files.Reconcile(ctx, sessionID, box); err != nil {
			return err
		}
	}
	if m.skills != nil {
		if err := m.skills.ReconcileThread(ctx, sessionID, threadID, box); err != nil {
			return err
		}
	}
	if m.memory != nil {
		return m.memory.Reconcile(ctx, sessionID, box)
	}
	return nil
}

func (m *SessionRuntimeMaterializer) Writeback(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if m == nil || m.memory == nil {
		return nil
	}
	return m.memory.Writeback(ctx, sessionID, box)
}

func (m *SessionRuntimeMaterializer) WritebackForRelease(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	if m == nil || m.memory == nil {
		return nil
	}
	return m.memory.WritebackForRelease(ctx, sessionID, box)
}

func (m *SessionRuntimeMaterializer) MemoryStoreMountsForRelease(
	ctx context.Context,
	sessionID string,
) ([]sandbox.MemoryStoreMount, error) {
	if m == nil || m.memory == nil {
		return nil, nil
	}
	return m.memory.MemoryStoreMountsForRelease(ctx, sessionID)
}

func (m *SessionRuntimeMaterializer) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	if m.files != nil {
		if err := m.files.CleanupSession(ctx, sessionID); err != nil {
			return err
		}
	}
	if m.memory != nil {
		return m.memory.CleanupSession(ctx, sessionID)
	}
	return nil
}
