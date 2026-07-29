# Local development stack

This directory builds and runs the complete local control plane with pinned
infrastructure versions and health checks.

| Service    | Image                          | Ports                       | Purpose                                              |
| ---------- | ------------------------------ | --------------------------- | ---------------------------------------------------- |
| PostgreSQL | `postgres:17.5-alpine`         | `5432`                      | Application event ledger, projections, admission outbox (and Temporal's own persistence, on separate databases). |
| Temporal   | `temporalio/auto-setup:1.29.7` | `7233` (gRPC)               | Durable session/thread orchestration.                |
| Temporal UI| `temporalio/ui:2.52.1`         | `8233` → container `8080`   | Workflow explorer at <http://localhost:8233>.        |
| NATS Core  | `nats:2.11.17-alpine`          | `4222` (client), `8222` (monitoring) | Ephemeral previews and SSE wakeups; PostgreSQL cursor reads repair loss. |
| API        | `managed-agent-go:local`        | `8080`                      | PostgreSQL-backed Managed Agents-compatible HTTP API. |
| Worker     | `managed-agent-go:local`        | —                           | Temporal worker and PostgreSQL outbox relay. |

The Go module pins the matching client libraries: `go.temporal.io/sdk`,
`github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, and
`github.com/nats-io/nats.go`. See the root `go.mod` for exact versions.

## Startup

From the repository root:

```sh
make local-up       # start everything in the background
make local-health   # block until all services are healthy
```

`make health` returns only once Postgres accepts connections, the Temporal
frontend answers `cluster health`, NATS `/healthz` is green, and the API
answers `/readyz`.

Without `make`:

```sh
docker compose -f deployments/local/compose.yaml up -d --build
docker compose -f deployments/local/compose.yaml ps
```

## Connection strings

```sh
# Application database (pgx / goose / sqlc)
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"

# Temporal frontend (Go SDK client)
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_TEMPORAL_NAMESPACE="default"

# NATS Core live channel
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"
```

The `default` Temporal namespace is created automatically by `auto-setup`.

## Health checks

Each service declares a Docker `healthcheck`:

- **postgres** — `pg_isready -U postgres -d managed_agent`
- **temporal** — `tctl --address temporal:7233 cluster health`
- **nats** — HTTP `GET /healthz` on the monitoring port
- **api** — HTTP `GET /readyz`

`docker compose ps` shows `(healthy)` once each passes.

## Teardown

```sh
make local-down            # stop containers, keep data
make local-down VOLUMES=1  # also delete the Postgres volume
```

## Scope

This stack is for local development and integration tests only. It already
keeps API and worker process roles separate, but it is not a production
deployment manifest: authentication, TLS, secrets, rolling worker versioning,
managed persistence, observability, resource limits, and object storage remain
deployment work. See
[the deployment model](../../docs/deployment.md) and
[target-platform](../../docs/architecture/target-platform.md).
