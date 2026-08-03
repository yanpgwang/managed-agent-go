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
  headers, pagination, request-size limit, and error envelope.
- [Claude API errors](https://platform.claude.com/docs/en/api/errors) — Messages
  API status/error types, request IDs, transient retry classes, and
  `Retry-After`.
- [Create Agent](https://platform.claude.com/docs/en/api/beta/agents/create) —
  model configuration, initial `version`, and response shape.
- [Update Agent](https://platform.claude.com/docs/en/api/beta/agents/update) —
  optimistic concurrency and update-field semantics.
- [List Agents](https://platform.claude.com/docs/en/api/beta/agents/list) —
  `created_at[gte]`, `created_at[lte]`, `include_archived`, `limit`, and `page`;
  the documented Agent limit default of 20 and maximum of 100. Verified
  2026-08-03 against the API reference and official Go SDK v1.61.0 types.
- [List Agent Versions](https://platform.claude.com/docs/en/api/beta/agents/versions/list) —
  `limit`, `page`, the nullable `next_page` response cursor, and the documented
  default of 20 and maximum of 100. Verified 2026-08-03 against the API
  reference and official Go SDK v1.61.0 types.
- [List Environments](https://platform.claude.com/docs/en/api/beta/environments/list) —
  `include_archived`, `limit`, and `page`, with a forward cursor response and no
  documented created-at filters or limit bounds. Verified 2026-08-03 against
  the API reference and official Go SDK v1.61.0 types.
- [Start a session](https://platform.claude.com/docs/en/managed-agents/sessions)
  — agent reference forms, initial events, and resolved snapshot.
- [Create Session API Reference](https://platform.claude.com/docs/en/api/beta/sessions/create)
  — full session response and resolved snapshot.
- [Session operations](https://platform.claude.com/docs/en/managed-agents/session-operations)
  — statuses, pagination, archive, and delete behavior, plus the mid-session
  agent configuration update: `tools`/`mcp_servers` only, full replacement,
  session-local, and only while the session is `idle`.
- [Update Session API Reference](https://platform.claude.com/docs/en/api/beta/sessions/update)
  — the four body fields (`agent`, `metadata`, `title`, `vault_ids`), the
  per-key metadata patch, and the documented rejection of `vault_ids`.
- [Events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
  — event unions, content blocks, processing, and reconnect behavior.
- [Events API Reference](https://platform.claude.com/docs/en/api/beta/sessions/events)
  — `requires_action.event_ids`, the all-actions resolution boundary, and
  per-event `processed_at` semantics used by the durable pending-action gate.
- [List Events API Reference](https://platform.claude.com/docs/en/api/beta/sessions/events/list)
  — filters, pagination envelope, and per-event shapes.
- [Multiagent orchestration](https://platform.claude.com/docs/en/managed-agents/multiagent-orchestration)
  — multiagent input shape and resolved-roster behavior.

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
  events used for the handoff. The same references define the distinct
  `agent.mcp_tool_use` (required `mcp_server_name`, bare tool `name`, optional
  `evaluated_permission`) and `agent.mcp_tool_result` (`mcp_tool_use_id`, no
  server name) variants, and state that `user.tool_confirmation.tool_use_id`
  answers either tool-use variant. Verified 2026-08-03.
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
