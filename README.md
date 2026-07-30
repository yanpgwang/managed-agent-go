<p align="center">
  <img src="assets/mango-logo.png" alt="Mango" width="420">
</p>

<h1 align="center">Mango</h1>

<p align="center">
  <strong>The durable, open-source implementation of the core Claude Managed Agents API.</strong>
</p>

<p align="center">
  Run CMA-compatible agent sessions on your own infrastructure with
  PostgreSQL-backed state, Temporal-powered execution, and pluggable sandboxes.
</p>

<p align="center">
  <a href="https://github.com/yanpgwang/managed-agent-go/actions/workflows/ci.yml"><img src="https://github.com/yanpgwang/managed-agent-go/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://yanpgwang.github.io/managed-agent-go/"><img src="https://img.shields.io/badge/docs-online-blue" alt="Documentation"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache 2.0 License"></a>
</p>

Mango is an independent agent runtime written in Go. It implements a practical
subset of the Claude Managed Agents HTTP API, persists server-owned sessions,
turns their history into stateless Messages API requests, executes tools in
replaceable sandboxes, and streams results through a familiar event API.

For the supported surface, clients can use raw HTTP or the official Anthropic Go
SDK with a different base URL. Compatibility is the adoption surface; the
durable runtime is the product.

## Built for the failure paths

A happy-path agent loop is easy to demonstrate. Mango is designed around what
happens after the API accepts work: processes crash, wakeups are missed, tools
are interrupted, clients take time to approve actions, and sandboxes outlive
workers.

| Failure path | Runtime behavior |
| --- | --- |
| API or worker exits after accepting input | PostgreSQL commits the input events, running status, and a recoverable outbox wakeup in one transaction |
| A Temporal signal or NATS wakeup is missed | The durable relay and PostgreSQL sequence reads repair delivery; neither fast path is authoritative |
| A model or tool step is retried or interrupted | Temporal owns the in-flight Workflow, while the tool journal records completed and ambiguous side-effect boundaries |
| A custom tool or confirmation waits on a client | Pending actions are persisted and resume the same logical model loop after a process restart |
| A worker restarts or a Session is deleted | The persisted sandbox binding is reattached or durably cleaned up through the provider lifecycle |

These guarantees are exercised through application, store, Workflow, sandbox
conformance, raw HTTP, and official-SDK tests. See the
[architecture overview](docs/architecture.md) for the invariants and failure
model.

> **Industry context.**
> [Anthropic describes Managed Agents](https://www.anthropic.com/engineering/managed-agents)
> as stable, replaceable boundaries between the session, harness, and sandbox.
> [Cursor reports](https://cursor.com/blog/cloud-agent-lessons) moving its
> long-running cloud agents to Temporal and separating agent execution, machine
> state, and conversation state. These are non-normative references, not
> endorsements or compatibility evidence.

## What you get

- **Claude Managed Agents compatibility** for the core Agent, Session, and
  Event workflows, verified through the official Anthropic Go SDK and raw HTTP
  contract tests.
- **Durable multi-process execution** using PostgreSQL as the source of truth
  and Temporal for replay-safe orchestration.
- **Resumable tool and human-in-the-loop flows** with durable interrupts,
  custom-tool results, and `always_ask` confirmations.
- **Pluggable Session sandboxes** across local, Docker, E2B, CubeSandbox,
  OpenSandbox, and Daytona providers.
- **Live event delivery** with persisted SSE events and optional low-latency
  text previews over NATS.

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

## Status and compatibility

> [!IMPORTANT]
> **Alpha.** Mango implements a documented subset of the Claude Managed Agents
> API. It is independent, is not an Anthropic product, and does not yet claim
> full drop-in or production compatibility. The default local sandbox is not a
> security boundary. Check the
> [compatibility matrix](docs/compatibility.md) before depending on a capability.

| Area | Current PostgreSQL/Temporal surface |
| --- | --- |
| Agents | Create, get, list, update, versions, and archive |
| Environments | Create, get, list, archive, and delete; `cloud` only for Session execution |
| Sessions | Create, get, list, update title, archive, and delete |
| Events | Process messages, interrupts, custom-tool results, and tool confirmations; store `user.define_outcome`; list, cursor pagination, and SSE |
| Runtime | Multi-round Messages API loop, durable interrupt, client-action park/resume, and built-in tools |
| Tools | `bash`, `read`, `write`, `edit`, `glob`, and `grep`; web tools currently return not implemented |
| Sandboxes | Local and Docker available; E2B, CubeSandbox, OpenSandbox, and Daytona in Preview |
| Live delivery | Cross-process message previews and persisted-event wakeups over NATS |

Untargeted `user.interrupt` durably cancels an active model or tool Activity
across API and worker processes. Targeted multi-agent interrupts, generic
tool-result, and system-message inputs return an explicit `422`. MCP execution,
files/skills/memory/vaults, multi-agent orchestration, remote self-hosted
workers, schedules, and webhooks are not implemented.

See the [compatibility matrix](docs/compatibility.md) for exact behavior and the
[roadmap](docs/roadmap.md) for planned work.

## Architecture

The default deployment has separate API and worker roles:

```mermaid
flowchart LR
  Client --> API["Managed Agents HTTP API"]
  API --> PG[("PostgreSQL")]
  API -. "fast-path signal" .-> Temporal
  PG -- "durable outbox" --> Worker
  Worker <--> Temporal
  Worker --> PG
  Worker --> Model["Messages API"]
  Worker --> Sandbox["Sandbox provider"]
  Worker -. "previews + wakeups" .-> NATS
  NATS -.-> API
```

| Component | Responsibility |
| --- | --- |
| PostgreSQL | Source of truth for Agents, Environments, Sessions, events, admission outbox, sandbox bindings, and tool journal |
| Temporal | Durable in-flight Session Workflow and replay-safe model/tool Activities |
| NATS Core | Ephemeral SSE wakeups and previews; never authoritative data |
| API process | Managed Agents-compatible HTTP resources and PostgreSQL cursor reads |
| Worker process | Temporal worker, outbox relay, model calls, and sandbox tools |

The boundaries are deliberate: a NATS outage cannot lose persisted events, and
a failed direct Temporal signal cannot lose admitted work because PostgreSQL's
outbox is the recovery path.

## Production readiness

The data and orchestration boundaries are the intended production direction,
but operating Mango as a hardened multi-tenant service still requires:

- authentication, authorization, tenant isolation, TLS, and secret management;
- Temporal Worker Versioning, rollout tests, dependency-aware readiness, and
  observability;
- provider-aware routing, promotion of Preview sandbox adapters, and orphan
  reconciliation;
- large-payload/object-storage offload, quotas, resource policies, and
  production deployment manifests.

Sandbox checkpoint/restore remains a provider capability rather than a format
implemented by this control plane.

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
