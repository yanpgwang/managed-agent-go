---
title: Session Threads
slug: /api/session-threads
---

# Session Threads

Every Session has a durable primary Thread. In the current single-Agent runtime,
the primary Thread and Session describe the same execution. They are persisted
as separate projections so future child Threads can own execution state while
the Session remains the aggregate resource.

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

The current runtime updates the primary Thread projection transactionally with
execution changes to the Session aggregate. Session-only title, metadata, and
resource changes do not mutate the Thread. When child execution is added, each
child will update its own projection before the Session aggregate is
recomputed; Thread reads will not need a new storage model.

Thread Event list and stream are views over the same durable Session event
ledger and NATS live channel. Pagination uses forward-only opaque cursors bound
to both Session and Thread IDs. Streaming supports the same opt-in
`event_deltas[]` values as the Session stream.

Archiving the primary Thread uses the Session's idle-only archive fence. A
running Thread returns `409`; after archive the Thread reports `terminated` and
its duration is frozen.

## Current multi-agent boundary

The five HTTP operations and coordinator roster resolution are implemented,
but the runtime does not yet spawn or execute child Threads. Child archival and
event routes explicitly return `422` until independent child execution and
event visibility exist; they never fall through to the primary Session ledger.
Delegation, cross-thread messages, cross-posted child events, child context
compaction, and targeted interrupts remain future slices of the same runtime.
The v1.62 `advisor` Thread is not represented as an ordinary child placeholder;
it remains unsupported until its reserved, automatically terminating
consultation lifecycle can be implemented end to end. Session budgets are also
deferred until list cost is aggregated across all independently running Threads.

See the [Session Threads conformance matrix](session-threads-conformance.md).
