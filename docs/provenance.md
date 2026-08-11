---
title: Upstream provenance
slug: /maintainers/provenance
---

# Upstream provenance

This maintainer page records the normative public sources used to shape
Mango's compatibility surface. It is deliberately not an implementation
history. Git commits and release notes record when Mango behavior changed.

Only official Anthropic documentation and SDKs establish a public
compatibility claim. Other agent runtimes may inform independent internal
design, but they are not normative for the HTTP or event contract.

## SDK evidence

- Client: [github.com/anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go)
- Pinned test baseline: [v1.62.0](https://github.com/anthropics/anthropic-sdk-go/tree/v1.62.0)
- Tag commit: `0239529cb2a929c9e52b68f36fa7e413392c00fd`
- Module checksum: `h1:nKkyMPJnFF7PfrWlKw77mCY5ZiEswPPq8nK4sz9is78=`
- Last contract review: 2026-08-10

Tests point an explicit SDK client at an in-process Mango server. Successful
SDK decoding proves client interoperability for that test; it does not prove
that every optional upstream field or undocumented hosted behavior is present.

## Beta contracts

| Surface | Header |
| --- | --- |
| Managed Agents, Deployments, Vaults, Environment Work, Session Threads | `anthropic-beta: managed-agents-2026-04-01` |
| Files | `anthropic-beta: files-api-2025-04-14` |
| Skills | `anthropic-beta: skills-2025-10-02` |
| Memory resource routes | `anthropic-beta: agent-memory-2026-07-22` |

Memory Store attachment remains part of Session creation under the Managed
Agents beta. Strict mode rejects a Memory resource request that combines the
Memory and Managed Agents beta values.

## Resource API sources

- [API overview](https://platform.claude.com/docs/en/api/overview) and
  [errors](https://platform.claude.com/docs/en/api/errors)
- [Agents](https://platform.claude.com/docs/en/api/beta/agents)
- [Environments](https://platform.claude.com/docs/en/api/beta/environments)
- [Sessions](https://platform.claude.com/docs/en/api/beta/sessions) and
  [Session operations](https://platform.claude.com/docs/en/managed-agents/session-operations)
- [Session Events](https://platform.claude.com/docs/en/api/beta/sessions/events) and
  [events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
- [Session Threads](https://platform.claude.com/docs/en/api/beta/sessions/threads) and
  [multi-agent orchestration](https://platform.claude.com/docs/en/managed-agents/multiagent-orchestration)
- [Session budgets](https://platform.claude.com/docs/en/managed-agents/session-budgets)
- [Files](https://platform.claude.com/docs/en/api/beta/files) and
  [Managed Agents Files](https://platform.claude.com/docs/en/managed-agents/files)
- [Session Resources](https://platform.claude.com/docs/en/api/beta/sessions/resources)
- [Skills](https://platform.claude.com/docs/en/api/beta/skills) and
  [Managed Agents Skills](https://platform.claude.com/docs/en/managed-agents/skills)
- [Memory Stores](https://platform.claude.com/docs/en/api/beta/memory-stores) and
  [Managed Agents Memory](https://platform.claude.com/docs/en/managed-agents/memory)
- [Vaults](https://platform.claude.com/docs/en/api/beta/vaults) and
  [Credentials](https://platform.claude.com/docs/en/api/go/beta/vaults/credentials)
- [Deployments](https://platform.claude.com/docs/en/api/beta/deployments),
  [Deployment Runs](https://platform.claude.com/docs/en/api/beta/deployment-runs), and
  [scheduled deployments](https://platform.claude.com/docs/en/managed-agents/scheduled-deployments)
- [Self-hosted sandboxes](https://platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes)

## Runtime behavior sources

- [Managed Agents tools](https://platform.claude.com/docs/en/managed-agents/tools)
- [Permission policies](https://platform.claude.com/docs/en/managed-agents/permission-policies)
- [MCP connector](https://platform.claude.com/docs/en/managed-agents/mcp-connector)
- [Cloud Environments](https://platform.claude.com/docs/en/managed-agents/environments)
- [Web Search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)
  and [Web Fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool)
- [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [Claude Code Skill content lifecycle](https://code.claude.com/docs/en/slash-commands#skill-content-lifecycle)

The Claude Code Skill source informs Mango's private on-demand instruction
lifecycle. It does not establish the Managed Agents resource wire. Likewise,
Messages SSE frames and Managed Agents preview frames are separate contracts;
Mango translates between them rather than forwarding provider frames directly.

## Verification policy

When an upstream contract changes:

1. record the new official SDK tag and documentation sources here;
2. update the OpenAPI and operation inventory;
3. add raw-wire and official-SDK tests for changed request/response behavior;
4. add PostgreSQL/Temporal evidence when the change affects durable semantics;
5. update [API compatibility](compatibility.md) with any user-visible limit;
6. preserve old Temporal histories and stored resource shapes through explicit
   compatibility code or a documented migration.

The detailed evidence map is [Conformance evidence](conformance.md).
