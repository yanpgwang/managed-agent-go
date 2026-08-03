package app

import (
	"context"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// AgentRepository is the persistence boundary for versioned Agent resources.
// The application service owns validation and timestamps; the repository owns
// atomic version allocation and lifecycle concurrency.
type AgentRepository interface {
	PutVersion(context.Context, domain.Agent) error
	UpdateVersion(
		context.Context,
		string,
		func(domain.Agent) (domain.Agent, bool, error),
	) (domain.Agent, error)
	Archive(context.Context, string, time.Time) (domain.Agent, error)
	Latest(context.Context, string) (domain.Agent, error)
	GetVersion(context.Context, string, int) (domain.Agent, error)
	Versions(context.Context, string) ([]domain.Agent, error)
	ListLatest(context.Context, AgentListQuery) (AgentListPage, error)
}

// EnvironmentRepository is the persistence boundary for Environment
// resources. DeleteIfUnreferenced must make the delete/reference check atomic.
type EnvironmentRepository interface {
	Put(context.Context, domain.Environment) error
	Get(context.Context, string) (domain.Environment, error)
	List(context.Context, EnvironmentListQuery) (EnvironmentListPage, error)
	DeleteIfUnreferenced(context.Context, string) error
}
