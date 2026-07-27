---
title: managed-agent-go
slug: /
sidebar_position: 1
---

# managed-agent-go

`managed-agent-go` is an independent, Apache-2.0-licensed Go implementation of
the Claude Managed Agents HTTP API. It combines a compatible control plane with
a self-hosted agent runtime that owns conversation history and delegates only
inference to a Messages API endpoint.

:::caution[Experimental project]

This is a pre-release implementation of a documented API subset, not an
Anthropic product. Check the [compatibility ledger](compatibility.md) before
depending on a capability, and do not treat the default local sandbox as a
security boundary.

:::

## Start here

- [Getting started](getting-started.md) runs the server and completes a first
  session turn.
- [Architecture overview](architecture.md) explains boundaries, data flow, and
  current architectural debt.
- [Domain model](architecture/domain-model.md) describes agents, environments,
  sessions, events, and internal runs.
- [API overview](api/overview.md) lists the implemented endpoints and transport
  conventions.
- [Roadmap](roadmap.md) shows how the project moves from a strong single-node
  alpha toward durable multi-worker operation.

## What works today

The current slice supports versioned agents, environments, sessions, persisted
event history, cursor pagination, SSE, restartable single-node runs, a
multi-turn model/tool loop, six executing built-in tools (`bash`, `read`,
`write`, `edit`, `glob`, `grep`; `web_fetch`/`web_search` are declared but not
implemented), custom-tool handoff, local and Docker sandboxes, and opt-in live
previews of assistant text.

The default runtime uses a deterministic offline model. A real
Anthropic-shaped Messages API endpoint is enabled through environment variables.

## Compatibility policy

This project is a clean-room implementation. Public behavior is derived from
official documentation and validated through raw HTTP golden tests and
black-box use of the official Go SDK. Internal code and topology are original.

Capabilities are labeled `exact`, `partial`, or `unsupported`. A successful SDK
decode alone does not qualify as exact compatibility; complete field semantics
and relevant edge cases must also be tested.
