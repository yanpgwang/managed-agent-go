package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/yanpgwang/mango/internal/domain"
)

const (
	DefaultMemoryStoreListLimit   = 20
	MaxMemoryStoreListLimit       = 100
	DefaultMemoryListLimit        = 20
	MaxMemoryListLimit            = 100
	MaxFullMemoryListLimit        = 20
	DefaultMemoryVersionListLimit = 20
	MaxMemoryVersionListLimit     = 100
	MaxMemoriesPerStore           = 2000
	MaxMemoryContentBytes         = 102400
	MaxMemoryPathBytes            = 1024
)

type MemoryStoreCreateInput struct {
	Name        string
	Description string
	Metadata    map[string]string
}

type MemoryStoreUpdateInput struct {
	Name        *string
	Description *string
	Metadata    map[string]*string
}

type MemoryStoreListQuery struct {
	CreatedAtGte    *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	After           *ResourcePageBoundary
	Limit           int
}

type MemoryStoreListPage struct {
	Stores  []domain.MemoryStore
	HasNext bool
}

type MemoryCreateInput struct {
	Path    string
	Content string
	Actor   domain.MemoryActor
}

type MemoryUpdateInput struct {
	Path               *string
	Content            *string
	ExpectedContentSHA *string
	Actor              domain.MemoryActor
}

type MemoryListQuery struct {
	PathPrefix string
	Depth      int
	AfterPath  string
	Limit      int
	Full       bool
}

type MemoryListPage struct {
	Items   []domain.MemoryListItem
	HasNext bool
}

type MemoryVersionListQuery struct {
	APIKeyID     string
	SessionID    string
	MemoryID     string
	Operation    string
	CreatedAtGte *time.Time
	CreatedAtLte *time.Time
	After        *ResourcePageBoundary
	Limit        int
}

type MemoryVersionListPage struct {
	Versions []domain.MemoryVersion
	HasNext  bool
}

type MemoryStoreSyncBaseline struct {
	MemoryID      string
	Path          string
	ContentSHA256 string
}

type MemoryStoreSyncContent struct {
	Path    string
	Content string
}

type MemoryRepository interface {
	CreateStore(context.Context, domain.MemoryStore) (domain.MemoryStore, error)
	GetStore(context.Context, string) (domain.MemoryStore, error)
	UpdateStore(context.Context, string, MemoryStoreUpdateInput, domain.Clock) (domain.MemoryStore, error)
	ListStores(context.Context, MemoryStoreListQuery) (MemoryStoreListPage, error)
	ArchiveStore(context.Context, string, domain.Clock) (domain.MemoryStore, error)
	DeleteStore(context.Context, string) error

	CreateMemory(context.Context, domain.Memory, domain.MemoryVersion) (domain.Memory, error)
	GetMemory(context.Context, string, string) (domain.Memory, error)
	ListMemoryHeads(context.Context, string, string) ([]domain.Memory, error)
	UpdateMemory(context.Context, string, string, MemoryUpdateInput, string, domain.Clock) (domain.Memory, error)
	DeleteMemory(context.Context, string, string, *string, domain.MemoryActor, string, domain.Clock) (domain.Memory, error)

	GetMemoryVersion(context.Context, string, string) (domain.MemoryVersion, error)
	ListMemoryVersions(context.Context, string, MemoryVersionListQuery) (MemoryVersionListPage, error)
	RedactMemoryVersion(context.Context, string, string, domain.MemoryActor, domain.Clock) (domain.MemoryVersion, error)
	SyncMemoryStore(context.Context, string, []MemoryStoreSyncBaseline, []MemoryStoreSyncContent, domain.MemoryActor, domain.IDGenerator, domain.Clock) ([]domain.Memory, error)
}

type MemoryService struct {
	repo  MemoryRepository
	ids   domain.IDGenerator
	clock domain.Clock
}

func NewMemoryService(repo MemoryRepository, ids domain.IDGenerator, clock domain.Clock) *MemoryService {
	return &MemoryService{repo: repo, ids: ids, clock: clock}
}

func (s *MemoryService) CreateStore(ctx context.Context, input MemoryStoreCreateInput) (domain.MemoryStore, error) {
	if err := validateMemoryStoreFields(input.Name, input.Description, input.Metadata); err != nil {
		return domain.MemoryStore{}, err
	}
	now := s.clock.Now().UTC()
	return s.repo.CreateStore(ctx, domain.MemoryStore{
		ID: s.ids.NewID(domain.PrefixMemoryStore), Name: input.Name,
		Description: input.Description, Metadata: cloneStringMap(input.Metadata),
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *MemoryService) GetStore(ctx context.Context, id string) (domain.MemoryStore, error) {
	return s.repo.GetStore(ctx, id)
}

func (s *MemoryService) UpdateStore(ctx context.Context, id string, input MemoryStoreUpdateInput) (domain.MemoryStore, error) {
	if input.Name != nil {
		if err := validateMemoryStoreName(*input.Name); err != nil {
			return domain.MemoryStore{}, err
		}
	}
	if input.Description != nil && utf8.RuneCountInString(*input.Description) > 1024 {
		return domain.MemoryStore{}, domain.Validation("description must contain at most 1024 characters")
	}
	if err := validateMemoryMetadataPatch(input.Metadata); err != nil {
		return domain.MemoryStore{}, err
	}
	return s.repo.UpdateStore(ctx, id, input, s.clock)
}

func (s *MemoryService) ListStores(ctx context.Context, query MemoryStoreListQuery) (MemoryStoreListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultMemoryStoreListLimit
	}
	if query.Limit < 1 || query.Limit > MaxMemoryStoreListLimit {
		return MemoryStoreListPage{}, domain.Validation("limit must be between 1 and 100")
	}
	return s.repo.ListStores(ctx, query)
}

func (s *MemoryService) ArchiveStore(ctx context.Context, id string) (domain.MemoryStore, error) {
	return s.repo.ArchiveStore(ctx, id, s.clock)
}

func (s *MemoryService) DeleteStore(ctx context.Context, id string) error {
	return s.repo.DeleteStore(ctx, id)
}

func (s *MemoryService) CreateMemory(ctx context.Context, storeID string, input MemoryCreateInput) (domain.Memory, error) {
	if err := validateMemoryPath(input.Path); err != nil {
		return domain.Memory{}, err
	}
	if err := validateMemoryContent(input.Content); err != nil {
		return domain.Memory{}, err
	}
	if err := validateMemoryActor(input.Actor); err != nil {
		return domain.Memory{}, err
	}
	now := s.clock.Now().UTC()
	memoryID := s.ids.NewID(domain.PrefixMemory)
	versionID := s.ids.NewID(domain.PrefixMemoryVersion)
	sha := memoryContentSHA(input.Content)
	size := int64(len([]byte(input.Content)))
	memory := domain.Memory{
		ID: memoryID, MemoryStoreID: storeID, MemoryVersionID: versionID,
		Path: input.Path, Content: input.Content, ContentSize: size,
		ContentSHA256: sha, CreatedAt: now, UpdatedAt: now,
	}
	path, content := input.Path, input.Content
	version := domain.MemoryVersion{
		ID: versionID, MemoryStoreID: storeID, MemoryID: memoryID,
		Operation: "created", Path: &path, Content: &content,
		ContentSize: &size, ContentSHA256: &sha, CreatedAt: now,
		CreatedBy: input.Actor,
	}
	return s.repo.CreateMemory(ctx, memory, version)
}

func (s *MemoryService) GetMemory(ctx context.Context, storeID, memoryID string) (domain.Memory, error) {
	return s.repo.GetMemory(ctx, storeID, memoryID)
}

func (s *MemoryService) ListMemories(ctx context.Context, storeID string, query MemoryListQuery) (MemoryListPage, error) {
	if query.PathPrefix != "" {
		if !strings.HasSuffix(query.PathPrefix, "/") {
			return MemoryListPage{}, domain.Validation("path_prefix must end with /")
		}
		if err := validateMemoryPrefix(query.PathPrefix); err != nil {
			return MemoryListPage{}, err
		}
	}
	if query.Depth != 0 && query.Depth != 1 {
		return MemoryListPage{}, domain.Validation("depth must be 0 or 1")
	}
	if query.Limit == 0 {
		query.Limit = DefaultMemoryListLimit
	}
	maxLimit := MaxMemoryListLimit
	if query.Full {
		maxLimit = MaxFullMemoryListLimit
	}
	if query.Limit < 1 || query.Limit > maxLimit {
		return MemoryListPage{}, domain.Validation("limit must be between 1 and " + integerString(maxLimit))
	}
	heads, err := s.repo.ListMemoryHeads(ctx, storeID, query.PathPrefix)
	if err != nil {
		return MemoryListPage{}, err
	}
	items := projectMemoryList(heads, query.PathPrefix, query.Depth)
	page := MemoryListPage{Items: make([]domain.MemoryListItem, 0, query.Limit)}
	for _, item := range items {
		path := memoryListItemPath(item)
		if query.AfterPath != "" && path <= query.AfterPath {
			continue
		}
		if len(page.Items) == query.Limit {
			page.HasNext = true
			break
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func (s *MemoryService) UpdateMemory(ctx context.Context, storeID, memoryID string, input MemoryUpdateInput) (domain.Memory, error) {
	// An empty update is a documented no-op, but the repository still performs
	// the scoped lookup so missing IDs do not turn into a false success.
	if input.Path != nil {
		if err := validateMemoryPath(*input.Path); err != nil {
			return domain.Memory{}, err
		}
	}
	if input.Content != nil {
		if err := validateMemoryContent(*input.Content); err != nil {
			return domain.Memory{}, err
		}
	}
	if input.ExpectedContentSHA != nil && !validMemorySHA(*input.ExpectedContentSHA) {
		return domain.Memory{}, domain.Validation("precondition.content_sha256 must be 64 lowercase hexadecimal characters")
	}
	if err := validateMemoryActor(input.Actor); err != nil {
		return domain.Memory{}, err
	}
	return s.repo.UpdateMemory(ctx, storeID, memoryID, input,
		s.ids.NewID(domain.PrefixMemoryVersion), s.clock)
}

func (s *MemoryService) DeleteMemory(ctx context.Context, storeID, memoryID string, expected *string, actor domain.MemoryActor) (domain.Memory, error) {
	if expected != nil && !validMemorySHA(*expected) {
		return domain.Memory{}, domain.Validation("expected_content_sha256 must be 64 lowercase hexadecimal characters")
	}
	if err := validateMemoryActor(actor); err != nil {
		return domain.Memory{}, err
	}
	return s.repo.DeleteMemory(ctx, storeID, memoryID, expected, actor,
		s.ids.NewID(domain.PrefixMemoryVersion), s.clock)
}

func (s *MemoryService) GetMemoryVersion(ctx context.Context, storeID, versionID string) (domain.MemoryVersion, error) {
	return s.repo.GetMemoryVersion(ctx, storeID, versionID)
}

func (s *MemoryService) ListMemoryVersions(ctx context.Context, storeID string, query MemoryVersionListQuery) (MemoryVersionListPage, error) {
	if query.Limit == 0 {
		query.Limit = DefaultMemoryVersionListLimit
	}
	if query.Limit < 1 || query.Limit > MaxMemoryVersionListLimit {
		return MemoryVersionListPage{}, domain.Validation("limit must be between 1 and 100")
	}
	if query.Operation != "" && query.Operation != "created" && query.Operation != "modified" && query.Operation != "deleted" {
		return MemoryVersionListPage{}, domain.Validation("operation must be created, modified, or deleted")
	}
	return s.repo.ListMemoryVersions(ctx, storeID, query)
}

func (s *MemoryService) RedactMemoryVersion(ctx context.Context, storeID, versionID string, actor domain.MemoryActor) (domain.MemoryVersion, error) {
	if err := validateMemoryActor(actor); err != nil {
		return domain.MemoryVersion{}, err
	}
	return s.repo.RedactMemoryVersion(ctx, storeID, versionID, actor, s.clock)
}

// RuntimeHeads returns the complete canonical snapshot used to populate a
// Session mount. It intentionally bypasses public pagination while preserving
// the Store existence check in the repository.
func (s *MemoryService) RuntimeHeads(ctx context.Context, storeID string) ([]domain.Memory, error) {
	return s.repo.ListMemoryHeads(ctx, storeID, "")
}

// SyncRuntimeSnapshot atomically persists all file changes made by one
// sandbox tool against the baseline that was last published to that sandbox.
func (s *MemoryService) SyncRuntimeSnapshot(
	ctx context.Context,
	storeID string,
	baseline []MemoryStoreSyncBaseline,
	current []MemoryStoreSyncContent,
	actor domain.MemoryActor,
) ([]domain.Memory, error) {
	if err := validateMemoryActor(actor); err != nil {
		return nil, err
	}
	seenBaseline := make(map[string]struct{}, len(baseline))
	for _, item := range baseline {
		if item.MemoryID == "" || !validMemorySHA(item.ContentSHA256) {
			return nil, domain.Validation("memory runtime baseline is invalid")
		}
		if err := validateMemoryPath(item.Path); err != nil {
			return nil, err
		}
		if _, duplicate := seenBaseline[item.Path]; duplicate {
			return nil, domain.Validation("memory runtime baseline contains duplicate paths")
		}
		seenBaseline[item.Path] = struct{}{}
	}
	seenCurrent := make(map[string]struct{}, len(current))
	for _, item := range current {
		if err := validateMemoryPath(item.Path); err != nil {
			return nil, err
		}
		if err := validateMemoryContent(item.Content); err != nil {
			return nil, err
		}
		if _, duplicate := seenCurrent[item.Path]; duplicate {
			return nil, domain.Validation("memory runtime snapshot contains duplicate paths")
		}
		seenCurrent[item.Path] = struct{}{}
	}
	if len(current) > MaxMemoriesPerStore {
		return nil, domain.TooLarge("memory store cannot contain more than 2000 memories")
	}
	return s.repo.SyncMemoryStore(
		ctx, storeID, baseline, current, actor, s.ids, s.clock,
	)
}

func validateMemoryStoreFields(name, description string, metadata map[string]string) error {
	if err := validateMemoryStoreName(name); err != nil {
		return err
	}
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > 1024 {
		return domain.Validation("description must contain at most 1024 characters")
	}
	if len(metadata) > 16 {
		return domain.Validation("metadata must contain at most 16 entries")
	}
	for key, value := range metadata {
		if err := validateMemoryMetadataEntry(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateMemoryStoreName(name string) error {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 {
		return domain.Validation("name must contain between 1 and 255 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return domain.Validation("name contains a control character")
		}
	}
	return nil
}

func validateMemoryMetadataPatch(metadata map[string]*string) error {
	for key, value := range metadata {
		if value == nil {
			if !utf8.ValidString(key) || utf8.RuneCountInString(key) < 1 || utf8.RuneCountInString(key) > 64 {
				return domain.Validation("metadata keys must contain between 1 and 64 characters")
			}
			continue
		}
		if err := validateMemoryMetadataEntry(key, *value); err != nil {
			return err
		}
	}
	return nil
}

func validateMemoryMetadataEntry(key, value string) error {
	if !utf8.ValidString(key) || utf8.RuneCountInString(key) < 1 || utf8.RuneCountInString(key) > 64 {
		return domain.Validation("metadata keys must contain between 1 and 64 characters")
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
		return domain.Validation("metadata values must contain at most 512 characters")
	}
	return nil
}

func validateMemoryContent(content string) error {
	if !utf8.ValidString(content) {
		return domain.Validation("content must be valid UTF-8")
	}
	if len([]byte(content)) > MaxMemoryContentBytes {
		return domain.TooLarge("content must not exceed 102400 bytes")
	}
	return nil
}

func validateMemoryPath(path string) error {
	if !utf8.ValidString(path) || len([]byte(path)) > MaxMemoryPathBytes || !strings.HasPrefix(path, "/") || path == "/" {
		return domain.Validation("path must be an absolute path between 2 and 1024 bytes")
	}
	if !norm.NFC.IsNormalString(path) {
		return domain.Validation("path must be NFC-normalized")
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return domain.Validation("path contains a control or format character")
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return domain.Validation("path contains an invalid segment")
		}
	}
	return nil
}

func validateMemoryPrefix(prefix string) error {
	if prefix == "/" {
		return nil
	}
	return validateMemoryPath(strings.TrimSuffix(prefix, "/"))
}

func validateMemoryActor(actor domain.MemoryActor) error {
	if actor.ID == "" || (actor.Type != "api_actor" && actor.Type != "session_actor" && actor.Type != "user_actor") {
		return domain.Validation("memory actor is invalid")
	}
	return nil
}

func memoryContentSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func validMemorySHA(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func projectMemoryList(heads []domain.Memory, prefix string, depth int) []domain.MemoryListItem {
	items := make([]domain.MemoryListItem, 0, len(heads))
	seenPrefixes := make(map[string]struct{})
	for index := range heads {
		memory := heads[index]
		if depth == 1 {
			remainder := strings.TrimPrefix(memory.Path, prefix)
			remainder = strings.TrimPrefix(remainder, "/")
			if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
				rolled := prefix
				if rolled == "" {
					rolled = "/"
				}
				if !strings.HasSuffix(rolled, "/") {
					rolled += "/"
				}
				rolled += remainder[:slash+1]
				if _, exists := seenPrefixes[rolled]; !exists {
					seenPrefixes[rolled] = struct{}{}
					items = append(items, domain.MemoryListItem{Prefix: rolled})
				}
				continue
			}
		}
		copy := memory
		items = append(items, domain.MemoryListItem{Memory: &copy})
	}
	return items
}

func memoryListItemPath(item domain.MemoryListItem) string {
	if item.Memory != nil {
		return item.Memory.Path
	}
	return item.Prefix
}

func integerString(value int) string {
	if value == 20 {
		return "20"
	}
	return "100"
}
