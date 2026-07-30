---
title: Claude API coverage
slug: /compatibility
---

# Claude API coverage

`managed-agent-go` is a self-hosted managed agent runtime with a practical
Claude Managed Agents-compatible HTTP surface. Compatibility makes existing
clients easier to adopt; reproducing every upstream product feature or internal
execution detail one-for-one is not a project goal.

This page describes the user-visible integration surface:

- **Supported** means the documented workflow is implemented and exercised
  end-to-end for the scope described here.
- **Limited** means the workflow is usable with important constraints.
- **Not supported** means the server may accept or preserve part of the shape,
  but does not provide the corresponding behavior.

The official Anthropic Go SDK is used as a black-box client for core lifecycle
tests. That verifies useful interoperability, not universal drop-in
compatibility.

## Coverage summary

| Area | Status | Current scope and limitations |
|---|---|---|
| Agent lifecycle | Supported | Create, get, list, version, update with optimistic concurrency, and archive. Agent list filtering and pagination are not implemented. |
| Agent configuration | Limited | Model, system, tools, MCP references, skills, metadata, and opaque `multiagent` values are stored. MCP resolution, skills execution, and multi-agent orchestration are not supported. |
| Environment lifecycle | Limited | Create, get, list, archive, and delete work for local `cloud` records. Environment pagination and a remote self-hosted worker protocol are not implemented. |
| Session lifecycle | Supported | Create from latest or pinned agent versions, preserve an immutable resolved snapshot, get, list, update title, archive, and delete. Delete waits for durable sandbox teardown; a provider outage can return an error while the fenced cleanup Workflow continues and a retry safely joins it. Several upstream response fields remain empty placeholders. |
| Session listing | Limited | Bidirectional cursor pagination and core agent, status, archive, and time filters work. Deployment and memory matching are not implemented. |
| Event send and list | Limited | The PostgreSQL path durably processes `user.message`, untargeted `user.interrupt`, `user.custom_tool_result`, and `user.tool_confirmation`, and stores `user.define_outcome`. Generic tool-result, system-message, and targeted multi-agent interrupt inputs return an explicit `422`. |
| SSE event stream | Supported | Streams new persisted events across API/worker processes. NATS wakes subscribers and PostgreSQL sequence reads repair missed notifications. It does not replay history or support `Last-Event-ID`. |
| Live message previews | Limited | Opt-in `agent.message` start/delta frames cross NATS and are never persisted. Thinking and span previews are not implemented. |
| Built-in tool loop | Limited | `always_allow` `bash`, `read`, `write`, `edit`, `glob`, and `grep` execute as durable Temporal Activities. `web_fetch` and `web_search` return a not-implemented result. |
| Sandbox execution | Limited | Session-scoped local, Docker, E2B, CubeSandbox, OpenSandbox, and Daytona providers persist an opaque PostgreSQL binding, reattach after worker restart, and clean up through a durable deletion workflow. Remote adapters are Preview; OpenSandbox has additionally passed manual live conformance against its Docker runtime. Provider selection remains process-global. See the [backend matrix](sandboxes.md). |
| Custom tools | Supported | Custom calls park on an atomic multi-action barrier. Partial results remain idle; the final result resumes the same logical model loop with all tool results before queued messages continue. |
| Tool confirmations | Supported | `always_ask` built-ins park durably. Allow executes the original server-owned call through the tool journal; deny returns an error tool result with the optional denial message. |
| User interrupt | Limited | An untargeted interrupt durably cancels an active model or tool Activity across API and worker processes. PostgreSQL defines finish-vs-interrupt ordering, emits one idle `end_turn`, and fences uncertain started tool steps as ambiguous. Targeted multi-agent interrupts are not supported. |
| MCP execution | Not supported | MCP toolset references parse and persist but are not resolved or executed. |
| Files, skills, memory, and vaults | Not supported | These product surfaces are outside the core harness scope. |
| Multi-agent orchestration | Not supported | `multiagent` configuration can be stored, but rosters, threads, delegation, and orchestration are not executed. |
| Distributed workers | Limited | API and Temporal worker roles are separate and can be replicated around PostgreSQL/NATS. Sandbox ownership is durable: local and Docker references require workers connected to the same host filesystem or daemon, while remote references can reattach from workers sharing the same provider configuration. Task queues must remain configuration-homogeneous. Worker Versioning, production manifests, and rollout tests remain open. |

## Backend transition

`serve` defaults to the PostgreSQL control plane. `serve -backend sqlite`
temporarily retains the former single-process behavior for transition
comparison. This is not a commitment to maintain two backends: SQLite is frozen
and will be removed after the PostgreSQL/Temporal conformance gate passes.

## Integration contract

For supported workflows, the project aims to keep request and response shapes,
status codes, headers, event correlation, and SDK usage stable. Known
limitations should produce an explicit validation or not-supported result when
possible rather than silently claiming upstream behavior.

The project does **not** promise:

- that every official SDK method or upstream field works;
- exact parity for undocumented behavior and error wording;
- Anthropic-internal scheduling, storage, or orchestration semantics;
- production-grade authentication, multi-tenancy, or distributed execution.

See the [API reference](api/overview.md) for repository behavior and
[compatibility provenance](provenance.md) for the official public sources used
when shaping this integration surface.

## How coverage is verified

Compatibility-related changes should use the smallest evidence appropriate to
the behavior:

- raw HTTP tests for JSON shapes, status codes, headers, and validation;
- official Go SDK tests for client interoperability;
- application and store tests for durable execution semantics;
- end-to-end tests for runtime, tool, interrupt, and streaming workflows.

Test names and edge-case details live beside the implementation and in the
architecture guides rather than in this user-facing coverage table.
