---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango is a clean-room implementation of the public Claude Managed Agents (CMA)
contract. This page records what is planned, in what order, and — just as
importantly — what the project deliberately will not build.

Phases are ordered by importance in the official CMA documentation, not by
implementation convenience. Mango currently implements three of the thirteen
documented Beta API resource families: agents, environments, and sessions. The
single-agent session harness works end-to-end; current behavior is tracked in
the [compatibility matrix](compatibility.md).

## Scope: what belongs in this project

**Mango's OSS scope is the documented CMA API surface, plus the infrastructure
needed to run it single-tenant and self-hosted.**

Explicitly out of scope:

- multi-tenancy and org/workspace isolation;
- RBAC and SSO;
- quotas, spend limits, and billing;
- a management Console.

The reasoning is that none of these are part of the API contract Mango
reproduces. `organization_id` and `workspace_id` appear nowhere in the resource
schemas — only inside webhook payload envelopes, as attribution on an event
Anthropic's hosted service emits. Rate limits are documented as Anthropic
organization policy rather than endpoint behavior. A self-hosted deployment
therefore has no documented tenancy semantics to conform to, and inventing them
would mean inventing wire surface.

Authentication **is** in scope. Upstream documents `x-api-key` throughout, and
memory versions carry an `api_key_id` actor identity, so the API key is an
observable part of the contract rather than a deployment concern. Real key
validation and principal attribution land with memory stores in Phase 5, where
`api_key_id` first becomes visible on the wire. An interim honest auth check
comes earlier, in Phase 0.

## Verification findings that shaped this plan

Five conclusions from checking the upstream snapshot reversed earlier
assumptions. They are recorded here because each one is a decision *not* to
build something that looks plausible:

1. **No SSE `id:` or `retry:` frames.** The documentation never mentions `id:`,
   `retry:`, or `Last-Event-ID`. The documented reconnect is application-level:
   open a new stream, seed seen IDs from the event history endpoint, then tail
   live and skip duplicates. Emitting `id:` would make browser `EventSource`
   clients auto-send `Last-Event-ID` on reconnect for a resume capability that
   does not exist. Comment-frame keepalives remain safe.
2. **No closed-enum model validation.** `BetaManagedAgentsModel` is an open
   union — a list of known IDs *or* an arbitrary string. Mango's existing
   free-form handling is correct, so enum validation was dropped from Phase 1.
3. **Sandbox provisioning stays lazy.** Upstream provisions a session's sandbox
   "when the session first needs it," and the 30-day retention clock starts at
   sandbox creation without being extended by activity. Provisioning eagerly
   would burn retention for nothing. Mounts must therefore be materialized at
   provision time rather than by forcing provisioning earlier.
4. **Rejecting `vault_ids` on session update is conformant.** Upstream marks the
   field "not yet supported; requests setting this field are rejected." This is
   documented behavior Mango matches, not a Mango gap. (Create-time `vault_ids`
   *is* supported upstream, and remains a genuine gap.)
5. **Only `type: "file"` resources can be added to a running session.** GitHub
   repositories and memory stores are create-time only. This reshaped Phase 3.

## Phases

### Phase 0 — conformance debt and operability *(in progress)*

Seven independent changes, currently in review; none are merged yet.

- Reject unknown nested fields in `tools`, `mcp_servers`, `skills`, and
  `multiagent`, which are otherwise persisted verbatim and echoed back.
- Agent and environment list parameters, plus environment update. The two list
  endpoints are asymmetric upstream and do not share a parameter shape.
- Complete `POST /v1/sessions/{id}` beyond title-only: tool and MCP replacement
  (requiring `idle` status), per-key metadata patching, and a conformant
  `vault_ids` rejection.
- Distinct `agent.mcp_tool_use` and `agent.mcp_tool_result` events instead of
  emitting MCP calls as plain tool use. This crosses the pending-action barrier
  and needs a workflow version gate.
- Observability: structured logging, request-ID propagation, real `/readyz`
  probes, a worker health surface, and SSE comment-frame keepalives.
- Runtime tuning — connection pool, worker concurrency, and drainable shutdown —
  and honest authentication to replace the current presence-only header check.
- Commit the contract-first working material and correct the documentation.

### Phase 1 — close M1 single-agent conformance

Start-only `agent.thinking` previews, which are a progress signal carrying no
thinking content; periodic `span.outcome_evaluation_ongoing`; the full typed
`session.error` union with retry status; `stop_reason.retries_exhausted`; and
per-model context budgets replacing the single global default. Compaction stays
a local choice, since the upstream compaction event is opaque.

### Phase 2 — Files API and blob storage

The five `/v1/files*` endpoints, and the only resource family using classic
`after_id`/`before_id` pagination. Uploaded files outlive sessions, so this
requires storage outside the sandbox. It unblocks four live rejections: file
content blocks, file rubrics, session resources, and deliverable download.
Ingesting agent-produced output is system-prompt mediated rather than a
filesystem contract, so its trigger and timing are a documented local choice.

### Phase 3 — session resources

`POST`/`GET`/`DELETE` on session resources, for files only. Mount paths are
server-controlled, including a server-derived memory slug. Adding mounts to the
sandbox spec invalidates every stored spec hash, which is a migration-visible
change. Provisioning stays lazy.

### Phase 4 — skills

Nine endpoints, multipart upload, sandbox mounting, and execution. Skills are
already accepted and persisted today; only execution is missing.

### Phase 5 — memory stores

Fourteen endpoints, a `/mnt/memory/` mount with filesystem-level read-only and
read-write enforcement, content-hash preconditions, and a version audit trail.
**Real API-key storage and principal attribution land here**, because this is
where `api_key_id` becomes observable.

### Phase 6 — vaults and credentials

Thirteen endpoints with envelope encryption. Unblocks authenticated MCP.

### Phase 7 — multi-agent (M2)

Session threads, roster, delegation, targeted interrupts, and the
`agent.thread_*` events. Note the upstream path asymmetry: thread events live
under `.../threads/{id}/events`, but the thread stream is `.../threads/{id}/stream`
with no `/events` segment.

### Phase 8 — self-hosted work queue

The eight `/v1/environments/{id}/work/*` endpoints, including lease heartbeats
with an expected-heartbeat precondition.

### Phase 9 — deployments and webhooks

Scheduled deployments and deployment runs, which map naturally onto Temporal
Schedules, plus webhooks: 42 event types with bounded retry and auto-disable.

### Phase 10 — research-preview surfaces

Tunnels, dreams, and user profiles.

## Continuous work

Not tied to a phase, and expected to run alongside it:

- an OpenAPI specification and a route-drift gate, replacing today's stub;
- Temporal `WorkflowReplayer` tests, required by the
  [architecture overview](architecture.md) but currently absent;
- a `sqlc` drift gate and coverage reporting;
- fuzzing on the event validators;
- production deployment manifests and Worker Versioning.

## Working method

Every phase follows the contract-first method in the repository instructions:
identify the relevant official guide and API reference first, label each claim
as documented contract, design inference, or local choice, then add focused
conformance tests. The official sources behind existing decisions are recorded
in [compatibility provenance](provenance.md).
