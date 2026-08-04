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
	if len(versions) == 0 {
		return nil
	}
	mounter, supported := box.(sandbox.SkillBundleSandbox)
	if !supported {
		return sandbox.Permanent(fmt.Errorf(
			"sandbox: provider cannot materialize custom Skills for Session %s",
			sessionID,
		))
	}
	seenNames := make(map[string]struct{}, len(versions))
	var expandedBytes int64
	for _, version := range versions {
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
}

func NewSessionRuntimeMaterializer(
	files *SessionResourceMaterializer,
	skills *SessionSkillMaterializer,
) *SessionRuntimeMaterializer {
	return &SessionRuntimeMaterializer{files: files, skills: skills}
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
		return m.skills.Reconcile(ctx, sessionID, box)
	}
	return nil
}

func (m *SessionRuntimeMaterializer) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	if m.files == nil {
		return nil
	}
	return m.files.CleanupSession(ctx, sessionID)
}
