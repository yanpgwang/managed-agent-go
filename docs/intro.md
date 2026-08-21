---
title: Mango
slug: /
sidebar_label: Overview
sidebar_position: 1
---

# Mango

Mango is an independent, Apache-2.0-licensed agent runtime
written in Go. It persists server-owned sessions, delegates inference to a
configured model endpoint, executes tools in replaceable sandboxes, and exposes
its own HTTP API for durable agent work.

:::caution[Experimental project]

This is a pre-release project with no customers and no supported stable API.
`/v1` is the single development API namespace. Routes, fields, schemas, and
behavior may change there directly; Mango does not
preserve earlier development snapshots through `/v2` or compatibility layers,
and does not promise drop-in use with a hosted agent service or third-party
SDK. Check [capabilities and limits](capabilities.md) before depending on a
workflow, and do not treat the default local sandbox as a security boundary.

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
offline model and needs no credentials. The runtime and its native API are the
product. External contracts may inform design research, but Mango's own
documentation, OpenAPI definition, implementation, and tests define current
behavior.

## Next steps

- **[Get started](getting-started.md)** — run the full stack and complete a
  first Session turn.
- **[Product direction](product.md)** — what Mango optimizes for and how work is
  selected.
- **[Concepts](architecture.md)** — how the server owns history, and how
  sessions, events, and the runtime fit together.
- **[API reference](api/overview.md)** — implemented endpoints, request shapes,
  and transport conventions.
- **[Run a multi-agent Session](guides/multi-agent.md)** — configure a
  coordinator, delegate to persistent child Threads, and inspect their work.
- **[Capabilities and limits](capabilities.md)** — what is supported, limited,
  or still in preview.
