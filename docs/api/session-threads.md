---
title: Session Threads
slug: /api/session-threads
---

# Session Threads

Every Session has a durable primary Thread. A coordinator can start persistent
child Threads through its private model tools. The Session remains the
aggregate resource while every Thread owns its execution and conversation.

```text
GET  /v1/sessions/{session_id}/threads
GET  /v1/sessions/{session_id}/threads/{thread_id}
POST /v1/sessions/{session_id}/threads/{thread_id}/archive
GET  /v1/sessions/{session_id}/threads/{thread_id}/events
GET  /v1/sessions/{session_id}/threads/{thread_id}/stream
```

The primary identity and its initial execution projection are inserted in the
same PostgreSQL transaction as the Session, its immutable Skill and Vault pins,
initial events, and orchestration outbox. Existing databases receive
deterministic primary identities and backfilled projections during migration.
Deleting a Session cascades to its Threads.

## Projection model

Each Thread owns an independent PostgreSQL projection of its immutable Agent
snapshot, status, cumulative usage, and timing. The primary Thread has a null
`parent_thread_id`. Its Agent omits `multiagent`; the resolved coordinator
roster remains on `Session.agent`, matching the upstream response boundary.

The runtime updates an owning Thread before recomputing the Session aggregate in
the same PostgreSQL transaction. The Session remains running while any Thread
is running; usage is the sum of independently accumulated Thread usage.
Session-only title, metadata, and resource changes do not mutate Thread state.

Thread Event list and stream select the chosen Thread from the Session-wide
ordered event ledger. Pagination uses forward-only opaque cursors bound to both
Session and Thread IDs. Streaming supports the same opt-in `event_deltas[]`
values as the Session stream, but child previews use Thread-scoped NATS subjects
and never leak into the primary stream.

Archiving the primary Thread uses the Session's idle-only archive fence. A
running Thread returns `409`; after archive the Thread reports `terminated` and
its duration is frozen.

## Coordinator execution

A coordinator receives `list_agents` and `send_to_agent` as private model tools;
they do not emit generic `agent.tool_use`/`agent.tool_result` events. A new send
atomically captures the resolved roster Agent, creates the child projection,
appends directional message and lifecycle events, and writes the child outbox.
The relay starts a stable per-Thread Temporal Workflow. Follow-up sends reuse
that Thread and its provider-native transcript. A completed child answer is
reported asynchronously to the primary Thread, which is then scheduled for a
separate synthesis turn.

The remaining multi-agent boundary is cross-posted confirmation/custom-tool
requests with automatic response routing, global and targeted interrupts,
Workflow shutdown on archive or Session deletion, and immutable child context
snapshots. The v1.62 `advisor` Thread is not represented as an ordinary child
placeholder; it remains unsupported until its reserved, automatically
terminating consultation lifecycle can be implemented end to end. Session
budgets are also deferred until list cost is aggregated across all independently
running Threads.

See the [Session Threads conformance matrix](session-threads-conformance.md).
