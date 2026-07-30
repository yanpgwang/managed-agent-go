<p align="center">
  <img src="assets/mango-logo.png" alt="Mango" width="420">
</p>

<hr>

<p align="center"><strong>Managed Agents, Native Go, On-demand.</strong></p>

**Mango** is an open-source, self-hosted agent runtime in Go with a
Claude Managed Agents-compatible HTTP API.

> [!IMPORTANT]
> **Alpha.** This project implements a documented subset of the Managed Agents
> API. It is independent, is not an Anthropic product, and is not yet a drop-in
> production service. The default local sandbox is not a security boundary.
> Check the [compatibility matrix](docs/compatibility.md) before depending on a
> capability.

## What it is

`managed-agent-go` owns durable agent sessions: it stores conversation history,
turns that history into stateless model requests, executes tools in a
replaceable sandbox, and streams results through a familiar HTTP API.

The runtime is the product; Claude API compatibility is an integration surface.
The implementation follows public wire behavior where useful without trying to
reproduce Anthropic's internal architecture.

## Architecture

The default path is a multi-process PostgreSQL, Temporal, and NATS architecture:

```mermaid
flowchart LR
  Client --> API["HTTP API"]
  API --> PG[("PostgreSQL")]
  API -. "fast-path signal" .-> Temporal
  PG -- "durable outbox" --> Worker
  Worker <--> Temporal
  Worker --> PG
  Worker --> Model["Messages API"]
  Worker --> Sandbox
  Worker -. "previews + wakeups" .-> NATS
  NATS -.-> API
```

| Component | Responsibility |
| --- | --- |
| PostgreSQL | Source of truth for Agents, Environments, Sessions, events, admission outbox, and tool journal |
| Temporal | Durable in-flight Session Workflow and replay-safe model/tool Activities |
| NATS Core | Ephemeral SSE wakeups and previews; never authoritative data |
| API process | Managed Agents-compatible HTTP resources and PostgreSQL cursor reads |
| Worker process | Temporal worker, outbox relay, model calls, and sandbox tools |

The boundaries are deliberate: a NATS outage cannot lose persisted events, and
a failed direct Temporal signal cannot lose admitted work because PostgreSQL's
outbox is the recovery path. See the [architecture overview](docs/architecture.md)
for the invariants and failure model.

## Current scope

| Area | Primary PostgreSQL/Temporal path |
| --- | --- |
| Agents | Create, get, list, update, versions, archive |
| Environments | Create, get, list, archive, delete; `cloud` only for Session execution |
| Sessions | Create, get, list, update title, archive, delete |
| Events | Process messages, interrupts, custom-tool results, and tool confirmations; store `user.define_outcome`; list, cursor pagination, and SSE |
| Runtime | Multi-round Messages API loop, durable interrupt and client-action park/resume, and built-in tools |
| Tools | `bash`, `read`, `write`, `edit`, `glob`, and `grep`; web tools currently return not implemented |
| Sandboxes | Local development provider and optional Docker isolation |
| Live delivery | Cross-process message previews and persisted-event wakeups over NATS |

Untargeted `user.interrupt` durably cancels an active model or tool Activity
across API and worker processes. Targeted multi-agent interrupts, generic
tool-result, and system-message inputs return an explicit `422`. MCP execution,
files/skills/memory/vaults, multi-agent orchestration, remote self-hosted
workers, schedules, and webhooks are not implemented.
See the [compatibility matrix](docs/compatibility.md) and
[roadmap](docs/roadmap.md) for details.

## Production readiness

The core data and orchestration boundaries are now the intended production
direction, so harness work can proceed without redesigning the scheduler.
Operating this as a production service still requires:

- authentication, tenant isolation, TLS, and secret management;
- Temporal Worker Versioning, rollout tests, dependency-aware readiness, and
  observability;
- remote sandbox provider adapters and orphan reconciliation;
- large-payload/object-storage offload, resource policies, and production
  deployment manifests.

Sandbox checkpoint/restore remains a provider capability rather than a format
implemented by this control plane.

## Quick start

Requirements: Docker with Compose.

```bash
make local-up
make local-health
curl http://localhost:8080/readyz
```

This builds and starts:

- API: <http://localhost:8080>
- Temporal UI: <http://localhost:8233>
- PostgreSQL, Temporal, NATS, and the worker

No credentials are required. The worker uses a deterministic offline model, so
the complete HTTP → PostgreSQL → Temporal → worker → SSE path works locally.

Continue with the [five-minute walkthrough](docs/getting-started.md) to create
an Environment, Agent, and Session and send the first message.

Stop the stack without deleting PostgreSQL data:

```bash
make local-down
```

Use `make local-down VOLUMES=1` only when you intentionally want to delete the
local database.

## Run from source

Start only the backing services:

```bash
docker compose -f deployments/local/compose.yaml \
  up -d postgres temporal temporal-ui nats

export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"
```

Then run the two application roles in separate shells with the same variables:

```bash
go run ./cmd/managed-agent serve
```

```bash
go run ./cmd/managed-agent orchestrate
```

The source API binds to `127.0.0.1:8080` by default. Pass `-addr` deliberately
to expose another interface. `-strict` validates required Claude wire headers;
it is not authentication.

## Use a real model

If the complete Compose stack is already running, stop its offline worker
before starting a configured source worker:

```bash
docker compose -f deployments/local/compose.yaml stop worker
```

Then configure and start the worker process:

```bash
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"

export MANAGED_AGENT_MODEL_BASE_URL=https://api.example.com
export MANAGED_AGENT_MODEL_API_KEY=replace-me
export MANAGED_AGENT_MODEL_ID=claude-model-id
export MANAGED_AGENT_MODEL_AUTH=x-api-key # or authorization-bearer
export MANAGED_AGENT_SANDBOX=docker

go run ./cmd/managed-agent orchestrate
```

`MANAGED_AGENT_SANDBOX` is a strict deployment-level provider selection:
`local` is the offline default; `docker`, `e2b`, `cube`, `opensandbox`, and
`daytona` are explicit opt-ins. Unknown provider names fail startup.
Provider-specific configuration is intentionally kept out of the Managed
Agents public API.

Do not run workers with different model or sandbox configuration on the same
Temporal Task Queue.

The worker refuses to pair a real model with the local sandbox because local
tool commands run on the host. Docker provides stronger isolation and disables
sandbox networking by default, but it is not certified for hostile multi-tenant
workloads. Read [Sandbox backends](docs/sandboxes.md) before using tools with
untrusted input. Keep model credentials in the environment and never commit
them.

## SQLite compatibility backend

The former single-process implementation remains available temporarily:

```bash
go run ./cmd/managed-agent serve -backend sqlite -db managed-agent.db
```

It is frozen, is not a production target, and remains only for transition
comparison until the PostgreSQL/Temporal conformance gate is complete.

## Documentation

- [Hosted documentation](https://yanpgwang.github.io/managed-agent-go/)
- [Getting started](docs/getting-started.md)
- [Compatibility matrix](docs/compatibility.md)
- [Architecture overview](docs/architecture.md)
- [Session lifecycle](docs/architecture/session-lifecycle.md)
- [Sandbox backends](docs/sandboxes.md)
- [Deployment model](docs/deployment.md)
- [API reference](docs/api/overview.md)
- [Roadmap](docs/roadmap.md)

## Development

```bash
make verify
make docs-check
make image-smoke
```

Create the optional developer-only environment file once:

```bash
make dev-env-init
$EDITOR ~/.config/mango/dev.env
scripts/with-dev-env go test ./...
```

The file is outside the repository, shared by worktrees, and must remain mode
`0600`. The wrapper exports it only to the requested command; the server never
silently reads a dotenv file. Rotate any credential that has been pasted into
chat, logs, or an issue before storing its replacement there.

`make verify` requires golangci-lint 2.12.x and checks changed Go code against
the standard lint set plus `gofmt`.

Default tests run offline. Real PostgreSQL, Temporal, NATS, and Docker paths are
covered by opt-in integration tests documented in
[`deployments/local`](deployments/local/README.md). PostgreSQL schema changes
use embedded, versioned `goose` migrations.

The repository-level `Makefile` is the stable entry point for development and
local deployment commands. See the [deployment model](docs/deployment.md) for
the support boundary between the local stack and future release bundles.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a change and report
vulnerabilities through [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
