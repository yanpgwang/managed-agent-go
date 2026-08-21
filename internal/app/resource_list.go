package app

import (
	"time"

	"github.com/yanpgwang/mango/internal/domain"
)

// AgentListQuery is deliberately distinct from EnvironmentListQuery because
// the two public endpoints expose different filters.
type AgentListQuery struct {
	CreatedAtGte    *time.Time
	CreatedAtLte    *time.Time
	IncludeArchived bool
	After           *ResourcePageBoundary
	Limit           int
}

type AgentListPage struct {
	Agents  []domain.Agent
	HasNext bool
}

// AgentVersionListQuery pages forward through one Agent's immutable version
// history. Versions are returned in ascending numeric order.
type AgentVersionListQuery struct {
	AfterVersion int
	Limit        int
}

type AgentVersionListPage struct {
	Versions []domain.Agent
	HasNext  bool
}

// EnvironmentListQuery exposes only the parameters documented for
// GET /v1/environments: include_archived, limit, and page.
type EnvironmentListQuery struct {
	IncludeArchived bool
	After           *ResourcePageBoundary
	Limit           int
}

type EnvironmentListPage struct {
	Environments []domain.Environment
	HasNext      bool
}

// ResourcePageBoundary is the last (created_at, id) pair returned by a
// forward-only resource page.
type ResourcePageBoundary struct {
	CreatedAt time.Time
	ID        string
}

const (
	// The Agent and Agent Version list bounds are documented by Mango's public
	// API.
	DefaultAgentListLimit = 20
	MaxAgentListLimit     = 100

	// The Environment list reference documents no default or maximum. Mango
	// applies its existing general list bounds as a local operational choice.
	DefaultEnvironmentListLimit = 100
	MaxEnvironmentListLimit     = 1000
)
