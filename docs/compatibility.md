---
title: Claude API coverage
slug: /compatibility
---

# Claude API coverage

`managed-agent-go` is a self-hosted runtime that aims to align the public Claude
Managed Agents API contract. The HTTP resources and observable event semantics
follow the official surface; the internal scheduler, storage, and sandbox
implementation remain independent.

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
| Model configuration | Supported | Model ID, effort (`low` through `max`), and speed (`standard`/`fast`) resolve every working and grader request. The semantic defaults (`high`/`standard`) are omitted on the Messages wire for compatibility; non-default effort and `fast` are forwarded and require endpoint support. Same-model Agent updates preserve omitted effort; changing model resets omitted fields to defaults. |
| Agent configuration | Limited | System, tools, MCP references, metadata, and model configuration execute. Skills and multi-agent orchestration are not yet executed. |
| Environment lifecycle | Supported | Create, get, list, archive, and delete support `cloud` and `self_hosted`. Cloud sandbox tools execute on the worker; self-hosted sandbox tools park for `user.tool_result` from the client. Pagination is not implemented. |
| Session lifecycle | Supported | Create from latest or pinned agent versions, apply session-local overrides, preserve an immutable resolved snapshot, get, list, update title, archive, and delete. Usage, timing stats, and outcome evaluations are live projections rather than placeholders. Unsupported resources/vaults are rejected at create time, so their required response arrays truthfully remain empty. |
| Session listing | Limited | Bidirectional cursor pagination and core agent, status, archive, and time filters work. Deployment and memory matching are not implemented. |
| Event send and list | Limited | All core single-agent input event types are validated and durably processed: `user.message`, `user.interrupt`, `user.tool_confirmation`, `user.custom_tool_result`, self-hosted `user.tool_result`, `user.define_outcome`, and companion `system.message`. Targeted multi-agent interrupts remain unsupported. Model request and outcome spans carry correlated per-request usage. |
| SSE event stream | Supported | Streams new persisted events across API/worker processes. NATS wakes subscribers and PostgreSQL sequence reads repair missed notifications. It does not replay history or support `Last-Event-ID`. |
| Live message previews | Limited | Opt-in `agent.message` start/delta frames cross NATS and are never persisted. Thinking previews are not implemented, and durable model-span pairs currently publish at turn commit rather than streaming `span.model_request_start` before the first message preview. |
| Outcomes | Limited | Text-rubric outcomes drive an independent grader context, revision cycles, terminal evaluation state, usage accounting, and interrupt handling. File rubrics require the not-yet-implemented Files resource; periodic public `span.outcome_evaluation_ongoing` heartbeats are not emitted. |
| Context management | Limited | A lossless provider transcript is committed atomically with each turn. Requests use conservative token-aware projection, legal tool-use/result boundaries, extractive compaction, and rich image/document/tool-result preservation; compacted rich history remains recoverable from the private transcript. The budget is currently a server default rather than an endpoint/model-specific context profile, and the estimate is not a provider tokenizer. |
| Built-in tool loop | Limited | In cloud environments, `always_allow` `bash`, `read`, `write`, `edit`, `glob`, and `grep` execute as durable Temporal Activities; `web_fetch` and `web_search` use the configured Messages endpoint as native server tools and retain provider-private continuation blocks. In self-hosted environments every built-in, including Web, is client-executed through `user.tool_result`. Native Web requires `always_allow` plus endpoint support. |
| Sandbox execution | Limited | Session-scoped local, Docker, E2B, CubeSandbox, OpenSandbox, and Daytona providers persist provisioning intent plus an opaque PostgreSQL binding, reconcile the create-before-binding crash window, reattach after worker restart, and clean up through a durable deletion workflow. Remote adapters are Preview; OpenSandbox has additionally passed manual live conformance against its Docker runtime. Provider selection remains process-global. See the [backend matrix](sandboxes.md). |
| Custom tools | Supported | Custom calls park on an atomic multi-action barrier. Partial results remain idle; the final result resumes the same logical model loop with all tool results before queued messages continue. |
| Tool confirmations | Supported | Interceptable `always_ask` built-ins and MCP tools park durably. Allow executes the original server-owned call through the tool journal; deny returns an error tool result with the optional denial message. Provider-native Web Search/Fetch cannot be intercepted and reject `always_ask`. |
| User interrupt | Limited | An untargeted interrupt durably cancels an active model, outcome grader, or tool Activity across API and worker processes. PostgreSQL defines finish-vs-interrupt ordering, closes model spans, emits one idle `end_turn`, and fences uncertain started tool steps as ambiguous. Targeted multi-agent interrupts are not supported. |
| MCP execution | Limited | Remote tools are discovered over unauthenticated Streamable HTTP, pinned per Session, permission-checked, journaled, and executed with large or binary results materialized in the Session sandbox. Authentication, private-network access, deprecated-SSE fallback, resources, and prompts are not supported. |
| Files, skills, memory, and vaults | Not supported | These product surfaces are outside the core harness scope. File-backed outcome rubrics and file-sourced message content are rejected explicitly; inline or URL image/document content still participates in the provider transcript and context projection. |
| Multi-agent orchestration | Not supported | `multiagent` configuration can be stored, but rosters, threads, delegation, and orchestration are not executed. |
| Distributed workers | Limited | API and Temporal worker roles are separate and can be replicated around PostgreSQL/NATS. Sandbox ownership is durable: local and Docker references require workers connected to the same host filesystem or daemon, while remote references can reattach from workers sharing the same provider configuration. Task queues must remain configuration-homogeneous. Worker Versioning, production manifests, and rollout tests remain open. |

## Runtime architecture

`serve` has one authoritative control-plane path: PostgreSQL for resources,
events, projections, and admission; Temporal for durable orchestration; and
NATS Core for ephemeral wakeups and previews.

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
- application and PostgreSQL tests for durable execution semantics;
- end-to-end tests for runtime, tool, interrupt, and streaming workflows.

Test names and edge-case details live beside the implementation and in the
architecture guides rather than in this user-facing coverage table.
