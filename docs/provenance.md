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
- Test and reference version: [v1.60.0](https://github.com/anthropics/anthropic-sdk-go/tree/v1.60.0),
  tag commit `b43f0d7a327fe3e30c302d773162669ab3bb4d26` (verified
  2026-07-24).
- Module checksum recorded in `go.sum`:
  `h1:cZzHheifXy7+pps7XsQ/6hfVKc34gwNiG8butUzik/4=`.
- Used only as a black-box compatibility client in tests (base URL pointed at an
  in-process `httptest` server through an explicit per-test client). No
  package-global SDK base-URL hook is used, and no SDK request/response types are
  copied into this repository. Successful decoding is evidence of
  interoperability, but it does not verify that every required response field
  is present.

## Official documentation and API Reference

Verified 2026-07-24.

- [API overview](https://platform.claude.com/docs/en/api/overview) — auth
  headers, pagination, request-size limit, and error envelope.
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
  events used for the handoff.
- [Permission policies](https://platform.claude.com/docs/en/managed-agents/permission-policies)
  — `always_allow` / `always_ask` / `always_deny` evaluation and the
  `requires_action` stop reason that a pending confirmation produces.
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

Verified 2026-07-26. These inform the opt-in shape and event correlation used
by stream-only previews.

- [Events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
  — persisted event streaming, event-delta opt-in, and reconnect guidance.
- [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
  — upstream Messages API SSE event types decoded by the real model client.

The inbound Messages API SSE wire and the outbound Managed Agents preview wire
are separate contracts. The implementation translates between them rather than
forwarding upstream frames.

## Notes on scope

- The current tool slice executes `bash`, `read`, `write`, `edit`, `glob`, and
  `grep`. `web_fetch` and `web_search` are declared to the model but not
  executed. MCP toolsets are parsed but not supported. The default local
  sandbox is a guardrail, not a security boundary. An opt-in Docker provider
  supplies container isolation, while gVisor/remote isolation remains planned.
- Opaque multiagent configuration persists with tested replace/null-clear
  behavior. Resolved rosters, reference validation, and multiagent
  execution/orchestration remain outside the implemented slice.
- Environments beyond the current placeholder records, self-hosted Environment
  Work, files, skills, memory, vaults, and scheduled deployments are out of the
  current batch. Their official pages are not re-listed until the corresponding
  wire is implemented and tested, to avoid claiming coverage that does not
  exist.
- Only official documentation and official SDKs are normative for the public
  compatibility surface.
