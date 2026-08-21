<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/mango-logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/mango-logo.svg">
    <img src="assets/mango-logo.png" alt="Mango" width="420">
  </picture>
</p>

<p align="center">
  <strong>The self-hosted, durable runtime for long-running AI agents.</strong>
</p>

<p align="center">
  <a href="https://yanpgwang.github.io/mango/">Documentation</a> ·
  <a href="https://yanpgwang.github.io/mango/getting-started">Getting started</a> ·
  <a href="https://yanpgwang.github.io/mango/product">Product direction</a> ·
  <a href="https://yanpgwang.github.io/mango/capabilities">Capabilities</a> ·
  <a href="https://yanpgwang.github.io/mango/architecture">Architecture</a>
</p>

<p align="center">
  <a href="https://github.com/yanpgwang/mango/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/yanpgwang/mango/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/yanpgwang/mango/actions/workflows/pages.yml"><img alt="Documentation" src="https://github.com/yanpgwang/mango/actions/workflows/pages.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="Apache 2.0 license" src="https://img.shields.io/github/license/yanpgwang/mango"></a>
</p>

Mango is a self-hosted control plane and execution runtime for durable AI agent
work: persistent Sessions, event streaming, tool orchestration, File and custom
Skill resources, Memory Stores, encrypted credentials, scheduled Deployments,
self-hosted worker leases, and pluggable sandbox execution. Its
production-oriented architecture is built in Go on PostgreSQL and Temporal.

Mango owns its public API and roadmap. Its original resource model was informed
by public agent-platform contracts, but users are not required to use an
Anthropic SDK and Mango does not pursue drop-in interoperability with a hosted
service. Mango may deliberately reuse or adapt sound public routes, resource
shapes, events, and SDK-exposed types; once adopted, they are Mango-owned and
may evolve independently.

## Why Mango

- **Own the runtime.** Keep state, orchestration, credentials, and execution on
  infrastructure you control, using Mango's documented HTTP API.
- **Keep accepted work durable.** Sessions, events, interrupts, tool calls, and
  client-action waits survive API and worker restarts.
- **Bring your own execution environment.** Choose local, Docker, E2B,
  CubeSandbox, OpenSandbox, or Daytona sandbox adapters.
- **Run the whole stack locally.** Start without external model credentials;
  the Compose stack supplies a development-only Mango API key.
- **Inspect every turn.** Query the persisted event history, stream live
  previews over SSE, and inspect active workflows in Temporal UI.

## Quick start

You need Docker with Compose and `make`.

```bash
git clone https://github.com/yanpgwang/mango.git
cd mango
make local-up
make local-health
```

Verify that Mango is ready:

```bash
curl -i http://localhost:8080/readyz
```

The local stack uses a deterministic offline model, so no model API key is
required. Protected Mango routes use the development key
`sk-mango-local-development`; health and readiness remain public.
Follow the [five-minute walkthrough](https://yanpgwang.github.io/mango/getting-started)
to create an Environment, Agent, and Session, then send and stream your first
message.

```bash
make local-down
```

## Capabilities and stability

> [!IMPORTANT]
> Mango is in alpha, has no customers, and has no supported stable API.
> `/v1` is its single development API namespace, not a claim that Mango 1.0
> exists. Routes, fields, schemas, and behavior may all change
> directly on `/v1`; earlier development snapshots are not preserved through
> `/v2` or compatibility layers. The project does not yet claim production
> readiness. Review
> [capabilities and limits](https://yanpgwang.github.io/mango/capabilities)
> before relying on a workflow. The default local sandbox is for development
> and is not a security boundary.

| Area | Current support |
| --- | --- |
| Core resources | Agent, Environment, and Session lifecycle, versioning, filtering, and pagination |
| Events and runtime | Messages, interrupts, custom-tool results, confirmations, outcomes, retries, SSE, and durable park/resume |
| Tools | Sandbox built-ins, provider-native Web Search/Fetch, and remote MCP tools with optional Vault-backed bearer authentication |
| Files | Object-backed File lifecycle, reusable outcome rubrics, File-backed Session Resources, and idle-boundary publication of Docker and remote-sandbox `/mnt/session/outputs` deliverables |
| Skills | Custom Skill lifecycle, immutable Version pins, and on-demand instruction loading in Docker Sessions |
| Memory | Store, Memory, and immutable Version lifecycle; durable read/write or read-only Docker mounts at `/mnt/memory` |
| Vaults | Encrypted Vault and Credential lifecycle plus ordered Session attachment, live OAuth validation, and automatic token refresh; environment-variable egress remains in progress |
| Deployments | Deployment and Run lifecycle, pinned Agent versions, manual runs, and PostgreSQL-leased cron scheduling |
| Environment Work | Worker leases, transactional self-hosted Session activation, heartbeats, and reclaim |
| Session Threads | Persistent coordinator delegation plus Mango-managed Advisor consultations over ordinary client tool calls, with independent context, usage, lifecycle events, reports, isolated event/preview streams, routed client-action waits, and durable interrupts |
| Sandboxes | Local and Docker available; E2B, CubeSandbox, OpenSandbox, and Daytona in Preview |

The [capability summary](https://yanpgwang.github.io/mango/capabilities)
states the user-visible boundary. Persistent child-Agent orchestration,
provider-neutral Advisor consultations, and shared Session budgets are
implemented. Active product work may live directly in focused pull requests;
[GitHub Issues](https://github.com/yanpgwang/mango/issues) are used when
discussion, sequencing, or longer-term tracking is useful.

## Architecture

```mermaid
flowchart LR
  Client --> API["Mango API"]
  API --> PG[("PostgreSQL")]
  API --> Objects[("S3-compatible storage")]
  API <-- "work lease + Session events" --> SelfHostedWorker["EnvironmentWorker"]
  SelfHostedWorker --> CustomerSandbox["Customer-hosted sandbox"]
  PG -- "durable outbox" --> Worker
  Worker <--> Temporal
  Worker --> Model["Model provider"]
  Worker --> Sandbox
  Worker -. "live previews" .-> NATS
  NATS -.-> API
```

PostgreSQL owns public state, event history, Memory contents and Versions, and
File/Skill lifecycle intents.
An S3-compatible store owns File bytes and immutable Skill archives. Temporal
owns in-flight execution. NATS
carries only ephemeral wakeups and previews; persisted events are always
reconciled from PostgreSQL. A lost signal, process restart, or NATS outage
cannot discard accepted work.

Read the [architecture overview](https://yanpgwang.github.io/mango/architecture)
for the failure model, transactional outbox, tool journal, interrupt ordering,
and sandbox lifecycle.

## Documentation

| I want to… | Read |
| --- | --- |
| Run my first agent session | [Getting started](https://yanpgwang.github.io/mango/getting-started) |
| Connect a real model endpoint | [Use a real model endpoint](https://yanpgwang.github.io/mango/getting-started#use-a-real-model-endpoint) |
| Choose an execution backend | [Sandbox backends](https://yanpgwang.github.io/mango/sandboxes) |
| Run a coordinator and child Agents | [Multi-agent guide](https://yanpgwang.github.io/mango/guides/multi-agent) |
| Check an API operation | [API reference](https://yanpgwang.github.io/mango/api) |
| Understand supported behavior | [Capabilities and limits](https://yanpgwang.github.io/mango/capabilities) |
| Plan a deployment | [Deployment model](https://yanpgwang.github.io/mango/deployment) |

The complete documentation is also published at
[yanpgwang.github.io/mango](https://yanpgwang.github.io/mango/).

## Development

```bash
make verify       # lint, unit tests, race tests, and vet
make docs-check   # type-check and build the documentation site
make image-smoke  # build and smoke-test the container image
```

Default tests are offline. PostgreSQL, Temporal, NATS, MinIO, Docker, model,
and remote-sandbox integrations have explicit opt-in suites. See the
[local stack guide](deployments/local/README.md) and
[contribution guide](CONTRIBUTING.md).

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

Mango is licensed under the [Apache License 2.0](LICENSE).
