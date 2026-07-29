# managed-agent-go

An open-source, self-hosted managed agent runtime in Go with a Claude Managed
Agents-compatible HTTP API.

> [!IMPORTANT]
> **Alpha.** The primary runtime now uses PostgreSQL, Temporal, and NATS with
> separate API and worker processes. It implements a documented subset of the
> Managed Agents API and is still **not production-ready**.
> It is **independent and not an Anthropic product**. It is **not
> a drop-in production service. The default local sandbox is **not a security
> boundary**. See
> [Claude API coverage](docs/compatibility.md) for the supported integration
> surface and known differences.

## Why this project?

`managed-agent-go` explores a server-owned agent runtime: the service persists
session history, projects that history into stateless model requests, executes
tools in a replaceable sandbox, and exposes the result through a familiar
Managed Agents-compatible HTTP surface.

The runtime is the product; Claude API compatibility is an integration surface.
The project aims to support common Managed Agents workflows and the official Go
SDK for that documented subset. It does not aim to reproduce every upstream
field, feature, edge case, or internal execution detail one-for-one.

The current implementation provides:

- versioned agents and immutable per-session agent snapshots;
- environments, sessions, cursor pagination, and append-only session events;
- PostgreSQL event admission with a transactional outbox;
- one durable Temporal Workflow per Session, with the model/tool loop split
  into replay-safe Activities;
- NATS Core wakeups and previews reconciled against PostgreSQL cursors;
- a self-hosted model/tool loop with an offline deterministic model for tests;
- `bash`, `read`, `write`, `edit`, `glob`, and `grep` in local or Docker
  sandboxes (`web_fetch` and `web_search` are declared to the model but return a
  not-implemented tool result);
- live `agent.message` previews over cross-process SSE;
- black-box compatibility tests using the official Anthropic Go SDK.

## Project status

This is a **pre-release implementation**. PostgreSQL/Temporal/NATS is the
primary architecture and the default `serve` backend. The former SQLite
dispatcher remains only as a deprecated compatibility mode while the last
client-action and interrupt behaviors move to Temporal.

| Area | Current state |
| --- | --- |
| Agents | Create, get, list, update, versions, archive |
| Environments | Create, get, list, archive, delete |
| Sessions | Create, get, list, update title, archive, delete |
| Events | Send, list, SSE stream, opt-in message previews |
| Runtime | Temporal-owned multi-turn Messages API loop; `always_allow` built-ins |
| Sandboxes | Local development guardrail and optional Docker isolation; see the [backend matrix](docs/sandboxes.md) |
| Default storage | PostgreSQL (`pgx`, `sqlc`, embedded `goose` migrations) |
| Orchestration | Temporal Session Workflow + PostgreSQL admission outbox |
| Live delivery | NATS Core wakeups/previews + PostgreSQL cursor reconciliation |
| Legacy mode | `serve -backend sqlite`; frozen migration bridge, not a production target |

Important gaps include client-action waits and interrupts on the Temporal path,
durable sandbox leases/checkpoint restore across worker restarts, large-payload
offload, MCP execution, files/skills/memory, multiagent orchestration, worker
versioning, observability, and production deployment manifests.
See the [roadmap](docs/roadmap.md).

## Quick start

Requirements: Docker with Compose.

```bash
make -C deployments/local up
make -C deployments/local health
curl http://localhost:8080/readyz
```

This starts the API on `localhost:8080`, the Temporal UI on
`localhost:8233`, and the API's PostgreSQL, Temporal, NATS, and worker
dependencies. With no model configuration the worker uses an offline
deterministic model, so the complete path runs without credentials.

For source development, start the backing stack, then run the two process roles
in separate shells:

```bash
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"

go run ./cmd/managed-agent serve
# second shell, with the same environment:
go run ./cmd/managed-agent orchestrate
```

Follow the [getting started guide](docs/getting-started.md) to create an
environment, agent, and session.

`serve` binds to `127.0.0.1:8080` by default when run directly. `-strict` turns
on Claude wire-header validation; it is not authentication.

## Real model configuration

```bash
export MANAGED_AGENT_MODEL_BASE_URL=https://api.example.com
export MANAGED_AGENT_MODEL_API_KEY=replace-me
export MANAGED_AGENT_MODEL_ID=claude-model-id
export MANAGED_AGENT_MODEL_AUTH=x-api-key # or authorization-bearer
export MANAGED_AGENT_SANDBOX=docker

go run ./cmd/managed-agent orchestrate
```

Credentials are read only from the environment. Never commit them.

## Sandbox configuration

The default local sandbox confines paths to a temporary work directory, clears
the environment, applies a timeout, and caps output. It is a development
guardrail, **not a security boundary**, and must not run untrusted code.

Because of this, the server **refuses to start when a real model is configured
and the local sandbox is selected** — a real model can be steered into running
tool commands on the host with no isolation. Either select the Docker sandbox
(`MANAGED_AGENT_SANDBOX=docker`) or, at your own risk, set
`MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1` to override the check. The
zero-config offline fake model plus local sandbox always starts.

Docker provides stronger process and filesystem isolation:

```bash
export MANAGED_AGENT_SANDBOX=docker
export MANAGED_AGENT_SANDBOX_IMAGE=alpine:latest
go run ./cmd/managed-agent orchestrate
```

Docker sandboxes use `--network none` by default, but containers still share
the host kernel and this path has not been audited for hostile multi-tenant
workloads. Sandboxes are scoped to the session: the first run needing tools
provisions one and later runs in the same session reuse it, so filesystem state
persists across turns; the sandbox is released when the session is deleted. The
manager is in-memory, so a process restart does not restore an idle session's
sandbox.

## PostgreSQL/Temporal platform spine

The primary path routes HTTP resources and `user.message` events through
PostgreSQL admission, a coalescible outbox, an at-least-once Signal-With-Start
relay, and a durable `SessionWorkflow`. The plan-act-observe loop lives in
Workflow code. Each model call and
each always_allow built-in tool call is a separate Activity, so Temporal replay
preserves assistant text, multi-tool round structure, and completed tool results
without reconstructing conversation state from the journal. The PostgreSQL tool
journal preserves the `prepared → started → completed` boundary with
`ambiguous` branching from `started`: a completed step returns its durable result
without re-execution, while a step left `started` is refused rather than silently
replayed. Existing Workflow histories retain the previous `RunTurn` path through
Temporal version markers. The real integration suite also runs the new tool
Activity path through a Docker sandbox and verifies execution inside the
container.
The API and worker are separate roles but share one image. The local Compose
stack starts both:

```bash
make -C deployments/local up
make -C deployments/local health

curl http://localhost:8080/readyz
```

`serve -backend sqlite -db managed-agent.db` temporarily exposes the former
single-process compatibility path for client-action/interrupt comparison. New
infrastructure work must target PostgreSQL/Temporal; SQLite is removed after
those behaviors reach parity. See the
[platform spine milestone](docs/architecture/platform-spine-milestone.md).

## Documentation

- [Hosted documentation](https://yanpgwang.github.io/managed-agent-go/)
- [Getting started](docs/getting-started.md)
- [Sandbox backends](docs/sandboxes.md)
- [Architecture](docs/architecture.md)
- [Target platform and technology selection](docs/architecture/target-platform.md)
- [Managed Agents orchestration fit review](docs/architecture/orchestration-fit.md)
- [Domain model](docs/architecture/domain-model.md)
- [API reference](docs/api/overview.md)
- [Claude API coverage](docs/compatibility.md)
- [Roadmap](docs/roadmap.md)
- [Compatibility provenance](docs/provenance.md)

The documentation site uses Docusaurus Classic in docs-only mode:

```bash
cd website
npm install
npm start
```

## Development

```bash
go test ./...
go test -race ./...
go vet ./...

cd website
npm ci
npm run typecheck
npm run build
```

Default Go tests are offline. PostgreSQL integration tests opt in through the
documented test environment variables; the full suite also exercises real
Temporal, NATS, and Docker sandbox paths. PostgreSQL schema changes use embedded
versioned `goose` migrations.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a change and report
vulnerabilities through [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
