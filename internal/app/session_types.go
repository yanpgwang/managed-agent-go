package app

import (
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const (
	MaxSessionResources     = 500
	MaxSessionResourceBytes = MaxFileBytes
)

// CreateSessionInput is the storage-independent command accepted by the
// control-plane session service.
type CreateSessionInput struct {
	AgentID         string
	AgentVersion    *int
	Overrides       *domain.AgentOverrides
	EnvironmentID   string
	Title           string
	Metadata        map[string]any
	InitialEvents   []domain.EventDraft
	Resources       []FileSessionResourceInput
	MemoryResources []MemorySessionResourceInput
	VaultIDs        []string
	DeploymentID    *string
	DeploymentRun   *domain.DeploymentRun
}

type FileSessionResourceInput struct {
	FileID    string
	MountPath *string
}

type MemorySessionResourceInput struct {
	MemoryStoreID string
	Access        string
	Instructions  string
}

type PreparedSessionResource struct {
	Resource domain.SessionResource
	File     domain.File
	Blob     BlobInfo
}

type SessionResourcePageBoundary struct {
	CreatedAt time.Time
	ID        string
}

type SessionResourceListQuery struct {
	Limit    int
	Boundary *SessionResourcePageBoundary
}

type SessionResourceListPage struct {
	Resources []domain.SessionResource
	HasMore   bool
}

// ListPage describes a keyset-paginated session query.
type ListPage struct {
	AgentID         string
	AgentVersion    *int
	CreatedAtGt     *time.Time
	CreatedAtGte    *time.Time
	CreatedAtLt     *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	Statuses        []domain.Status
	DeploymentID    *string
	MemoryStoreID   *string
	Boundary        *SessionPageBoundary
	Limit           int
	Desc            bool
}

type SessionPageBoundary struct {
	CreatedAt time.Time
	ID        string
	Backward  bool
}

type SessionListPage struct {
	Sessions []domain.Session
	HasPrev  bool
	HasNext  bool
}
