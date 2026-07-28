---
title: managed-agent-go
slug: /
sidebar_position: 1
---

# managed-agent-go

`managed-agent-go` is an independent, Apache-2.0-licensed, self-hosted managed
agent runtime written in Go. It owns conversation history and tool execution,
delegates inference to a Messages API endpoint, and exposes a Claude Managed
Agents-compatible HTTP surface for common workflows.

:::caution[Experimental project]

This is a pre-release implementation of a documented API subset, not an
Anthropic product or a drop-in replacement. Check
[Claude API coverage](compatibility.md) before depending on a capability, and
do not treat the default local sandbox as a security boundary.

:::

## Start here

- [Getting started](getting-started.md) runs the server and completes a first
  session turn.
- [Sandbox backends](sandboxes.md) shows what is available today, the security
  boundary of each backend, and the ordered path toward remote execution.
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

## Project direction

The runtime is the product; Claude API compatibility is an integration surface.
The project prioritizes reliable server-owned sessions, durable execution, safe
tool handoffs, and replaceable model and sandbox backends. It supports a useful
documented subset of the Managed Agents API so existing clients can integrate
with low friction.

It does not aim to reproduce every upstream field, product feature, edge case,
or internal execution detail one-for-one. Public behavior is derived from
official documentation and exercised through raw HTTP tests and black-box use
of the official Go SDK; internal code and topology are original.
