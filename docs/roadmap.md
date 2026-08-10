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

The Session Threads slice implements all five public read/archive/event
operations plus the ordinary coordinator execution path. `list_agents` and
`send_to_agent` are private model tools: delegation atomically creates or
resumes a persistent child Thread, appends the directional message and
lifecycle events, and enqueues an independent Temporal Workflow. Each child
uses its own Agent configuration, provider transcript, usage, retry state, and
event/preview stream while sharing Session infrastructure. A completed report
wakes the primary Thread for a later synthesis turn. Child confirmation,
custom-tool, and self-hosted tool requests are cross-posted to the primary
stream and their client responses route back to the owning Thread. Global and
targeted interrupts now use independently processable Thread receipts and the
same PostgreSQL finish race as the primary runtime. Child archive now closes
queued input and pending barriers in the same transaction as its terminal
projection and termination-dominant outbox intent. Session deletion terminates
all child Workflows before primary cleanup and physical cascade. Exact hosted
preview suppression for report-only turns and advisor Threads remain M2 work.

The living SDK baseline is now v1.62. Agent inference geography is persisted,
forwarded to every model request, and validated across coordinator rosters and
Session overrides. The new Session budget response fields are represented
truthfully as nullable, while non-null limits remain explicitly unsupported
until provider list-cost accounting can enforce one cap across all Threads.
The v1.62 advisor entry is tracked as a separate M2 runtime variant rather than
being folded into ordinary child-Agent execution.
Deployment budget templates follow the same boundary: their nullable wire
projection is present, and non-null values remain explicit `422` responses
until the Sessions they create can enforce the shared cap.

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
implemented. Session creation freezes the full resolved roster definitions,
including Session overrides on `self`. Independent Thread projections,
per-Thread public event, private provider-transcript, and MCP discovery
ownership, migration backfill, and atomic child creation from that frozen
roster are in place. Custom Skill retention, discovery, materialization, and
activation now select the resolved Agent execution scope used by each Thread;
primary/self paths remain compatible while external Agent paths are isolated.
The ordinary coordinator runtime now connects private `list_agents` and
`send_to_agent` model tools to replay-safe child creation/resume, a per-Thread
outbox and stable Temporal Workflow, independent model/tool execution,
Thread-scoped provider context and live previews, lifecycle cross-posting,
asynchronous reports, retry ownership, and Session status/usage aggregation.
Client-action waits are also Thread-owned: child confirmation, custom-tool, and
self-hosted tool requests are cross-posted with client-visible IDs, while
results are routed to the canonical child event and resume only that Thread's
complete action barrier. Global and targeted interrupts now route through every
affected Thread ledger and preserve the primary runtime's durable completion
race. Child archive now closes its projection, queued input, pending barriers,
lifecycle events, and termination-dominant outbox intent in one transaction;
Session deletion stops every child Workflow before primary cleanup and physical
cascade. The remaining M2 work is exact hosted preview suppression for
report-only turns, immutable child context snapshots, and the distinct advisor
lifecycle.
Advisor consultation Threads and shared Session budget enforcement follow the
ordinary child runtime because both depend on correct per-Thread usage and
Session-level aggregation, but retain their distinct public behavior.
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
