package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	DefaultSkillListLimit        = 20
	MaxSkillListLimit            = 100
	DefaultSkillVersionListLimit = 20
	MaxSkillVersionListLimit     = 1000
	skillCleanupTimeout          = 10 * time.Second
)

type SkillCreateInput struct {
	DisplayTitle *string
	Files        []SkillUploadFile
}

type SkillListQuery struct {
	Source string
	After  *ResourcePageBoundary
	Limit  int
}

type SkillListPage struct {
	Skills  []domain.Skill
	HasNext bool
}

type SkillVersionListQuery struct {
	After *ResourcePageBoundary
	Limit int
}

type SkillVersionListPage struct {
	Versions []domain.SkillVersion
	HasNext  bool
}

type SkillVersionDownload struct {
	Version domain.SkillVersion
	Body    io.ReadCloser
}

// SkillRepository owns metadata and durable object-store lifecycle intents.
// Begin operations commit before blob I/O; CompleteVersion is the visibility
// boundary for an immutable archive.
type SkillRepository interface {
	BeginSkill(context.Context, domain.Skill, domain.SkillVersion) error
	BeginVersion(context.Context, domain.SkillVersion) error
	CompleteVersion(context.Context, string, string, BlobInfo) (domain.Skill, domain.SkillVersion, error)
	GetSkill(context.Context, string) (domain.Skill, error)
	ListSkills(context.Context, SkillListQuery) (SkillListPage, error)
	GetVersion(context.Context, string, string) (domain.SkillVersion, error)
	ListVersions(context.Context, string, SkillVersionListQuery) (SkillVersionListPage, error)
	BeginDeleteVersion(context.Context, string, string) (domain.SkillVersion, error)
	RemoveIncompleteVersion(context.Context, string, string) error
	ListIncompleteVersions(context.Context) ([]domain.SkillVersion, error)
	DeleteEmptySkill(context.Context, string) error
	DeleteSkill(context.Context, string) (domain.Skill, error)
}

type SkillService struct {
	repo  SkillRepository
	blobs BlobStore
	ids   domain.IDGenerator
	clock domain.Clock

	versionMu sync.Mutex
	lastEpoch int64
}

func NewSkillService(
	repo SkillRepository,
	blobs BlobStore,
	ids domain.IDGenerator,
	clock domain.Clock,
) *SkillService {
	return &SkillService{repo: repo, blobs: blobs, ids: ids, clock: clock}
}

func (s *SkillService) Create(ctx context.Context, input SkillCreateInput) (domain.Skill, error) {
	bundle, err := prepareSkillBundle(input.Files)
	if err != nil {
		return domain.Skill{}, err
	}
	title, explicit, err := skillDisplayTitle(input.DisplayTitle, bundle.Name)
	if err != nil {
		return domain.Skill{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	skill := domain.Skill{
		ID: s.ids.NewID(domain.PrefixSkill), CreatedAt: now, UpdatedAt: now,
		DisplayTitle: title, Source: "custom", TitleExplicit: explicit,
	}
	version := s.newVersion(skill.ID, bundle, now, true)
	if err := s.repo.BeginSkill(ctx, skill, version); err != nil {
		return domain.Skill{}, err
	}
	created, _, err := s.storeVersion(ctx, skill, version, bundle.Archive)
	return created, err
}

func (s *SkillService) CreateVersion(
	ctx context.Context,
	skillID string,
	files []SkillUploadFile,
) (domain.SkillVersion, error) {
	bundle, err := prepareSkillBundle(files)
	if err != nil {
		return domain.SkillVersion{}, err
	}
	current, err := s.repo.GetSkill(ctx, skillID)
	if err != nil {
		return domain.SkillVersion{}, err
	}
	if err := s.observeVersion(current.LatestVersion); err != nil {
		return domain.SkillVersion{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	version := s.newVersion(skillID, bundle, now, false)
	if err := s.repo.BeginVersion(ctx, version); err != nil {
		return domain.SkillVersion{}, err
	}
	_, created, err := s.storeVersion(ctx, domain.Skill{ID: skillID}, version, bundle.Archive)
	return created, err
}

func (s *SkillService) storeVersion(
	ctx context.Context,
	skill domain.Skill,
	version domain.SkillVersion,
	archive []byte,
) (domain.Skill, domain.SkillVersion, error) {
	info, err := s.blobs.Put(
		ctx, version.BlobKey, "application/zip", bytes.NewReader(archive), MaxSkillUploadBytes,
	)
	if err != nil {
		s.cleanupIncompleteVersion(ctx, version)
		if errors.Is(err, ErrBlobTooLarge) {
			return domain.Skill{}, domain.SkillVersion{},
				domain.TooLarge("Skill upload must be smaller than 30 MB")
		}
		return domain.Skill{}, domain.SkillVersion{}, err
	}
	completedSkill, completedVersion, err := s.repo.CompleteVersion(
		ctx, version.SkillID, version.Version, info,
	)
	if err != nil {
		// A connection failure may be observed after PostgreSQL committed. Keep
		// the archive so a visible ready Version never loses its bytes.
		return domain.Skill{}, domain.SkillVersion{}, err
	}
	return completedSkill, completedVersion, nil
}

func (s *SkillService) newVersion(
	skillID string,
	bundle preparedSkillBundle,
	now time.Time,
	initial bool,
) domain.SkillVersion {
	epoch := s.nextVersion(now)
	version := strconv.FormatInt(epoch, 10)
	return domain.SkillVersion{
		ID: version, SkillID: skillID, Version: version,
		CreatedAt: time.UnixMicro(epoch).UTC(),
		Name:      bundle.Name, Description: bundle.Description, Directory: bundle.Directory,
		BlobKey: "skills/" + skillID + "/" + version + ".zip",
		State:   domain.SkillVersionUploading, Initial: initial,
	}
}

func (s *SkillService) nextVersion(now time.Time) int64 {
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	candidate := now.UnixMicro()
	if candidate < 1 {
		candidate = 1
	}
	if candidate <= s.lastEpoch {
		candidate = s.lastEpoch + 1
	}
	s.lastEpoch = candidate
	return candidate
}

func (s *SkillService) observeVersion(version string) error {
	if version == "" {
		return nil
	}
	epoch, err := strconv.ParseInt(version, 10, 64)
	if err != nil || epoch < 1 {
		return errors.New("stored Skill latest_version is invalid")
	}
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if epoch > s.lastEpoch {
		s.lastEpoch = epoch
	}
	return nil
}

func (s *SkillService) Get(ctx context.Context, id string) (domain.Skill, error) {
	return s.repo.GetSkill(ctx, id)
}

func (s *SkillService) List(ctx context.Context, query SkillListQuery) (SkillListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultSkillListLimit
	}
	if query.Limit < 1 || query.Limit > MaxSkillListLimit {
		return SkillListPage{}, domain.Validation("limit must be between 1 and 100")
	}
	if query.Source != "" && query.Source != "custom" && query.Source != "anthropic" {
		return SkillListPage{}, domain.Validation("source must be custom or anthropic")
	}
	if query.Source == "anthropic" {
		return SkillListPage{Skills: []domain.Skill{}}, nil
	}
	return s.repo.ListSkills(ctx, query)
}

func (s *SkillService) GetVersion(
	ctx context.Context,
	skillID string,
	version string,
) (domain.SkillVersion, error) {
	return s.repo.GetVersion(ctx, skillID, version)
}

func (s *SkillService) ListVersions(
	ctx context.Context,
	skillID string,
	query SkillVersionListQuery,
) (SkillVersionListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultSkillVersionListLimit
	}
	if query.Limit < 1 || query.Limit > MaxSkillVersionListLimit {
		return SkillVersionListPage{}, domain.Validation("limit must be between 1 and 1000")
	}
	return s.repo.ListVersions(ctx, skillID, query)
}

func (s *SkillService) Download(
	ctx context.Context,
	skillID string,
	version string,
) (SkillVersionDownload, error) {
	item, err := s.repo.GetVersion(ctx, skillID, version)
	if err != nil {
		return SkillVersionDownload{}, err
	}
	body, err := s.blobs.Open(ctx, item.BlobKey)
	if err != nil {
		return SkillVersionDownload{}, err
	}
	return SkillVersionDownload{Version: item, Body: body}, nil
}

func (s *SkillService) DeleteVersion(
	ctx context.Context,
	skillID string,
	version string,
) (domain.SkillVersion, error) {
	item, err := s.repo.BeginDeleteVersion(ctx, skillID, version)
	if err != nil {
		return domain.SkillVersion{}, err
	}
	if err := s.blobs.Delete(ctx, item.BlobKey); err != nil {
		return domain.SkillVersion{}, err
	}
	if err := s.repo.RemoveIncompleteVersion(ctx, skillID, version); err != nil {
		return domain.SkillVersion{}, err
	}
	return item, nil
}

func (s *SkillService) Delete(ctx context.Context, id string) (domain.Skill, error) {
	return s.repo.DeleteSkill(ctx, id)
}

func (s *SkillService) Reconcile(ctx context.Context) error {
	versions, err := s.repo.ListIncompleteVersions(ctx)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if err := s.blobs.Delete(ctx, version.BlobKey); err != nil {
			return err
		}
		if err := s.repo.RemoveIncompleteVersion(ctx, version.SkillID, version.Version); err != nil {
			return err
		}
		if version.Initial && version.State == domain.SkillVersionUploading {
			if err := s.repo.DeleteEmptySkill(ctx, version.SkillID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SkillService) cleanupIncompleteVersion(ctx context.Context, version domain.SkillVersion) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), skillCleanupTimeout)
	defer cancel()
	_ = s.blobs.Delete(cleanupCtx, version.BlobKey)
	_ = s.repo.RemoveIncompleteVersion(cleanupCtx, version.SkillID, version.Version)
	if version.Initial {
		_ = s.repo.DeleteEmptySkill(cleanupCtx, version.SkillID)
	}
}

func skillDisplayTitle(provided *string, fallback string) (string, bool, error) {
	if provided == nil {
		return fallback, false, nil
	}
	title := strings.TrimSpace(*provided)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 255 {
		return "", false, domain.Validation("display_title must contain between 1 and 255 characters")
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return "", false, domain.Validation("display_title contains a control character")
		}
	}
	return title, true, nil
}
