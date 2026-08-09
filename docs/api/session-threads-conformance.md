---
title: Session Threads conformance matrix
slug: /api/session-threads-conformance
---

# Session Threads conformance matrix

Baseline: `managed-agents-2026-04-01`, Anthropic Go SDK `v1.62.0`.

| Operation | Route | HTTP | Official Go SDK | Durable semantics | Known limitation |
| --- | --- | --- | --- | --- | --- |
| Get Thread | `GET /v1/sessions/{session_id}/threads/{thread_id}` | Yes | Yes | Independent PostgreSQL Thread execution projection | Only the primary Thread is produced by the runtime. |
| List Threads | `GET /v1/sessions/{session_id}/threads` | Yes | Yes | Primary-first forward keyset page of independent projections | No child Threads are produced yet. |
| Archive Thread | `POST /v1/sessions/{session_id}/threads/{thread_id}/archive` | Yes | Yes | Reuses the idle-only Session archive fence | Archiving the primary archives the single-thread Session. |
| List Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/events` | Yes | Yes | Forward page over the authoritative primary Session ledger | Child event history returns `422` until event visibility is implemented. |
| Stream Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/stream` | Yes | Yes | Primary NATS wakeups/previews with PostgreSQL repair | Child event streaming returns `422` rather than exposing primary activity. |

The test suite exercises all five methods through the official SDK, response
field decoding, Session/Thread ownership checks, migration-backed identity and
projection backfill, transactional primary projection updates, independent
child projection reads, cursor scoping, SSE decoding, archive state, and
cascade deletion.

Not yet claimed:

- child-Thread creation, execution, concurrency, and independent context;
- `agent.thread_message_*` and `session.thread_*` lifecycle events;
- cross-posted tool/custom events and targeted interrupts;
- child-Thread context compaction and per-Thread provider transcripts.
- advisor consultation Threads and Session-wide list-cost budget enforcement.
