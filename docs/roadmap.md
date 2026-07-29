---
title: Roadmap
slug: /roadmap
---

# Roadmap

The goal is a reliable, self-hosted managed agent runtime in Go. Claude Managed
Agents compatibility is the first client-facing integration surface, not a
requirement to reproduce every upstream feature one-for-one.

The roadmap is organized by outcome rather than by complete upstream API
parity. Detailed design constraints and completed mechanics live in the
[architecture guides](architecture.md). The production stack is no longer an
open question; it is fixed by the
[target-platform decision](architecture/target-platform.md).

## Platform spine: delivered primary path

The PostgreSQL/Temporal/NATS spine now serves the default HTTP backend:

- PostgreSQL with versioned `goose` migrations and `sqlc` queries;
- PostgreSQL-backed Agent, Environment, Session, and Event API resources;
- separate API and Temporal worker process roles;
- transactional event admission plus an outbox-to-Temporal Signal relay;
- a `SessionWorkflow` that accepts `user.message`, calls the model, executes
  `always_allow` built-ins, and commits authoritative events;
- stable operation IDs and the existing tool journal at the Activity boundary;
- NATS Core persisted-event wakeups and model previews, reconciled from
  PostgreSQL sequence cursors;
- Docker Compose API/worker/Temporal/PostgreSQL/NATS startup and end-to-end
  tests.

This still does not promise exactly-once execution for arbitrary shell
commands. Temporal owns orchestration recovery; the tool journal records the
irreducible uncertainty between an external side effect and its acknowledgment.

**Status (2026-07-29):** the spine is the primary path and is documented in
the [platform-spine milestone](architecture/platform-spine-milestone.md).
Delivered: PostgreSQL (`pgx` + `goose` + `sqlc`), transactional admission with a
coalescible outbox, an at-least-once Signal-With-Start relay, a `SessionWorkflow`
with a durable cursor, causal model history, one public idle per batch, and
Continue-As-New. The plan-act-observe loop now lives in Workflow code with one
Activity per model call and tool call; replay preserves assistant text,
multi-tool rounds, and completed tool results. The PostgreSQL journal retains
the `prepared → started → completed` boundary (`ambiguous` branches from
`started`): completed steps resume without re-execution, while a step left
`started` is refused as ambiguous. Attempt finalization and public completion
commit atomically. HTTP resource/session/event traffic now uses PostgreSQL;
NATS carries cross-process SSE wakeups and previews; API/worker Docker
containers exercise the complete path. PostgreSQL now persists client-action
waits atomically with `requires_action`, claims matching resolutions at
admission, and gates ordinary queued work until resume completion. Still open:
the Workflow park/resume loop, `user.interrupt`, durable sandbox leases, and
large-payload offload.

## Now: close the final infrastructure parity gates

- Connect custom tools and `always_ask` confirmations to the delivered
  PostgreSQL pending-action gate, then model their park/resume selection as a
  durable Workflow wait.
- Deliver `user.interrupt` as durable cross-process Workflow cancellation,
  including the finish-vs-interrupt ordering contract.
- Add Worker Versioning/rolling-upgrade tests and production observability.
- Define durable provider-backed sandbox leases; checkpoint/restore remains a
  sandbox-provider capability, not a home-grown scheduler feature.
- Keep the outbox limited to coalescible Workflow wakeups; it must not become
  another run scheduler.
- Preserve current causal history, pending-action gates, and event ordering with
  black-box compatibility tests.
- Delete the SQLite run queue and in-process recovery scheduler once the new
  path passes the client-action and interrupt gates.

## Next: complete the practical integration surface

- Use Anthropic server tools for supported `web_fetch`, `web_search`,
  `code_execution`, and tool-search behavior instead of recreating them.
- Resolve and execute MCP toolsets through the official MCP Go SDK.
- Add token usage accounting and model-request spans.
- Support aggregate resolution when a run parks on several client actions.
- Support aborting a session parked on `requires_action`.
- Complete the request and response fields, filters, pagination behavior, and
  event variants needed by real integrations.
- Expand preview support to thinking and span lifecycle events.

## Later: broaden runtime capabilities

- Add object-backed files, executable skills, and immutable memory versions.
- Add encrypted vault credentials using standard KMS and OAuth components.
- Add Daytona as the first managed sandbox adapter and Kubernetes Agent Sandbox
  as the first self-hosted production adapter.
- Model multi-agent threads as Temporal child Workflows sharing one sandbox.
- Implement outcomes, Temporal Schedules, and signed webhook delivery.
- Add further sandbox providers only behind the conformance-tested provider
  contract.

## Current foundation

The repository already provides:

- server-owned multi-turn history projected into stateless model requests;
- versioned agents and immutable per-session snapshots;
- atomic PostgreSQL event admission and a coalescible Temporal outbox;
- Workflow-owned restart recovery and causal reconstruction of prior output;
- a multi-step model and durable built-in tool loop;
- session-scoped local and Docker sandboxes;
- PostgreSQL cursor pagination, cross-process SSE, and NATS message previews;
- official Go SDK coverage for the supported API subset.

Harness work can now proceed in parallel because the main orchestration spine
is fixed. Removing the legacy path still depends on the two compatibility gates
above; it does not depend on sandbox checkpoint support.
