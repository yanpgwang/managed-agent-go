---
title: Roadmap
slug: /roadmap
---

# Roadmap

The goal is a reliable, self-hosted managed agent runtime in Go. Claude Managed
Agents compatibility is the first client-facing integration surface, not a
requirement to reproduce every upstream feature one-for-one.

The roadmap is organized by outcome rather than by complete upstream API
parity. Detailed design constraints and completed mechanics live in the
[architecture guides](architecture.md).

## Next implementation slice: recoverable tool execution

The highest-value next step is to remove the ambiguity between a tool side
effect and the durable record of that side effect. Today authoritative runtime
output is buffered until run completion. A crash after `bash`, `write`, or
another tool changes the world but before completion commits can cause recovery
to repeat the run.

The next slice should establish durable attempt and tool-step boundaries:

- persist the model's tool request before invoking the tool;
- distinguish prepared, started, and completed tool execution;
- persist the tool result before continuing the model loop;
- never silently retry an execution whose external outcome is ambiguous;
- use idempotency keys where a downstream tool actually supports them.

This does not promise exactly-once execution for arbitrary shell commands.
Instead, it makes uncertainty explicit and gives later retry policy a durable
fact model. Additional sandbox backends do not take priority over this
correctness boundary.

## Now: make single-node execution trustworthy

- Deliver the recoverable tool-execution slice above, then define retryable
  versus terminal failures around durable attempts.
- Harden streaming around upstream errors, late subscribers, preview
  completion, and slow consumers.
- Add context compaction when session history exceeds the model projection
  limit.
- Add durable sandbox checkpoint/restore, cleanup of orphaned sandboxes, quotas,
  and eviction.

## Next: complete the practical integration surface

- Implement `web_fetch` and `web_search`.
- Resolve and execute MCP toolsets.
- Add token usage accounting and model-request spans.
- Support aggregate resolution when a run parks on several client actions.
- Support aborting a session parked on `requires_action`.
- Complete the request and response fields, filters, pagination behavior, and
  event variants needed by real integrations.
- Expand preview support to thinking and span lifecycle events.

## Later: broaden runtime capabilities

- Add files, executable skills, memory, vaults, scheduling, and webhooks when
  concrete use cases require them.
- Model multi-agent rosters, threads, delegation, and targeted interrupts as
  first-class domain concepts.
- Follow the ordered [sandbox backend evolution path](sandboxes.md), including
  Environment-backed selection, durable lifecycle management, and stronger or
  remote providers.
- Introduce a production database adapter and versioned schema migrations.
- Split API and worker roles with leases, fencing, an outbox, distributed event
  delivery, and observability.

## Current foundation

The repository already provides:

- server-owned multi-turn history projected into stateless model requests;
- versioned agents and immutable per-session snapshots;
- atomic event admission and one durable run per processable trigger;
- single-node restart recovery and causal reconstruction of prior run output;
- a multi-step model and built-in tool loop;
- session-scoped local and Docker sandboxes;
- custom-tool and `always_ask` confirmation park/resume flows;
- single-process active-run interruption;
- persisted events, cursor pagination, SSE, and opt-in message previews;
- official Go SDK coverage for the supported API subset.

The immediate release threshold is a dependable single-node runtime. Distributed
topology and wider product parity follow only when their use cases justify the
additional operational complexity.
