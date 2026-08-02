---
title: managed-agent-go
slug: /
sidebar_position: 1
---

# managed-agent-go

`managed-agent-go` is an independent, Apache-2.0-licensed agent runtime written
in Go. It persists server-owned sessions, delegates inference to a Messages API
endpoint, executes tools in replaceable sandboxes, and exposes a Claude Managed
Agents-compatible HTTP surface.

:::caution[Experimental project]

This is a pre-release implementation of a documented API subset, not an
Anthropic product or a drop-in replacement. Check
[Claude API coverage](compatibility.md) before depending on a capability, and
do not treat the default local sandbox as a security boundary.

:::

## Start here

- [Getting started](getting-started.md) starts the complete Docker stack and
  completes a first Session turn.
- [Claude API coverage](compatibility.md) is the exact supported/unsupported
  behavior matrix.
- [Architecture overview](architecture.md) explains why PostgreSQL owns public
  state, Temporal owns in-flight execution, and NATS carries only ephemeral
  delivery.
- [Sandbox backends](sandboxes.md) shows what is available today, the security
  boundary of each backend, and the ordered path toward remote execution.
- [Domain model](architecture/domain-model.md) describes agents, environments,
  Sessions, events, and Workflow turns.
- [API overview](api/overview.md) lists the implemented endpoints and transport
  conventions.
- [Roadmap](roadmap.md) tracks the remaining compatibility and production
  hardening work on the durable multi-process architecture.

## Current architecture

The default deployment has separate API and worker roles:

- PostgreSQL is authoritative for resources, public events, projections,
  admission, and the tool journal.
- Temporal durably runs one Session Workflow and replay-safe model/tool
  Activities.
- NATS Core carries best-effort previews and persisted-event wakeups; streams
  repair missed wakeups from PostgreSQL sequence cursors.

The local Compose stack runs the complete architecture with a deterministic
offline model and needs no credentials.

## Current capability boundary

The primary path supports Agent, Environment, Session, and Event resources;
messages and untargeted interrupts; cursor pagination and SSE; a multi-round
model loop; durable custom-tool, confirmation, and self-hosted tool-result
waits; outcome evaluation; eight built-ins; provider-native Web Search/Fetch;
unauthenticated remote MCP tools; token-aware provider context; local and Docker
sandboxes; and opt-in assistant text previews.

The remaining M1 closure work is repeatable hosted black-box conformance,
outcome evaluation heartbeat events, Files-backed rubrics, and thinking
previews. Deployment-managed MCP authentication, skills/memory/vaults,
multi-agent orchestration, schedules, and webhooks remain future work.

The runtime is the product; Claude API compatibility is an integration surface.
Public behavior is derived from official documentation and tested through raw
HTTP plus the official Go SDK, while the internal implementation is original.
