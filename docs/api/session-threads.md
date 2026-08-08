---
title: Session Threads
slug: /api/session-threads
---

# Session Threads

Every Session has a durable primary thread. The primary thread is not a copy of
the Session runtime: it is the same execution exposed through the official
Thread resource and Thread Event routes.

```text
GET  /v1/sessions/{session_id}/threads
GET  /v1/sessions/{session_id}/threads/{thread_id}
POST /v1/sessions/{session_id}/threads/{thread_id}/archive
GET  /v1/sessions/{session_id}/threads/{thread_id}/events
GET  /v1/sessions/{session_id}/threads/{thread_id}/stream
```

The primary identity is inserted in the same PostgreSQL transaction as the
Session, its immutable Skill and Vault pins, initial events, and orchestration
outbox. Existing databases receive deterministic primary identities during
migration. Deleting a Session cascades to its Thread identity.

## Projection model

The primary Thread reads its immutable Agent snapshot, status, cumulative
usage, and timing from the Session projection. `parent_thread_id` is null. The
Thread's Agent omits `multiagent`; the resolved coordinator roster remains on
`Session.agent`, matching the upstream response boundary.

Thread Event list and stream are views over the same durable Session event
ledger and NATS live channel. Pagination uses forward-only opaque cursors bound
to both Session and Thread IDs. Streaming supports the same opt-in
`event_deltas[]` values as the Session stream.

Archiving the primary Thread uses the Session's idle-only archive fence. A
running Thread returns `409`; after archive the Thread reports `terminated` and
its duration is frozen.

## Current multi-agent boundary

The five HTTP operations are implemented, but the runtime does not yet spawn
child threads. It also does not resolve coordinator rosters, delegate work,
cross-post child permission events, compact child context, or route targeted
interrupts. Those capabilities will extend the same Thread identity and event
ledger model rather than introduce a parallel runtime.

See the [Session Threads conformance matrix](session-threads-conformance.md).
