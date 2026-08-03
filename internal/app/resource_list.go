package app

import (
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
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
	// The Agent list bounds are documented by the public API and official SDK.
	DefaultAgentListLimit = 20
	MaxAgentListLimit     = 100

	// The Environment list reference documents no default or maximum. Mango
	// applies its existing general list bounds as a local operational choice.
	DefaultEnvironmentListLimit = 100
	MaxEnvironmentListLimit     = 1000
)
