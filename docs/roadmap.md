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

## Next implementation slice: the platform spine

The highest-value next step is one end-to-end Temporal-backed session path,
rather than adding more behavior to the SQLite dispatcher. It establishes:

- PostgreSQL with versioned `goose` migrations and `sqlc` queries;
- a local Temporal service and Go worker;
- transactional event admission plus an outbox-to-Temporal Signal relay;
- a `SessionWorkflow` that accepts input, calls the model, executes one tool
  step, parks/resumes, and commits authoritative events;
- stable operation IDs and the existing tool journal at the Activity boundary;
- fault-injection tests for API retry, worker restart, and ambiguous tool
  completion.

This still does not promise exactly-once execution for arbitrary shell
commands. Temporal owns orchestration recovery; the tool journal records the
irreducible uncertainty between an external side effect and its acknowledgment.

**Status (2026-07-29):** the spine is landing incrementally and is documented in
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
commit atomically. The real path is validated with local and Docker sandbox
providers. Still open on this path: client-action park/resume, `user.interrupt`,
large-payload offload, and cutting the HTTP API over from SQLite.

## Now: replace infrastructure, preserve semantics

- Establish OpenAPI-generated wire types and PostgreSQL migrations.
- Move session orchestration, retries, cancellation, client-action waits, and
  timers to Temporal.
- Keep the outbox limited to coalescible Workflow wakeups; it must not become
  another run scheduler.
- Preserve current causal history, pending-action gates, and event ordering with
  black-box compatibility tests.
- Delete the SQLite run queue and in-process recovery scheduler once the new
  path passes the target architecture's acceptance gates.
- Replace the in-process stream hub with PostgreSQL cursor replay plus NATS Core
  previews/wakeups, then split API and worker processes.

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
- atomic event admission and one durable run per processable trigger;
- single-node restart recovery and causal reconstruction of prior run output;
- a multi-step model and built-in tool loop;
- session-scoped local and Docker sandboxes;
- custom-tool and `always_ask` confirmation park/resume flows;
- single-process active-run interruption;
- persisted events, cursor pagination, SSE, and opt-in message previews;
- official Go SDK coverage for the supported API subset.

The immediate release threshold is the Temporal/PostgreSQL vertical slice with
the current behavior preserved. Production topology follows by splitting the
same API and worker boundaries, not by evolving a second orchestration system.
