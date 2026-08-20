package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
	"github.com/yanpgwang/mango/internal/pg"
	"github.com/yanpgwang/mango/internal/workspace"
)

const MaxSessionResources = app.MaxSessionResources

// SessionResourceService owns File-copy preparation and the public lifecycle
// of File-backed Session Resources. PostgreSQL publishes a prepared copy only
// in the same transaction that publishes its resource row.
type SessionResourceService struct {
	store            *pg.Store
	files            app.FileRepository
	blobs            app.FileBlobStore
	ids              domain.IDGenerator
	clock            domain.Clock
	outputs          *app.SessionOutputPublisher
	admissionEnabled bool
}

func NewSessionResourceService(
	store *pg.Store,
	files app.FileRepository,
	blobs app.FileBlobStore,
	ids domain.IDGenerator,
	clock domain.Clock,
	admissionEnabled bool,
) *SessionResourceService {
	service := &SessionResourceService{
		store: store, files: files, blobs: blobs, ids: ids, clock: clock,
		admissionEnabled: admissionEnabled,
	}
	if outputFiles, ok := files.(app.SessionOutputRepository); ok {
		service.outputs = app.NewSessionOutputPublisher(
			outputFiles, blobs, ids, clock,
		)
	}
	return service
}

func (s *SessionResourceService) PrepareForSession(
	ctx context.Context,
	session domain.Session,
	inputs []app.FileSessionResourceInput,
) ([]app.PreparedSessionResource, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if !s.admissionEnabled {
		return nil, domain.Unsupported(
			"File resources require a sandbox provider with materialization support",
		)
	}
	if len(inputs) > MaxSessionResources {
		return nil, domain.Validation("resources must contain at most 500 entries")
	}
	if session.ArchivedAt != nil {
		return nil, domain.Conflict("cannot add a resource to an archived session")
	}
	if session.Status == domain.StatusTerminated {
		return nil, domain.Conflict("cannot add a resource to a terminated session")
	}
	if len(session.Resources)+len(inputs) > MaxSessionResources {
		return nil, domain.Conflict("session already has 500 resources")
	}
	if session.EnvironmentType != "cloud" {
		return nil, domain.Unsupported(
			"File resources require a cloud Environment with a capable sandbox provider",
		)
	}

	mountPaths := make([]string, len(inputs))
	for index, input := range inputs {
		mountPath, err := domain.NormalizeSessionFileMountPath(input.FileID, input.MountPath)
		if err != nil {
			return nil, err
		}
		for _, existing := range mountPaths[:index] {
			if domain.SessionFileMountPathsConflict(existing, mountPath) {
				return nil, domain.Conflict(
					"session resource mount_path values cannot overlap",
				)
			}
		}
		for _, existing := range session.Resources {
			if existing.State == domain.SessionResourceActive &&
				domain.SessionFileMountPathsConflict(existing.MountPath, mountPath) {
				return nil, domain.Conflict(
					"session resource mount_path values cannot overlap",
				)
			}
		}
		mountPaths[index] = mountPath
	}

	var totalBytes int64
	if len(session.Resources) > 0 {
		var err error
		totalBytes, err = s.store.ActiveSessionResourceBytes(ctx, session.ID)
		if err != nil {
			return nil, err
		}
	}
	sources := make([]domain.File, len(inputs))
	for index, input := range inputs {
		source, err := s.sourceFile(ctx, session.ID, input.FileID)
		if err != nil {
			return nil, err
		}
		if source.SizeBytes > app.MaxSessionResourceBytes-totalBytes {
			return nil, domain.TooLarge(
				"Session File Resources exceed the 500 MB aggregate limit",
			)
		}
		totalBytes += source.SizeBytes
		sources[index] = source
	}

	prepared := make([]app.PreparedSessionResource, 0, len(inputs))
	for index := range inputs {
		item, err := s.prepareFileCopy(ctx, session.ID, sources[index], mountPaths[index])
		if err != nil {
			s.DiscardPrepared(ctx, prepared)
			return nil, err
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func (s *SessionResourceService) Add(
	ctx context.Context,
	sessionID string,
	input app.FileSessionResourceInput,
) (domain.SessionResource, error) {
	session, err := s.store.GetSessionForWorkspace(ctx, sessionID)
	if err != nil {
		return domain.SessionResource{}, err
	}
	prepared, err := s.PrepareForSession(ctx, session, []app.FileSessionResourceInput{input})
	if err != nil {
		return domain.SessionResource{}, err
	}
	resource, err := s.store.AddSessionResource(
		ctx,
		prepared[0],
		MaxSessionResources,
		app.MaxSessionResourceBytes,
	)
	if err != nil {
		var domainErr *domain.DomainError
		if errors.As(err, &domainErr) {
			s.DiscardPrepared(ctx, prepared)
		}
		return domain.SessionResource{}, err
	}
	return resource, nil
}

func (s *SessionResourceService) Get(
	ctx context.Context,
	sessionID string,
	resourceID string,
) (domain.SessionResource, error) {
	if err := s.store.AssertSessionWorkspace(ctx, sessionID); err != nil {
		return domain.SessionResource{}, err
	}
	return s.store.GetSessionResource(ctx, sessionID, resourceID)
}

func (s *SessionResourceService) List(
	ctx context.Context,
	sessionID string,
	query app.SessionResourceListQuery,
) (app.SessionResourceListPage, error) {
	if err := s.store.AssertSessionWorkspace(ctx, sessionID); err != nil {
		return app.SessionResourceListPage{}, err
	}
	if query.Limit < 0 || query.Limit > 1000 {
		return app.SessionResourceListPage{}, domain.Validation(
			"limit must be between 1 and 1000",
		)
	}
	if query.Limit == 0 {
		query.Limit = MaxSessionResources
	}
	return s.store.ListSessionResources(ctx, sessionID, query)
}

func (s *SessionResourceService) Update(
	ctx context.Context,
	sessionID string,
	resourceID string,
	_ string,
) (domain.SessionResource, error) {
	if err := s.store.AssertSessionWorkspace(ctx, sessionID); err != nil {
		return domain.SessionResource{}, err
	}
	if _, err := s.store.GetSessionResource(ctx, sessionID, resourceID); err != nil {
		return domain.SessionResource{}, err
	}
	return domain.SessionResource{}, domain.Unsupported(
		"File resources do not support authorization_token rotation; update applies only to GitHub repository resources",
	)
}

func (s *SessionResourceService) Delete(
	ctx context.Context,
	sessionID string,
	resourceID string,
) (domain.SessionResource, error) {
	if err := s.store.AssertSessionWorkspace(ctx, sessionID); err != nil {
		return domain.SessionResource{}, err
	}
	resource, err := s.store.BeginSessionResourceDeletion(ctx, sessionID, resourceID)
	if err != nil {
		return domain.SessionResource{}, err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	// Desired-state deletion is already committed. Cleanup is idempotent and is
	// retried by worker reconciliation; a transient object-store error must not
	// turn the successful public delete into an unsafe client retry.
	blobKey := resource.BlobKey
	if blobKey == "" {
		blobKey = workspace.BlobKey(cleanupCtx, "files/"+resource.FileID)
	}
	if err := s.blobs.Delete(cleanupCtx, blobKey); err == nil {
		if resource.BlobKey == "" {
			_ = s.blobs.Delete(cleanupCtx, "files/"+resource.FileID)
		}
		_ = s.files.RemoveIncomplete(cleanupCtx, resource.FileID)
	}
	return resource, nil
}

func (s *SessionResourceService) CleanupSession(
	ctx context.Context,
	sessionID string,
) error {
	if s.outputs != nil {
		if err := s.outputs.CleanupSession(ctx, sessionID); err != nil {
			return err
		}
	}
	return app.CleanupSessionResourceFiles(
		ctx, s.store, s.files, s.blobs, sessionID,
	)
}

func (s *SessionResourceService) DiscardPrepared(
	ctx context.Context,
	prepared []app.PreparedSessionResource,
) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, item := range prepared {
		if item.Resource.Type() != domain.SessionResourceTypeFile {
			continue
		}
		if err := s.blobs.Delete(cleanupCtx, item.File.BlobKey); err == nil {
			_ = s.files.RemoveIncomplete(cleanupCtx, item.File.ID)
		}
	}
}

func (s *SessionResourceService) prepareFileCopy(
	ctx context.Context,
	sessionID string,
	source domain.File,
	mountPath string,
) (app.PreparedSessionResource, error) {
	if source.ID == "" {
		return app.PreparedSessionResource{}, domain.Validation("file_id is required")
	}

	now := s.clock.Now().UTC()
	fileID := s.ids.NewID(domain.PrefixFile)
	clone := domain.File{
		ID: fileID, CreatedAt: now, UpdatedAt: now,
		Filename: source.Filename, MimeType: source.MimeType,
		Downloadable: true,
		Scope:        &domain.FileScope{ID: sessionID, Type: "session"},
		BlobKey:      workspace.BlobKey(ctx, "files/"+fileID),
		State:        domain.FileStateUploading,
	}
	if err := s.files.BeginUpload(ctx, clone); err != nil {
		return app.PreparedSessionResource{}, err
	}
	body, err := s.blobs.Open(ctx, source.BlobKey)
	if err != nil {
		s.DiscardPrepared(ctx, []app.PreparedSessionResource{{File: clone}})
		return app.PreparedSessionResource{}, err
	}
	info, putErr := s.blobs.Put(ctx, clone.BlobKey, clone.MimeType, body, app.MaxFileBytes)
	closeErr := body.Close()
	if putErr != nil {
		s.DiscardPrepared(ctx, []app.PreparedSessionResource{{File: clone}})
		return app.PreparedSessionResource{}, putErr
	}
	if closeErr != nil {
		s.DiscardPrepared(ctx, []app.PreparedSessionResource{{File: clone}})
		return app.PreparedSessionResource{}, fmt.Errorf("session resource: close source File: %w", closeErr)
	}
	if info.SizeBytes != source.SizeBytes || info.ChecksumSHA256 != source.ChecksumSHA256 {
		s.DiscardPrepared(ctx, []app.PreparedSessionResource{{File: clone}})
		return app.PreparedSessionResource{}, errors.New(
			"session resource: source File changed while it was copied",
		)
	}

	return app.PreparedSessionResource{
		Resource: domain.SessionResource{
			ID: s.ids.NewID(domain.PrefixSessionResource), SessionID: sessionID,
			ResourceType: domain.SessionResourceTypeFile,
			SourceFileID: source.ID, FileID: clone.ID, MountPath: mountPath,
			CreatedAt: now, UpdatedAt: now, State: domain.SessionResourceActive,
		},
		File: clone,
		Blob: info,
	}, nil
}

func (s *SessionResourceService) sourceFile(
	ctx context.Context,
	sessionID string,
	sourceFileID string,
) (domain.File, error) {
	if sourceFileID == "" {
		return domain.File{}, domain.Validation("file_id is required")
	}
	source, err := s.files.Get(ctx, sourceFileID)
	if err != nil {
		var domainErr *domain.DomainError
		if errors.As(err, &domainErr) && domainErr.Kind == domain.KindNotFound {
			return domain.File{}, domain.Validation(
				"file_id does not identify a ready File",
			)
		}
		return domain.File{}, err
	}
	if source.Scope != nil && source.Scope.ID != sessionID {
		return domain.File{}, domain.Validation(
			"a session-scoped File cannot be attached to another Session",
		)
	}
	return source, nil
}
