---
title: API compatibility
slug: /compatibility
---

# API compatibility

Mango is an independent, self-hosted runtime that follows the documented
Claude Managed Agents HTTP contract. Compatibility describes observable API
and runtime behavior; Mango does not reproduce Anthropic's private scheduler,
storage, sandbox, or control plane.

Use this page to decide whether a workflow is ready for your deployment:

- **Supported** — implemented and exercised end to end for the stated scope.
- **Limited** — usable with constraints that may affect architecture or
  operations.
- **Preview** — implemented, but live-provider or production evidence is not
  yet strong enough for a compatibility commitment.
- **Not supported** — rejected explicitly rather than silently accepted.

Mango exposes all 90 operations represented by the pinned official Go SDK.
Operation count is not a parity claim: accepted fields, durable behavior,
provider capabilities, and deployment constraints still matter. Compatibility
claims are enforced by the repository's HTTP, SDK, PostgreSQL, Temporal, and
service test suites.

## Capability summary

| Capability | Status | Supported scope and important constraints |
| --- | --- | --- |
| Agents and Versions | Supported | Create, get, list, update, immutable Version history, archive, filters, and pagination. Model ID, effort, speed, and `inference_geo` reach working and grader requests. |
| Environments | Supported | Cloud and self-hosted lifecycle, package configuration, limited-network declarations, filters, and pagination. Package execution requires a capable sandbox; limited egress is currently enforced only by OpenSandbox. |
| Sessions | Supported | Create from immutable Agent snapshots, get/list/update/archive/delete, metadata, filters, usage, timing, and resource projections. Deletion fences admission and durably releases the Workflow and sandbox. |
| Events and client actions | Supported | All documented client input variants, system context, messages, thinking, tool events, confirmation/custom/self-hosted result barriers, outcomes, retries, and interrupts. Periodic `session.usage` and `session.budget_reached` events are not emitted. |
| Event streaming | Supported | PostgreSQL-authoritative Session and Thread streams with NATS wakeups, cursor repair, bounded backpressure, and opt-in ephemeral text previews. Streams do not replay history or interpret `Last-Event-ID`. |
| Model and context runtime | Limited | Durable provider-native transcripts, conservative token projection, extractive compaction, rich content, and immutable compacted-child projections are implemented. Provider-exact tokenizers, model context profiles, and complete per-request audit snapshots remain open. |
| Sandbox tools | Limited | `bash`, `read`, `write`, `edit`, `glob`, and `grep`, plus provider-native Web Search/Fetch. Local is development-only; Docker is the current mount-capable isolated backend. |
| MCP tools | Limited | Streamable HTTP discovery/execution, permissions, journaled calls, large-result materialization, and Vault bearer/OAuth authentication. Private-network connectivity, deprecated SSE, MCP resources, and prompts are not supported. |
| [Files](api/files.md) | Limited | Five operations with configured S3-compatible storage and crash-recoverable intents. Client uploads are not downloadable; message content, outcome rubrics, and arbitrary output export remain open. Startup reconciliation currently assumes one Files-enabled API replica. |
| [Session Resources](api/session-resources.md) | Limited | Five operations, independent File copies, and create-time Memory attachments. Runtime File attach/detach works. Mount execution currently requires Docker; GitHub repository resources are not supported. |
| [Skills](api/skills.md) | Limited | Nine custom resource operations, immutable Version pins, strict bundle validation, Docker materialization, and on-demand Claude Code-style instruction injection. Anthropic-managed, GitHub, self-hosted, and remote-sandbox activation remain open. |
| [Memory](api/memory.md) | Limited | Fourteen Store/Memory/Version operations, immutable history, SHA-256 preconditions, Docker read/write mounts, and deletion-time writeback. Non-Docker mounts and automatic retention are not implemented. |
| [Vaults](api/vaults.md) | Limited | Thirteen Vault/Credential operations, encrypted storage, ordered Session attachment, OAuth validation, expiry refresh, and token rotation. Environment-variable egress and refresh-failure webhooks are not implemented. |
| [Deployments](api/deployments.md) | Limited | Ten Deployment/Run operations, pinned Agent Versions, manual runs, cron scheduling, leases, and atomic success/failure records. Shared budget enforcement, GitHub resources, and Agent-archive propagation remain open. |
| [Environment Work](api/environment-work.md) | Limited | All eight self-hosted worker-protocol operations and official Go `WorkPoller` interoperability. Environment-key issuance, tenant-scoped authorization, Work secrets, and health-check Work remain open. |
| [Multi-agent](guides/multi-agent.md) | Limited | Persistent ordinary child Agents, independent Workflows/transcripts/events/usage, reports, action routing, interrupts, retries, archive, deletion, and durable context-compaction checkpoints. Exact report-only preview behavior, advisors, and shared list-cost budgets remain open. |
| [Sandbox adapters](sandboxes.md) | Limited / Preview | Local and Docker are available. E2B, CubeSandbox, OpenSandbox, and Daytona have durable bindings but remain Preview pending repeated live conformance and production routing policy. |
| Distributed operation | Limited | API and worker roles scale independently around PostgreSQL, Temporal, and NATS. Worker Versioning, heterogeneous-provider routing, distributed Files reconciliation, and production rollout evidence remain open. |

## Explicitly outside the current claim

Mango does not currently claim:

- hosted-infrastructure equivalence or undocumented error wording;
- production authentication, authorization, or tenant isolation;
- quota, billing, audit, backup, or observability completeness;
- a supported Kubernetes or production Compose distribution;
- safe hostile multi-tenant execution on the local or Docker sandbox;
- every field or method added by a future upstream SDK release.

Unsupported behavior should fail with an explicit validation or capability
error whenever it can be detected at admission time.

## Verification boundary

Compatibility changes use raw HTTP/OpenAPI tests, the official Go SDK as a
black-box client, PostgreSQL transaction tests, Temporal replay and integration
tests, and real PostgreSQL/Temporal/NATS/MinIO/Docker service tests.

Mango has not published a versioned release. Published versions will appear in
[GitHub Releases](https://github.com/yanpgwang/managed-agent-go/releases). See
[Roadmap](roadmap.md) for remaining work.
