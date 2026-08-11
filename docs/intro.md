---
title: Mango
slug: /
sidebar_label: Overview
sidebar_position: 1
---

# Mango

Mango (`managed-agent-go`) is an independent, Apache-2.0-licensed agent runtime
written in Go. It persists server-owned sessions, delegates inference to a
Messages API endpoint, executes tools in replaceable sandboxes, and exposes an
HTTP surface compatible with the Claude Managed Agents API.

:::caution[Experimental project]

This is a pre-release implementation of a documented API subset — not an
Anthropic product or a drop-in replacement. Check
[Claude API coverage](compatibility.md) before depending on a capability, and do
not treat the default local sandbox as a security boundary.

:::

## What it does

- Server-owned **Agents, Environments, Sessions, and Events** over a `/v1` HTTP
  API, with cursor pagination and SSE streaming.
- A durable **model-and-tool loop**: multi-round inference, custom-tool and
  confirmation waits, single- and multi-Thread interrupts, and outcome
  evaluation.
- Tools run in **replaceable sandboxes** — local, Docker, and remote providers —
  with eight built-ins plus provider-native Web Search/Fetch and remote MCP
  tools.
- Opt-in **live previews** of assistant text, streamed while the authoritative
  event is still being produced.

## How it fits together

The default deployment separates API and worker roles around three backends:

- **PostgreSQL** is authoritative for resources, public events, projections,
  admission, and the tool journal.
- **Temporal** durably runs each Session Workflow and replay-safe model and tool
  Activities.
- **NATS Core** carries best-effort previews and event wakeups; missed wakeups
  are repaired from PostgreSQL sequence cursors.

The local Compose stack runs this complete architecture with a deterministic
offline model and needs no credentials. The runtime is the product; Claude API
compatibility is an integration surface, derived from official documentation and
tested through raw HTTP and the official Go SDK.

## Next steps

- **[Get started](getting-started.md)** — run the full stack and complete a
  first Session turn.
- **[Concepts](architecture.md)** — how the server owns history, and how
  sessions, events, and the runtime fit together.
- **[API reference](api/overview.md)** — implemented endpoints, request shapes,
  and transport conventions.
- **[Run a multi-agent Session](guides/multi-agent.md)** — configure a
  coordinator, delegate to persistent child Threads, and inspect their work.
- **[API compatibility](compatibility.md)** — exactly what is supported,
  limited, in preview, or not supported.
