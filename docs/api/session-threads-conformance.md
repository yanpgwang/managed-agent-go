---
title: Session Threads conformance matrix
slug: /api/session-threads-conformance
---

# Session Threads conformance matrix

Baseline: `managed-agents-2026-04-01`, Anthropic Go SDK `v1.61.0`.

| Operation | Route | HTTP | Official Go SDK | Durable semantics | Known limitation |
| --- | --- | --- | --- | --- | --- |
| Get Thread | `GET /v1/sessions/{session_id}/threads/{thread_id}` | Yes | Yes | PostgreSQL identity plus live Session projection | Only the primary thread exists. |
| List Threads | `GET /v1/sessions/{session_id}/threads` | Yes | Yes | Primary-first forward keyset page | No child threads are produced yet. |
| Archive Thread | `POST /v1/sessions/{session_id}/threads/{thread_id}/archive` | Yes | Yes | Reuses the idle-only Session archive fence | Archiving the primary archives the single-thread Session. |
| List Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/events` | Yes | Yes | Forward page over the authoritative Session ledger | Child-thread filtering awaits child runtime state. |
| Stream Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/stream` | Yes | Yes | NATS wakeups/previews with PostgreSQL repair | Only primary-thread activity exists. |

The test suite exercises all five methods through the official SDK, response
field decoding, Session/Thread ownership checks, migration-backed identity,
cursor scoping, SSE decoding, archive state, and cascade deletion.

Not yet claimed:

- resolved coordinator rosters and one-level reference validation;
- child-thread creation, persistence, concurrency, and independent context;
- `agent.thread_message_*` and `session.thread_*` lifecycle events;
- cross-posted tool/custom events and targeted interrupts;
- child-thread context compaction and per-thread provider transcripts.
