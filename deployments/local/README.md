# Local development stack

This directory brings up the backing services the Temporal platform-spine slice
needs, with pinned versions and health checks.

| Service    | Image                          | Ports                       | Purpose                                              |
| ---------- | ------------------------------ | --------------------------- | ---------------------------------------------------- |
| PostgreSQL | `postgres:17.5-alpine`         | `5432`                      | Application event ledger, projections, admission outbox (and Temporal's own persistence, on separate databases). |
| Temporal   | `temporalio/auto-setup:1.29.7` | `7233` (gRPC)               | Durable session/thread orchestration.                |
| Temporal UI| `temporalio/ui:2.52.1`         | `8233` → container `8080`   | Workflow explorer at <http://localhost:8233>.        |
| NATS Core  | `nats:2.11-alpine`             | `4222` (client), `8222` (monitoring) | Ephemeral previews / SSE wakeups (wired in a later slice). |

The Go module pins the matching client libraries: `go.temporal.io/sdk`,
`github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, and
`github.com/nats-io/nats.go`. See the root `go.mod` for exact versions.

## Startup

```sh
make -C deployments/local up       # start everything in the background
make -C deployments/local health   # block until all services are healthy
```

`make health` polls Docker's own health status for each service, so it returns
only once Postgres accepts connections, the Temporal frontend answers
`cluster health`, and NATS `/healthz` is green.

Without `make`:

```sh
docker compose -f deployments/local/docker-compose.yml up -d
docker compose -f deployments/local/docker-compose.yml ps
```

## Connection strings

```sh
# Application database (pgx / goose / sqlc)
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"

# Temporal frontend (Go SDK client)
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_TEMPORAL_NAMESPACE="default"

# NATS (later slice)
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"
```

The `default` Temporal namespace is created automatically by `auto-setup`.

## Health checks

Each service declares a Docker `healthcheck`:

- **postgres** — `pg_isready -U postgres -d managed_agent`
- **temporal** — `tctl --address temporal:7233 cluster health`
- **nats** — HTTP `GET /healthz` on the monitoring port

`docker compose ps` shows `(healthy)` once each passes.

## Teardown

```sh
make -C deployments/local down            # stop containers, keep data
make -C deployments/local down VOLUMES=1  # also delete the Postgres volume
```

## Scope

This stack is for local development and integration tests only. It is not a
production deployment manifest — production topology (split API/worker,
Temporal Cloud or self-hosted cluster, managed Postgres, object storage) is out
of scope for this milestone. See
[target-platform](../../docs/architecture/target-platform.md).
