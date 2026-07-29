<p align="center">
  <img src="assets/mango-logo.png" alt="Mango" width="760">
</p>

<h1 align="center">managed-agent-go</h1>

<p align="center"><strong>Managed Agents, Native Go, On-demand.</strong></p>

**Mango** is an open-source, self-hosted managed agent runtime in Go with a
Claude Managed Agents-compatible HTTP API.

> [!IMPORTANT]
> **Alpha.** The default server is an early, experimental, **single-node**
> runtime (SQLite, one process) that implements a documented subset of the
> Managed Agents API. A feature-gated PostgreSQL/Temporal execution plane is
> available for platform-spine development, but HTTP traffic has not been cut
> over to it.
> It is **independent and not an Anthropic product**. It is **not
> production-ready**: do not use it as a drop-in production service. The default
> local sandbox is **not a security boundary**. See
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
- a durable single-node run queue backed by SQLite with restart recovery;
- a self-hosted model/tool loop with an offline deterministic model for tests;
- `bash`, `read`, `write`, `edit`, `glob`, and `grep` in local or Docker
  sandboxes (`web_fetch` and `web_search` are declared to the model but return a
  not-implemented tool result);
- custom-tool handoff and live `agent.message` previews over SSE;
- black-box compatibility tests using the official Anthropic Go SDK.

## Project status

This is a **pre-release implementation**. The default public server remains
single-node; an additive PostgreSQL/Temporal path now exercises the production
orchestration boundary without replacing the SQLite API path. The public API
intentionally covers only the workflows listed below.

| Area | Current state |
| --- | --- |
| Agents | Create, get, list, update, versions, archive |
| Environments | Create, get, list, archive, delete |
| Sessions | Create, get, list, update title, archive, delete |
| Events | Send, list, SSE stream, opt-in message previews |
| Runtime | Multi-turn Messages API loop and custom-tool handoff |
| Sandboxes | Local development guardrail and optional Docker isolation; see the [backend matrix](docs/sandboxes.md) |
| Default storage | SQLite; one process; at-least-once restart recovery |
| Platform spine | Feature-gated PostgreSQL ledger + Temporal Workflow-owned agent loop; not traffic-cut-over |

Important gaps include HTTP/PostgreSQL cutover, client-action waits and
interrupts on the Temporal path, durable sandbox leases/checkpoint restore
across worker restarts, large-payload offload, MCP execution,
files/skills/memory, multiagent orchestration, and production deployment
manifests.
See the [roadmap](docs/roadmap.md).

## Quick start

Requirements: Go 1.26 or newer.

```bash
go run ./cmd/managed-agent serve -db managed-agent.db
```

By default the server binds to `127.0.0.1:8080` (loopback only), so a fresh
start never exposes the unauthenticated API on all interfaces. Pass `-addr` to
change the bind address (for example `-addr :8080` to listen on all
interfaces — only do this behind your own network controls).

With no model configuration the server uses an offline deterministic model, so
the binary and tests work without network access or credentials.

Local development mode is intentionally lenient. `-strict` is **header
validation, not authentication**: it only requires that the following Claude
API wire headers are present and valid — it does not verify any credential or
grant access control.

- `x-api-key` or `authorization`;
- `anthropic-version: 2023-06-01`;
- `anthropic-beta: managed-agents-2026-04-01`;
- `content-type: application/json` for requests with bodies.

Follow the [getting started guide](docs/getting-started.md) to create an
environment, agent, and session.

## Real model configuration

```bash
export MANAGED_AGENT_MODEL_BASE_URL=https://api.example.com
export MANAGED_AGENT_MODEL_API_KEY=replace-me
export MANAGED_AGENT_MODEL_ID=claude-model-id
export MANAGED_AGENT_MODEL_AUTH=x-api-key # or authorization-bearer
export MANAGED_AGENT_SANDBOX=docker

go run ./cmd/managed-agent serve
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
go run ./cmd/managed-agent serve
```

Docker sandboxes use `--network none` by default, but containers still share
the host kernel and this path has not been audited for hostile multi-tenant
workloads. Sandboxes are scoped to the session: the first run needing tools
provisions one and later runs in the same session reuse it, so filesystem state
persists across turns; the sandbox is released when the session is deleted. The
manager is in-memory, so a process restart does not restore an idle session's
sandbox.

## Platform spine (experimental, feature-gated)

The first Temporal/PostgreSQL platform-spine slice is implemented alongside the
default SQLite path — it does **not** replace it. It routes one `user.message`
end to end through PostgreSQL admission, a coalescible outbox, an at-least-once
Signal-With-Start relay, and a durable `SessionWorkflow` — including a tool-using
turn whose plan-act-observe loop lives in Workflow code. Each model call and
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
Start the local stack and run the execution plane:

```bash
make -C deployments/local up
make -C deployments/local health

export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"     # default
export MANAGED_AGENT_TEMPORAL_NAMESPACE="default"           # default
go run ./cmd/managed-agent orchestrate
```

The default `serve` command is unchanged and reads none of these variables. See
the [platform spine milestone](docs/architecture/platform-spine-milestone.md)
for scope, configuration, tests, and explicit limitations.

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

Default Go tests are offline. Schema migrations are not maintained during the
pre-release phase; recreate local development databases after incompatible
schema changes.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a change and report
vulnerabilities through [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
