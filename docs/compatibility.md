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
| Session lifecycle | Supported | Create from latest or pinned agent versions, preserve an immutable resolved snapshot, get, list, update title, archive, and delete. Several upstream response fields remain empty placeholders. |
| Session listing | Limited | Bidirectional cursor pagination and core agent, status, archive, and time filters work. Deployment and memory matching are not implemented. |
| Event send and list | Supported | Implemented event variants use one persisted tagged-union shape and durable per-trigger processing. The complete upstream event union is not implemented. |
| SSE event stream | Supported | Streams new persisted events and supports open-stream-then-list reconciliation. It does not replay history or support `Last-Event-ID`; fan-out is process-local. |
| Live message previews | Limited | Opt-in `agent.message` start/delta frames are ephemeral and never persisted. Thinking and span previews are not implemented. |
| Built-in tool loop | Limited | `bash`, `read`, `write`, `edit`, `glob`, and `grep` execute. `web_fetch` and `web_search` return a not-implemented result. |
| Sandbox execution | Limited | Session-scoped local and Docker providers execute built-ins. Provider selection is process-global; Environment config does not yet choose a backend, and restart cannot reattach or restore a sandbox. See the [backend matrix](sandboxes.md). |
| Custom tools | Supported | A custom tool can park a run, persist a pending action, accept the matching result, and resume. Aggregate resolution of several pending actions is not implemented. |
| Tool confirmations | Supported | One `always_ask` built-in can park and resume through allow or deny. Crash replay after an allowed side effect remains possible without a durable side-effect journal. |
| User interrupt | Limited | Cancels the active run in a single process and single-agent session. Parked-session abort, `session_thread_id` targeting, durable cancellation, and cross-process delivery are not supported. |
| MCP execution | Not supported | MCP toolset references parse and persist but are not resolved or executed. |
| Files, skills, memory, and vaults | Not supported | These product surfaces are outside the current runtime slice. |
| Multi-agent orchestration | Not supported | `multiagent` configuration can be stored, but rosters, threads, delegation, and orchestration are not executed. |
| Distributed workers | Not supported | The current topology is one process with SQLite, an in-memory stream hub, and at-least-once restart recovery. |

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
