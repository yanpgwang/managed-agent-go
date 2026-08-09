---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango is focused on a reliable, self-hosted implementation of the core Claude
Managed Agents session API. Priorities are ordered by user-visible capability,
not by reproducing Anthropic's internal implementation.

## M1: core Session API alignment

The first release milestone is the single-agent Session harness exposed by the
official Agent setup, Sessions, Events, outcomes, and Tools contracts.

Implemented in the current M1 slice:

- effective model ID, effort, speed, session override, usage, and timing stats;
- all single-agent input event variants, companion system context, correlated
  model spans whose durable starts precede live message deltas, durable
  confirmation/custom/self-hosted result barriers, and cross-process interrupt;
- text-rubric outcome work with an isolated grader, revisions, terminal Session
  projection, usage, and interruption;
- lossless provider transcript plus conservative token-aware request projection,
  extractive compaction, and rich image/document/large-result handling;
- built-in sandbox tools, provider-native Web Search/Fetch, remote MCP basics,
  custom tools, and confirmation;
- capability-gated Environment package setup and deny-by-default limited
  networking through OpenSandbox, with unsupported adapters failing admission.

The M1 core conformance gate is closed by
[core compatibility statement v1.0.0](compatibility/core-v1.md). The statement
pins its upstream beta and SDK evidence, included operations, deployment
profile, and known limitations. Later capability work does not silently expand
that frozen claim.

The operation-level baseline lives in the [core API conformance
matrix](api/core-conformance.md). Files-backed outcomes and other broader
product surfaces remain outside the core gate.

Repeatable black-box comparison against the hosted Managed Agents API remains
useful optional validation when a Managed Agents-capable credential is
available. It is not a blocker for implementing behavior documented by the
public API contract.

## Post-core API alignment

The first broader resource slice is the five-operation Files API. PostgreSQL
stores metadata and lifecycle intents, S3-compatible storage holds bytes, and
the official Go SDK exercises upload, bidirectional list, metadata, content,
and delete behavior. This remains a conditional capability rather than an
expansion of the frozen M1 claim; see the [Files conformance
matrix](api/files-conformance.md).

The File-backed Session Resources slice now implements all five SDK operations,
create-time and runtime attachment, independent session-scoped File copies,
and durable read-only Docker mounts. It remains provider-gated; see the
[Session Resources conformance matrix](api/session-resources-conformance.md).
The custom Skills resource API, immutable archive lifecycle, strict
Agent/Session reference validation, and Session Version pinning are also in
place. Docker-backed cloud Sessions now add bounded model discovery, on-demand
full `SKILL.md` injection, checksum-verified read-only materialization, worker
reattachment, repair, and cleanup on the shared sandbox path. Other sandbox
adapters must prove the same capability contract before advertising Skill
runtime support. A hosted differential harness for post-compaction Skill
behavior remains follow-up conformance work.

The Memory slice now implements all fourteen Store, Memory, and immutable
Version operations. PostgreSQL is the canonical cross-Session store. Docker
Sessions can attach up to eight Stores at creation, expose their contents as
ordinary files beneath `/mnt/memory`, enforce read-only attachments, and write
multi-file changes back atomically with Session actor attribution. See the
[Memory conformance matrix](api/memory-conformance.md).

The Vault slice now implements all six Vault lifecycle operations, all six
Credential lifecycle operations, OAuth validation, ordered Session `vault_ids`,
and deterministic runtime MCP bearer resolution. Static bearer and OAuth access
tokens are injected per request; expired OAuth grants refresh and persist their
encrypted rotation. Cross-origin authenticated redirects are rejected.
Environment-variable SecretEgress follows after a sandbox provider can enforce
substitution without revealing the secret.

The Deployments slice now implements all ten Deployment and Deployment Run
operations. Deployments pin immutable Agent Versions, create Sessions manually
or from optional cron schedules, and retain success or failure Run records.
Scheduled claims are PostgreSQL-leased across workers; successful Session and
Run creation is atomic. See the [Deployments conformance
matrix](api/deployments-conformance.md).

The Environment Work slice now implements all eight worker-protocol operations
used by Anthropic's prebuilt `EnvironmentWorker` and CLI. A runnable admission
to a `self_hosted` Session creates or coalesces its Work activation in the same
PostgreSQL transaction as the event ledger and Temporal outbox. External
workers poll, acknowledge, heartbeat, and stop that lease while executing the
existing Session event and `user.tool_result` protocol; no second Agent runtime
or event state machine is introduced. See the [Environment Work guide](api/environment-work.md)
and [conformance matrix](api/environment-work-conformance.md). Environment-key
issuance and tenant-scoped authorization remain platform hardening work.

The Session Threads slice now implements all five public read/archive/event
operations. Each Session gets one durable primary-thread identity in the same
creation transaction. Its event history and live stream are views of the
existing Session ledger and NATS channel, so the HTTP surface does not create a
second source of truth. The child-thread runtime remains an M2 capability.

Skills and Memory are not deferred to multi-agent work. They provide reusable
capabilities and continuity to one Agent today, and later become foundations
that multi-agent orchestration can consume without inventing a second storage
or materialization model.

## Runtime integrations

The first integration slice now includes provider-native Web Search/Fetch and
remote MCP tool discovery and execution with optional Vault authentication.
Remaining work is:

- private-network MCP connectivity;
- explicit per-endpoint server-tool capability profiles;
- optional managed Web Search/Fetch executors that can honor `always_ask`;
- preview expansion beyond the core message and thinking types;
- promotion of preview sandbox adapters after recorded live conformance.

## M2: multi-agent

Official coordinator roster resolution and immutable Agent Version pinning are
implemented. The remaining M2 work is independent Thread projection,
child-thread creation, delegation/message execution, cross-posted confirmation,
context-compaction, and targeted interrupt events.
The primary Thread HTTP surface, single-agent event ledger, pending-action
barrier, private provider transcript, Skills, Memory, and Temporal turn loop
are intended to be reused rather than replaced.

## Deferred platform hardening

Production deployments additionally need:

- authentication, authorization, and tenant isolation;
- Worker Versioning and rolling-upgrade coverage;
- metrics, traces, audit logs, and operational runbooks;
- production object-storage policy, distributed Files reconciliation, and
  lifecycle operations for large histories and artifacts;
- quotas and resource policy;
- versioned deployment manifests and migration procedures.

Authentication/tenancy, provider routing, quotas, audit, metrics, backups, and
deployment management are intentionally not prerequisites for M1 API alignment.
The post-core Skills, Memory, Vaults, Deployments, and Webhooks surfaces do not
expand the frozen M1 claim. Current behavior is tracked in the
[compatibility matrix](compatibility.md); architectural guarantees are
documented in the [architecture overview](architecture.md).
