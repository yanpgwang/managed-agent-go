package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/sandbox"
	"github.com/yanpgwang/mango/internal/workspace"
)

type SessionResourceMountRepository interface {
	SessionResourcesForReconcile(context.Context, string) ([]domain.SessionResource, error)
	GetSessionResource(context.Context, string, string) (domain.SessionResource, error)
	FinalizeSessionResourceDeletion(context.Context, string, string) error
}

// SessionResourceMaterializer converges one durable sandbox to PostgreSQL
// desired state. Provider markers make repeated imports cheap and crash-safe.
type SessionResourceMaterializer struct {
	resources SessionResourceMountRepository
	files     FileRepository
	blobs     FileBlobStore
}

func NewSessionResourceMaterializer(
	resources SessionResourceMountRepository,
	files FileRepository,
	blobs FileBlobStore,
) *SessionResourceMaterializer {
	return &SessionResourceMaterializer{
		resources: resources, files: files, blobs: blobs,
	}
}

func (m *SessionResourceMaterializer) Reconcile(
	ctx context.Context,
	sessionID string,
	box sandbox.Sandbox,
) error {
	resources, err := m.resources.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	if len(resources) == 0 {
		return nil
	}
	mounter, supported := box.(sandbox.FileResourceSandbox)
	repositoryMounter, repositoriesSupported := box.(sandbox.GitRepositorySandbox)
	for _, resource := range resources {
		switch resource.State {
		case domain.SessionResourceActive:
			if resource.Type() == domain.SessionResourceTypeMemoryStore {
				continue
			}
			if resource.Type() == domain.SessionResourceTypeFile && !supported {
				return sandbox.Permanent(fmt.Errorf(
					"sandbox: provider cannot materialize File Resource %s",
					resource.ID,
				))
			}
			if resource.Type() == domain.SessionResourceTypeGitRepository && !repositoriesSupported {
				return sandbox.Permanent(fmt.Errorf(
					"sandbox: provider cannot materialize Git repository Resource %s",
					resource.ID,
				))
			}
			file, err := m.files.Get(ctx, resource.FileID)
			if err != nil {
				return err
			}
			var present bool
			if resource.Type() == domain.SessionResourceTypeGitRepository {
				present, err = repositoryMounter.HasGitRepository(ctx, sandbox.GitRepositoryMount{
					Identity: resource.ID, RuntimePath: resource.MountPath,
					ResolvedCommit: resource.RepositoryResolvedCommit,
					SizeBytes:      file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256,
				})
			} else {
				present, err = mounter.HasFileResource(ctx, sandbox.FileResourceMount{
					Identity: resource.ID, RuntimePath: resource.MountPath,
					SizeBytes: file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256,
				})
			}
			if err != nil {
				return err
			}
			if present {
				continue
			}
			body, err := m.blobs.Open(ctx, file.BlobKey)
			if err != nil {
				return err
			}
			var importErr error
			if resource.Type() == domain.SessionResourceTypeGitRepository {
				importErr = repositoryMounter.ImportGitRepository(ctx, sandbox.GitRepositoryMount{
					Identity: resource.ID, RuntimePath: resource.MountPath,
					ResolvedCommit: resource.RepositoryResolvedCommit,
					SizeBytes:      file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256,
				}, body)
			} else {
				importErr = mounter.ImportFileResource(ctx, sandbox.FileResourceMount{
					Identity: resource.ID, RuntimePath: resource.MountPath,
					SizeBytes: file.SizeBytes, ChecksumSHA256: file.ChecksumSHA256,
				}, body)
			}
			closeErr := body.Close()
			if importErr != nil {
				return importErr
			}
			if closeErr != nil {
				return fmt.Errorf("session resource: close object body: %w", closeErr)
			}
			// A timed-out attempt can overlap its retry. Revalidate after import
			// so a late attempt cannot keep a resource whose tombstone another
			// attempt already finalized.
			checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			current, currentErr := m.resources.GetSessionResource(
				checkCtx, sessionID, resource.ID,
			)
			if currentErr != nil {
				var domainErr *domain.DomainError
				if errors.As(currentErr, &domainErr) && domainErr.Kind == domain.KindNotFound {
					var removeErr error
					if resource.Type() == domain.SessionResourceTypeGitRepository {
						removeErr = repositoryMounter.RemoveGitRepository(
							checkCtx, resource.MountPath, resource.ID,
						)
					} else {
						removeErr = mounter.RemoveFileResource(
							checkCtx, resource.MountPath, resource.ID,
						)
					}
					cancel()
					if removeErr != nil {
						return removeErr
					}
					return errors.New(
						"session resource changed during materialization; retry",
					)
				}
				cancel()
				return currentErr
			}
			cancel()
			if current.State != domain.SessionResourceActive ||
				current.MountPath != resource.MountPath {
				return errors.New("session resource changed during materialization; retry")
			}
		case domain.SessionResourceDeleting:
			if resource.Type() == domain.SessionResourceTypeMemoryStore {
				if err := m.resources.FinalizeSessionResourceDeletion(
					ctx, sessionID, resource.ID,
				); err != nil {
					return err
				}
				continue
			}
			if resource.Type() == domain.SessionResourceTypeGitRepository && repositoriesSupported {
				if err := repositoryMounter.RemoveGitRepository(
					ctx, resource.MountPath, resource.ID,
				); err != nil {
					return err
				}
			} else if resource.Type() == domain.SessionResourceTypeFile && supported {
				if err := mounter.RemoveFileResource(
					ctx, resource.MountPath, resource.ID,
				); err != nil {
					return err
				}
			}
			blobKey := resource.BlobKey
			if blobKey == "" {
				blobKey = workspace.BlobKey(ctx, "files/"+resource.FileID)
			}
			if err := m.blobs.Delete(ctx, blobKey); err != nil {
				return err
			}
			if resource.BlobKey == "" {
				_ = m.blobs.Delete(ctx, "files/"+resource.FileID)
			}
			if err := m.files.RemoveIncomplete(ctx, resource.FileID); err != nil {
				return err
			}
			if err := m.resources.FinalizeSessionResourceDeletion(
				ctx, sessionID, resource.ID,
			); err != nil {
				return err
			}
		default:
			return fmt.Errorf(
				"session resource: unsupported persisted state %q", resource.State,
			)
		}
	}
	return nil
}

// CleanupSession discharges File Resource objects after Session deletion has
// destroyed the whole sandbox. PostgreSQL's non-cascading resource foreign key
// prevents the Session row from disappearing before this succeeds.
func (m *SessionResourceMaterializer) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	return CleanupSessionResourceFiles(
		ctx, m.resources, m.files, m.blobs, sessionID,
	)
}

// CleanupSessionResourceFiles is the single object/metadata cleanup sequence
// shared by the API deletion path and the worker lifecycle reconciler.
func CleanupSessionResourceFiles(
	ctx context.Context,
	resourcesRepository SessionResourceMountRepository,
	files FileRepository,
	blobs FileBlobStore,
	sessionID string,
) error {
	resources, err := resourcesRepository.SessionResourcesForReconcile(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.State != domain.SessionResourceDeleting {
			return fmt.Errorf(
				"session resource %s is not prepared for Session deletion",
				resource.ID,
			)
		}
		if resource.Type() == domain.SessionResourceTypeMemoryStore {
			if err := resourcesRepository.FinalizeSessionResourceDeletion(
				ctx, sessionID, resource.ID,
			); err != nil {
				return err
			}
			continue
		}
		blobKey := resource.BlobKey
		if blobKey == "" {
			blobKey = workspace.BlobKey(ctx, "files/"+resource.FileID)
		}
		if err := blobs.Delete(ctx, blobKey); err != nil {
			return err
		}
		if resource.BlobKey == "" {
			_ = blobs.Delete(ctx, "files/"+resource.FileID)
		}
		if err := files.RemoveIncomplete(ctx, resource.FileID); err != nil {
			return err
		}
		if err := resourcesRepository.FinalizeSessionResourceDeletion(
			ctx, sessionID, resource.ID,
		); err != nil {
			return err
		}
	}
	return nil
}
