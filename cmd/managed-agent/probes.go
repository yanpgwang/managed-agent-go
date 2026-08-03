package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yanpgwang/managed-agent-go/internal/health"
	"github.com/yanpgwang/managed-agent-go/internal/live"
	"go.temporal.io/sdk/client"
)

// Operational (non-CMA) configuration. The Claude Managed Agents API documents
// no health, readiness, or status endpoint, so everything here is a local
// deployment choice served outside /v1.
const (
	envHealthTimeout    = "MANAGED_AGENT_HEALTH_TIMEOUT"
	envHealthCacheTTL   = "MANAGED_AGENT_HEALTH_CACHE_TTL"
	envWorkerHealthAddr = "MANAGED_AGENT_WORKER_HEALTH_ADDR"
)

// defaultWorkerHealthAddr binds the worker's health listener to loopback for
// the same reason serve does: an operator who wants an orchestrator to reach it
// across the network must say so explicitly.
const defaultWorkerHealthAddr = "127.0.0.1:8081"

// postgresProbe reports whether the control-plane database is reachable.
// PostgreSQL is authoritative for accepted state, so an API process that cannot
// reach it must not be routed traffic.
func postgresProbe(pool *pgxpool.Pool) health.Probe {
	return health.Probe{
		Name: "postgres",
		Check: func(ctx context.Context) error {
			if pool == nil {
				return errors.New("postgres pool is not configured")
			}
			return pool.Ping(ctx)
		},
	}
}

// temporalProbe reports whether the durable-orchestration service is reachable.
func temporalProbe(c client.Client) health.Probe {
	return health.Probe{
		Name: "temporal",
		Check: func(ctx context.Context) error {
			if c == nil {
				return errors.New("temporal client is not configured")
			}
			_, err := c.CheckHealth(ctx, &client.CheckHealthRequest{})
			return err
		},
	}
}

// natsProbe reports whether the ephemeral live channel is reachable. NATS is a
// delivery optimization rather than a correctness dependency, but a process
// that cannot reach it serves degraded SSE latency, so readiness still fails
// closed.
func natsProbe(broker *live.Broker) health.Probe {
	return health.Probe{
		Name: "nats",
		Check: func(ctx context.Context) error {
			timeout := time.Second
			if deadline, ok := ctx.Deadline(); ok {
				if remaining := time.Until(deadline); remaining > 0 {
					timeout = remaining
				}
			}
			return broker.Ping(timeout)
		},
	}
}

// newReadinessChecker builds the dependency-aware readiness checker from the
// MANAGED_AGENT_HEALTH_* configuration.
func newReadinessChecker(probes ...health.Probe) (*health.Checker, error) {
	timeout, err := envDuration(envHealthTimeout)
	if err != nil {
		return nil, err
	}
	cacheTTL, err := envDuration(envHealthCacheTTL)
	if err != nil {
		return nil, err
	}
	return health.NewChecker(
		health.Config{Timeout: timeout, CacheTTL: cacheTTL},
		probes...,
	), nil
}

// startHealthListener serves the liveness and readiness endpoints on their own
// listener. The Temporal worker has no other HTTP surface, so without this it
// is unobservable to an orchestrator and Compose can only gate on the API.
// It returns the bound address so a ":0" configuration is still loggable.
func startHealthListener(
	addr string,
	prober health.Prober,
	logger *slog.Logger,
) (*http.Server, string, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	server := newHTTPServer(addr, health.Mux(prober, logger))
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("health listener stopped",
				slog.String("addr", addr),
				slog.String("error", serveErr.Error()),
			)
		}
	}()
	return server, listener.Addr().String(), nil
}
