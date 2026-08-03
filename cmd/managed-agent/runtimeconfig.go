package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/pg"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// Runtime limit and authentication environment variables. Every one of these is
// deployment configuration, not part of the Managed Agents public API. Each has
// a working default so the zero-config local path keeps starting unchanged.
const (
	// envAPIKeys holds the accepted API keys as a comma- or whitespace-
	// separated list of "<key-id>:<secret>" entries. Empty disables
	// authentication (with a startup warning); see runServe.
	envAPIKeys = "MANAGED_AGENT_API_KEYS"
	// envAllowAuthorizationHeader opts in to reading `authorization: Bearer
	// <key>` in addition to `x-api-key`. This is a non-upstream extension.
	envAllowAuthorizationHeader = "MANAGED_AGENT_AUTH_ALLOW_AUTHORIZATION_HEADER"

	// envShutdownTimeout bounds the API drain window. It must be long enough
	// for open SSE streams to end at a frame boundary.
	envShutdownTimeout = "MANAGED_AGENT_SHUTDOWN_TIMEOUT"

	envDBMaxConns          = "MANAGED_AGENT_DB_MAX_CONNS"
	envDBMinConns          = "MANAGED_AGENT_DB_MIN_CONNS"
	envDBMaxConnLifetime   = "MANAGED_AGENT_DB_MAX_CONN_LIFETIME"
	envDBMaxConnIdleTime   = "MANAGED_AGENT_DB_MAX_CONN_IDLE_TIME"
	envDBHealthCheckPeriod = "MANAGED_AGENT_DB_HEALTH_CHECK_PERIOD"
	envDBStatementTimeout  = "MANAGED_AGENT_DB_STATEMENT_TIMEOUT"

	envWorkerMaxActivities    = "MANAGED_AGENT_WORKER_MAX_CONCURRENT_ACTIVITIES"
	envWorkerMaxWorkflowTasks = "MANAGED_AGENT_WORKER_MAX_CONCURRENT_WORKFLOW_TASKS"
	envWorkerActivityPollers  = "MANAGED_AGENT_WORKER_ACTIVITY_POLLERS"
	envWorkerWorkflowPollers  = "MANAGED_AGENT_WORKER_WORKFLOW_POLLERS"
	envWorkerDrainTimeout     = "MANAGED_AGENT_WORKER_DRAIN_TIMEOUT"
)

// defaultShutdownTimeout is the API drain window. It is far longer than the
// previous hard-coded 5s: an in-flight SSE stream is signalled to end at a
// frame boundary first, and this bound only has to cover ordinary requests
// finishing plus the streams unwinding.
const defaultShutdownTimeout = 30 * time.Second

// poolConfigFromEnv resolves the PostgreSQL pool bounds. Values already present
// in MANAGED_AGENT_DATABASE_URL take precedence over these; see pg.PoolConfig.
func poolConfigFromEnv() (pg.PoolConfig, error) {
	maxConns, err := envIntOr(envDBMaxConns, pg.DefaultMaxConns)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	minConns, err := envIntOr(envDBMinConns, pg.DefaultMinConns)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	maxLifetime, err := envDurationOr(envDBMaxConnLifetime, pg.DefaultMaxConnLifetime)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	maxIdle, err := envDurationOr(envDBMaxConnIdleTime, pg.DefaultMaxConnIdleTime)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	healthCheck, err := envDurationOr(envDBHealthCheckPeriod, pg.DefaultHealthCheckPeriod)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	// A statement timeout of 0 means "leave statement_timeout unset". PoolConfig
	// spells that as a negative duration so its zero value can keep meaning
	// "use the default".
	statementTimeout, err := envDurationOrAllowZero(envDBStatementTimeout, pg.DefaultStatementTimeout)
	if err != nil {
		return pg.PoolConfig{}, err
	}
	if statementTimeout == 0 {
		statementTimeout = -1
	}
	return pg.PoolConfig{
		MaxConns:          int32(maxConns),
		MinConns:          int32(minConns),
		MaxConnLifetime:   maxLifetime,
		MaxConnIdleTime:   maxIdle,
		HealthCheckPeriod: healthCheck,
		StatementTimeout:  statementTimeout,
	}, nil
}

// workerConfigFromEnv resolves the Temporal worker concurrency, poller, and
// drain bounds.
func workerConfigFromEnv() (temporalpkg.WorkerConfig, error) {
	maxActivities, err := envIntOr(envWorkerMaxActivities, temporalpkg.DefaultMaxConcurrentActivities)
	if err != nil {
		return temporalpkg.WorkerConfig{}, err
	}
	maxWorkflowTasks, err := envIntOr(envWorkerMaxWorkflowTasks, temporalpkg.DefaultMaxConcurrentWorkflowTasks)
	if err != nil {
		return temporalpkg.WorkerConfig{}, err
	}
	activityPollers, err := envIntOr(envWorkerActivityPollers, temporalpkg.DefaultActivityPollers)
	if err != nil {
		return temporalpkg.WorkerConfig{}, err
	}
	workflowPollers, err := envIntOr(envWorkerWorkflowPollers, temporalpkg.DefaultWorkflowPollers)
	if err != nil {
		return temporalpkg.WorkerConfig{}, err
	}
	drainTimeout, err := envDurationOr(envWorkerDrainTimeout, temporalpkg.DefaultWorkerDrainTimeout)
	if err != nil {
		return temporalpkg.WorkerConfig{}, err
	}
	return temporalpkg.WorkerConfig{
		MaxConcurrentActivities:    maxActivities,
		MaxConcurrentWorkflowTasks: maxWorkflowTasks,
		ActivityPollers:            activityPollers,
		WorkflowPollers:            workflowPollers,
		DrainTimeout:               drainTimeout,
	}, nil
}

// envIntOr reads a positive integer, falling back to fallback when unset.
func envIntOr(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf(
			"configuration: %s must be a positive integer, got %q", name, value)
	}
	return parsed, nil
}

// envDurationOr reads a positive Go duration, falling back to fallback when
// unset.
func envDurationOr(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf(
			"configuration: %s must be a positive Go duration, got %q", name, value)
	}
	return parsed, nil
}

// envDurationOrAllowZero is envDurationOr for settings where "0" is a
// meaningful "disabled" value rather than an error.
func envDurationOrAllowZero(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf(
			"configuration: %s must be a non-negative Go duration, got %q", name, value)
	}
	return parsed, nil
}
