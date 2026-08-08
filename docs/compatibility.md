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

This page is the living coverage view. Per-operation evidence lives in the
conformance matrices, and the frozen claim lives in the versioned statement:

- [Core API conformance matrix](api/core-conformance.md) — the single-agent
  surface.
- [Files conformance matrix](api/files-conformance.md) — the first post-core
  resource slice.
- [Session Resources conformance matrix](api/session-resources-conformance.md) —
  the File attachment slice.
- [Skills conformance matrix](api/skills-conformance.md) — custom resources,
  reference validation, and immutable Version pinning.
- [Memory conformance matrix](api/memory-conformance.md) — Stores, Memories,
  immutable Versions, and Docker-backed Session mounts.
- [Vault conformance matrix](api/vaults-conformance.md) — encrypted Vault and
  Credential control-plane behavior and its runtime boundary.
- [Core compatibility statement v1.0.0](compatibility/core-v1.md) — the frozen
  claim against the `managed-agents-2026-04-01` beta and Anthropic Go SDK
  `v1.61.0`.

The official Anthropic Go SDK is used as a black-box client for core lifecycle
tests, which verifies useful interoperability rather than universal drop-in
compatibility.

## Coverage summary

| Area | Status | Current scope and limitations |
|---|---|---|
| Agent lifecycle | Supported | Create, get, list, version, update with optimistic concurrency, and archive. List supports the documented archive/time filters and forward cursor pagination over the latest version of each Agent; version history supports its documented forward pagination. |
| Model configuration | Supported | Model ID, effort (`low` through `max`), and speed (`standard`/`fast`) resolve every working and grader request. The semantic defaults (`high`/`standard`) are omitted on the Messages wire for compatibility; non-default effort and `fast` are forwarded and require endpoint support. Same-model Agent updates preserve omitted effort; changing model resets omitted fields to defaults. |
| Agent configuration | Limited | System, tools, MCP references, metadata, and model configuration execute. Admission rejects undocumented fields, malformed optional values, and custom tools that omit their required name, description, or object input schema; tool collections, custom-tool fields, and MCP tool-config names enforce the documented bounds. Runtime parsing remains tolerant of historical stored fields so upgrades do not break existing Sessions or replay. Custom-tool `input_schema` remains an open JSON Schema beyond its required object wrapper. Custom Skills execute conditionally on Docker-backed cloud Sessions; multi-agent orchestration is not yet executed. |
| Environment lifecycle | Supported | Create, get, list, update, archive, and delete support `cloud` and `self_hosted`; mutable resource fields, self-hosted scope, Environment type, default response shapes, configured package lists, limited networking, and delete responses round-trip through the official SDK. Metadata update is a per-key patch, cloud and limited-network config updates preserve omitted nested fields, and archived Environments are read-only. Cloud sandbox tools execute after configured `apt`, `cargo`, `gem`, `go`, `npm`, or `pip` packages install on an isolated backend. Package setup is once per Session sandbox rather than cached across Sessions sharing an Environment. OpenSandbox enforces deny-by-default host allowlists, MCP endpoint expansion, temporary setup registries, and final package-registry access. Incapable deployments return `422`; the local backend also rejects non-empty package configuration. Self-hosted sandbox tools park for `user.tool_result` from the client. |
| Session lifecycle | Supported | Create from latest or pinned agent versions, apply session-local overrides, preserve an immutable resolved snapshot, attach ordered active Vault references, get, list, archive, and delete. Update accepts `title`, a per-key `metadata` patch, and a session-local `agent.tools`/`agent.mcp_servers` full replacement that requires an idle session and applies from the next turn; update-time `vault_ids` is rejected, matching upstream. Usage, timing stats, outcome evaluations, and active Session Resources are live projections. |
| Session Resources | Limited | The five SDK operations and create-time File and Memory Store resources are implemented. File attachments own independent downloadable copies and durable read-only Docker mounts. Memory Store attachments are creation-only, snapshot their presentation metadata, and use read/write or read-only Docker mounts beneath `/mnt/memory`. Update explicitly rejects the GitHub-only token field, and runtime add/remove of Memory Stores is rejected. File admission requires configured object storage; both variants require a cloud Environment and the relevant Docker capability. GitHub repository resources remain unsupported. |
| Session listing | Limited | Bidirectional cursor pagination and core agent, status, archive, time, and Memory Store filters work. Deployment matching is not implemented. |
| Event send and list | Limited | All core single-agent input event types are validated and durably processed: `user.message`, `user.interrupt`, `user.tool_confirmation`, `user.custom_tool_result`, self-hosted `user.tool_result`, `user.define_outcome`, and companion `system.message`. Their nested content, source, search-result, and rubric variants reject malformed or undocumented fields before admission. List filters, ordering, and opaque forward cursors use `processed_at`, with deterministic null and timestamp-tie handling. Server output includes privacy-preserving `agent.thinking`, messages, the documented built-in/MCP tool variants, Session status/error events, and correlated model/outcome spans. Tool-use events report `evaluated_permission: allow` when execution proceeds and `ask` when the Session parks for confirmation. Targeted multi-agent interrupts remain unsupported. |
| SSE event stream | Supported | Streams new persisted events across API/worker processes. NATS wakes subscribers and PostgreSQL sequence reads repair missed notifications. Replacement API processes preserve the documented open-stream-then-list recovery path, and a bounded slow subscriber is dropped without delaying healthy subscribers or the durable ledger. The stream does not replay history or support `Last-Event-ID`. |
| Live event previews | Supported | Each `span.model_request_start` is durably appended before its provider request can emit an opt-in preview. `agent.message` carries start/delta frames; `agent.thinking` is start-only and never exposes reasoning. Preview frames cross NATS and are never persisted. The authoritative event closes a successful preview, while the correlated `span.model_request_end` closes an error or interrupt; request correlation prevents delayed frames from leaking into a later model round. |
| Outcomes | Limited | Text-rubric outcomes drive an independent grader context, revision cycles, terminal evaluation state, usage accounting, and interrupt handling. Each evaluation publishes a durable start before grader execution, periodic public `span.outcome_evaluation_ongoing` heartbeats while it runs, and a correlated terminal end. File rubrics remain unsupported until Files are wired into outcome evaluation. |
| Context management | Limited | A lossless provider transcript is committed atomically with each turn, including signed and redacted thinking continuation blocks that never expose private reasoning in the public ledger. Requests use conservative token-aware projection, legal tool-use/result boundaries, extractive compaction, and rich image/document/tool-result preservation; compacted rich history remains recoverable from the private transcript. The budget is currently a server default rather than an endpoint/model-specific context profile, and the estimate is not a provider tokenizer. |
| Built-in tool loop | Limited | In cloud environments, `always_allow` `bash`, `read`, `write`, `edit`, `glob`, and `grep` execute as durable Temporal Activities; `web_fetch` and `web_search` use the configured Messages endpoint as native server tools and retain provider-private continuation blocks. In self-hosted environments every built-in, including Web, is client-executed through `user.tool_result`. Native Web requires `always_allow` plus endpoint support. |
| Sandbox execution | Limited | Session-scoped local, Docker, E2B, CubeSandbox, OpenSandbox, and Daytona providers persist provisioning intent plus an opaque PostgreSQL binding, install Environment packages before publishing that binding, reconcile the create-before-binding crash window, reattach after worker restart, and clean up through a durable deletion workflow. OpenSandbox additionally enforces and dynamically reconciles limited egress before a binding becomes visible and after worker restart. Package installation requires the selected image to contain each requested manager. Remote adapters are Preview; OpenSandbox has passed manual live lifecycle conformance against its Docker runtime, while limited-egress service conformance remains an operator responsibility. Provider selection remains process-global. See the [backend matrix](sandboxes.md). |
| Custom tools | Supported | Custom calls park on an atomic multi-action barrier. Partial results remain idle; the final result resumes the same logical model loop with all tool results before queued messages continue. |
| Tool confirmations | Supported | Interceptable `always_ask` built-ins and MCP tools park durably. Allow executes the original server-owned call through the tool journal; deny returns an error tool result with the optional denial message. Provider-native Web Search/Fetch cannot be intercepted and reject `always_ask`. |
| User interrupt | Limited | An untargeted interrupt durably cancels an active model, outcome grader, or tool Activity across API and worker processes. PostgreSQL defines finish-vs-interrupt ordering, closes model spans, emits one idle `end_turn`, and fences uncertain started tool steps as ambiguous. Targeted multi-agent interrupts are not supported. |
| Terminal execution errors | Supported | Permanent model request failures emit the documented `model_request_failed_error` or `billing_error` variant with `retry_status: terminal`, followed by `session.status_terminated`; non-provider runtime failures use `unknown_error`. MCP setup failures retain their more specific documented event variant. Older persisted histories may contain the legacy `api_error` spelling. |
| Model retry lifecycle | Supported | Retryable provider responses emit typed `model_overloaded_error`, `model_rate_limited_error`, or `model_request_failed_error` events. Each bounded retry publishes `retrying`, `session.status_rescheduled`, then `session.status_running`; exhaustion emits `exhausted`, returns idle with `retries_exhausted`, and flushes later queued messages. Provider `Retry-After` is honored up to the server cap. Infrastructure recovery remains private to Temporal. |
| MCP execution | Limited | Remote tools are discovered over Streamable HTTP, pinned per Session, permission-checked, journaled, and executed with large or binary results materialized in the Session sandbox. Ordered Vaults can inject static bearer or current OAuth access tokens; credentials are re-resolved per request, 401/403 uses the dedicated authentication event, and cross-origin authenticated redirects are rejected. Calls publish `agent.mcp_tool_use` with the required `mcp_server_name` and the bare server-side tool name, answered by `agent.mcp_tool_result` carrying `mcp_tool_use_id`; an `always_ask` MCP call still parks for a `user.tool_confirmation` referencing `tool_use_id`. OAuth refresh, private-network access, deprecated-SSE fallback, resources, and prompts are not supported. |
| Files | Limited | Upload, list, metadata, content download, and delete interoperate with the official Go SDK when an S3-compatible store is configured. PostgreSQL owns metadata and lifecycle intents; the object store owns bytes. Client uploads are limited to 500 MB, have no scope, and correctly return `downloadable: false`. File-backed Session Resources create downloadable scoped copies; arbitrary sandbox-output export, file-sourced messages, and file-backed outcome rubrics remain unsupported. This slice is single-tenant and currently requires one Files-enabled API process because startup reconciliation is not leased across replicas. |
| Skills | Limited | All nine custom Skill and immutable Skill Version resource operations interoperate with the official Go SDK when S3-compatible storage is configured. Upload validation enforces a single safe root, root `SKILL.md`, frontmatter metadata, directory/name matching, the 30 MB bound, and archive traversal/symlink rejection. Custom Agent and Session references are strict tagged unions; omitted/`latest` Versions resolve to immutable IDs, Session pins are committed relationally with the snapshot, and pinned Versions cannot be deleted until physical Session deletion. Docker-backed cloud Sessions expose bounded discovery metadata and a private Claude Code-style `Skill` dispatcher: selecting a Skill returns `Launching skill: <name>` and injects the complete main instruction file from a checksum-verified read-only bundle at `/workspace/skills/<name>/`. Materialization repairs damaged staging, survives worker reattachment, and is cleaned with the sandbox. Aggregate expanded content is capped at 500 MB and runtime names must be unique. Anthropic-managed, local, self-hosted, and current remote-provider execution fail explicitly; exact hosted post-compaction parity remains outside the current evidence. |
| Memory | Limited | All fourteen Memory Store, Memory, and immutable Memory Version operations interoperate with the official Go SDK under `agent-memory-2026-07-22`. PostgreSQL is canonical; mutations use SHA-256 preconditions, immutable audit Versions, actor attribution, archive read-only semantics, redaction, depth-one path projection, and bounded pagination. Up to eight Stores attach at Session creation. Docker materializes ordinary files beneath `/mnt/memory`, enforces read-only mounts, atomically writes multi-file tool changes back as `session_actor` Versions, rejects concurrent stale writes, and flushes a dirty mount before Session sandbox deletion. Local, self-hosted, and current remote-provider mounts remain unsupported. |
| Vaults | Limited | All six Vault lifecycle operations and six Credential lifecycle operations interoperate with the official Go SDK when an operator keyring is configured. Static bearer and MCP OAuth secrets are write-only and AES-256-GCM encrypted in PostgreSQL; authenticated encryption binds each payload to its Vault, Credential, and public auth configuration. Archive atomically purges encrypted payload columns and frees the active MCP URL key. Session `vault_ids`, deterministic normalized-URL resolution, and redirect-safe MCP bearer injection are implemented. OAuth validation/automatic refresh and environment-variable SecretEgress are not; environment-variable credentials return explicit `422`. |
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
when shaping this integration surface. The
[versioned core statement](compatibility/core-v1.md) separates a frozen claim
from this continuously updated matrix.

## How coverage is verified

Compatibility-related changes should use the smallest evidence appropriate to
the behavior:

- raw HTTP tests for JSON shapes, status codes, headers, and validation;
- official Go SDK tests for client interoperability;
- application and PostgreSQL tests for durable execution semantics;
- end-to-end tests for runtime, tool, interrupt, and streaming workflows.

Test names and edge-case details live beside the implementation and in the
architecture guides rather than in this user-facing coverage table.
