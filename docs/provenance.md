---
title: Compatibility provenance
slug: /provenance
---

# Compatibility provenance

Official, normative sources used to design and verify public-wire compatibility
are listed here. Non-official material, if deliberately consulted for independent
internal design, is secondary and cannot establish a compatibility claim.

## Official Go SDK (test dependency)

- [github.com/anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go)
- Test and reference version: [v1.61.0](https://github.com/anthropics/anthropic-sdk-go/tree/v1.61.0),
  tag commit `0303a8539676836e0cb351f3489fc2d347bbacde` (verified
  2026-08-01).
- Module checksum recorded in `go.sum`:
  `h1:JRTnm1tPqn5xo1xd1zfrcFDlcoWXVMvV1K68YmhpZKw=`.
- Used only as a black-box compatibility client in tests (base URL pointed at an
  in-process `httptest` server through an explicit per-test client). No
  package-global SDK base-URL hook is used, and no SDK request/response types are
  copied into this repository. Successful decoding is evidence of
  interoperability, but it does not verify that every required response field
  is present.

## Official documentation and API Reference

Verified 2026-07-29.

- [API overview](https://platform.claude.com/docs/en/api/overview) — auth
  headers, response headers, pagination, request-size limit, and error
  envelope. See
  [Authentication and error-status sources](#authentication-and-error-status-sources)
  for the header-level detail.
- [Claude API errors](https://platform.claude.com/docs/en/api/errors) — status
  and error types, request IDs, transient retry classes, and `retry-after`. See
  [Authentication and error-status sources](#authentication-and-error-status-sources)
  for the full status table and how Mango maps onto it.
- [Create Agent](https://platform.claude.com/docs/en/api/beta/agents/create) —
  model configuration, initial `version`, and response shape.
- [Update Agent](https://platform.claude.com/docs/en/api/beta/agents/update) —
  optimistic concurrency and update-field semantics.
- [Start a session](https://platform.claude.com/docs/en/managed-agents/sessions)
  — agent reference forms, initial events, and resolved snapshot.
- [Create Session API Reference](https://platform.claude.com/docs/en/api/beta/sessions/create)
  — full session response and resolved snapshot.
- [Session operations](https://platform.claude.com/docs/en/managed-agents/session-operations)
  — statuses, pagination, archive, and delete behavior.
- [Events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
  — event unions, content blocks, processing, and reconnect behavior.
- [Events API Reference](https://platform.claude.com/docs/en/api/beta/sessions/events)
  — `requires_action.event_ids`, the all-actions resolution boundary, and
  per-event `processed_at` semantics used by the durable pending-action gate.
- [List Events API Reference](https://platform.claude.com/docs/en/api/beta/sessions/events/list)
  — filters, pagination envelope, and per-event shapes.
- [Multiagent orchestration](https://platform.claude.com/docs/en/managed-agents/multiagent-orchestration)
  — multiagent input shape and resolved-roster behavior.
- [Managed Agents reference](https://platform.claude.com/docs/en/managed-agents/reference)
  — the published per-organization rate limits (300 requests per minute for
  create endpoints, 1,200 for read endpoints). Verified 2026-08-03. The
  documentation states these as Anthropic organization policy. Mango implements
  no inbound rate limiting, so it never returns `429` `rate_limit_error` and has
  no occasion to emit `retry-after`.

## Authentication and error-status sources

Verified 2026-08-03. The Managed Agents API is part of the Claude API and
inherits these general pages; they are normative for Mango's credential headers
and error status codes.

- [API overview — Authentication](https://platform.claude.com/docs/en/api/overview#authentication)
  — the request-header table lists **both** `x-api-key` and `Authorization`,
  each marked "One of `x-api-key` or `Authorization`". `Authorization` carries
  `Bearer <token>`, where the token is short-lived and obtained from
  `POST /v1/oauth/token` through Workload Identity Federation. Both header
  shapes are therefore **documented contract** and Mango accepts both by
  default.

  Mango implements neither `POST /v1/oauth/token` nor Workload Identity
  Federation. It reproduces only the header shape, validating the presented
  bearer value against the same configured key set as `x-api-key`. That
  narrowing is a **local choice**, recorded in
  [API overview](api/overview.md#authentication).

  The same page's "Response headers" section documents `request-id` and
  `anthropic-organization-id` on every response. Mango emits `request-id`; it
  does not emit `anthropic-organization-id`, because it is single-tenant and
  has no organization to name. Recorded in
  [compatibility](compatibility.md).

- [Claude API errors](https://platform.claude.com/docs/en/api/errors)
  — binds status codes to error types explicitly: `400 invalid_request_error`,
  `401 authentication_error`, `402 billing_error`, `403 permission_error`,
  `404 not_found_error`, `409 conflict_error`, `413 request_too_large`,
  `429 rate_limit_error`, `500 api_error`, `504 timeout_error`, and
  `529 overloaded_error`. Mango's `401 authentication_error` for a missing or
  invalid credential is therefore **documented contract**, not a local choice,
  as are its 400/404/409/413/500 pairings.

  The page also states that the official SDKs retry transient failures
  "honoring the `retry-after` header when present", so `retry-after` is a
  documented header rather than an undocumented one.

  `422` is the only status Mango returns that the table does not list; it marks
  a documented capability Mango has not implemented. The page does say
  `invalid_request_error` "may also be used for other 4XX status codes not
  listed in this section", so the *type* remains documented while the *status*
  is a **local choice**. Recorded in
  [API overview](api/overview.md#errors).

The official Go SDK corroborates the two-header contract. `anthropic.NewClient`
reads `ANTHROPIC_AUTH_TOKEN` from the environment into an `Authorization:
Bearer` header, and an explicit `option.WithAPIKey` sets `X-Api-Key` alongside
it, so a single request routinely carries both headers with different values.
Mango therefore tries each presented credential rather than rejecting the
combination — a **design inference** covered by
`TestAuthMiddleware_BothHeadersAreTried`.

## Tool and sandbox sources

Verified 2026-07-28. These inform the tool-use loop, the built-in toolset
declaration, the custom/permission handoff, and the tool-block event wire.

- [Managed Agents tools](https://platform.claude.com/docs/en/managed-agents/tools)
  — the `agent_toolset_20260401` built-in toolset, per-tool `enabled` and
  `permission_policy` config, custom tools, and MCP toolsets.
- [List Events](https://platform.claude.com/docs/en/api/beta/sessions/events/list)
  and
  [Send Events](https://platform.claude.com/docs/en/api/beta/sessions/events/send)
  — the `agent.tool_use` / `agent.tool_result` / `agent.custom_tool_use` event
  variants and the `user.custom_tool_result` / `user.tool_confirmation` client
  events used for the handoff.
- [Permission policies](https://platform.claude.com/docs/en/managed-agents/permission-policies)
  — `always_allow` / `always_ask` / `always_deny` evaluation and the
  `requires_action` stop reason that a pending confirmation produces.
- [MCP connector](https://platform.claude.com/docs/en/managed-agents/mcp-connector)
  — server/toolset matching, default permission policy, oversized-result
  handling, and recoverable connection failures. Verified 2026-08-01.
- [Web Search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)
  and
  [Web Fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool)
  — provider-native server-tool declarations, result blocks, citations, and
  continuation behavior. Verified 2026-08-01.
- [Cloud environment setup](https://platform.claude.com/docs/en/managed-agents/environments)
  — reusable Environment configuration and isolated session sandbox ownership.
- [Self-hosted sandboxes](https://platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes)
  — the control-plane/worker boundary and session-scoped sandbox guidance.
- [Bash tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/bash-tool)
  and
  [Text editor tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/text-editor-tool)
  — consulted for public parameter conventions that shaped the *internal*
  input schemas we hand the model. These are Tool-Use (Messages API)
  conventions, not part of the Managed Agents public wire; the schemas we send
  are an internal design choice and are marked `partial`/internal in
  `docs/compatibility.md`.

The
[Anthropic Sandbox Runtime](https://github.com/anthropic-experimental/sandbox-runtime)
repository is referenced only as an experimental candidate backend. It does
not establish Managed Agents API compatibility and no SRT integration is
currently implemented.

Beta header for all Managed Agents routes covered here:
`anthropic-beta: managed-agents-2026-04-01`.

## Streaming sources

Verified 2026-08-03. These inform the opt-in shape and event correlation used
by stream-only previews.

- [Events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
  — persisted event streaming, event-delta opt-in, and reconnect guidance.
- [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
  — upstream Messages API SSE event types decoded by the real model client.

The inbound Messages API SSE wire and the outbound Managed Agents preview wire
are separate contracts. The implementation translates between them rather than
forwarding upstream frames.

## Compatibility claim boundary

- The sandbox-executed tool set is `bash`, `read`, `write`, `edit`, `glob`, and
  `grep`. `web_fetch` and `web_search` use the configured Messages API endpoint
  as provider-native server tools and currently require `always_allow`.
- MCP tool discovery and execution support unauthenticated public Streamable
  HTTP servers with Session-pinned definitions, permission checks, durable
  journaling, and sandbox materialization for large or binary results. Vault
  authentication, private-network connectivity, deprecated-SSE fallback,
  resources, and prompts are not supported.
- The default local sandbox is a guardrail, not a security boundary; the
  optional Docker provider supplies container isolation.
- Opaque multiagent configuration persists with tested replace/null-clear
  behavior. Resolved rosters, reference validation, and multiagent
  execution/orchestration are not implemented.
- Unsupported product surfaces are excluded from compatibility claims until
  their wire behavior is implemented and tested.
- Only official documentation and official SDKs are normative for the public
  compatibility surface.
