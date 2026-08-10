---
title: Session Threads conformance matrix
slug: /api/session-threads-conformance
---

# Session Threads conformance matrix

Baseline: `managed-agents-2026-04-01`, Anthropic Go SDK `v1.62.0`.

| Operation | Route | HTTP | Official Go SDK | Durable semantics | Known limitation |
| --- | --- | --- | --- | --- | --- |
| Get Thread | `GET /v1/sessions/{session_id}/threads/{thread_id}` | Yes | Yes | Independent PostgreSQL Thread execution projection | Advisor Thread snapshots are not implemented. |
| List Threads | `GET /v1/sessions/{session_id}/threads` | Yes | Yes | Primary-first forward keyset page of independent projections | Advisor Thread snapshots are not implemented. |
| Archive Thread | `POST /v1/sessions/{session_id}/threads/{thread_id}/archive` | Yes | Yes | Reuses the idle-only Session archive fence | Archiving the primary archives the single-thread Session. |
| List Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/events` | Yes | Yes | Forward page over the selected Thread's authoritative ledger | Child actions remain canonical in the child ledger; the primary stream receives separately identified client-visible projections. |
| Stream Thread Events | `GET /v1/sessions/{session_id}/threads/{thread_id}/stream` | Yes | Yes | Thread-scoped NATS previews plus PostgreSQL repair | The stream intentionally does not replay history. |

The test suite exercises all five methods through the official SDK, response
field decoding, Session/Thread ownership checks, migration-backed identity and
projection backfill, transactional primary projection updates, immutable
Session-roster capture during atomic child creation, multiple child instances
of one callable Agent, nested-Thread rejection, per-Thread event ownership and
migration backfill, private coordinator tools, replay-safe child creation and
follow-up delivery, independent Workflow/outbox identity, provider context,
status/usage/retry aggregation, asynchronous reports, child event/preview
isolation, cursor scoping, SSE decoding, archive state, cascade deletion,
Thread-owned action barriers, client-visible cross-post IDs, redundant routing
hint validation, partial-barrier waiting, automatic child response routing, and
idempotent child resume. Event tests additionally cover global and targeted
interrupt routing, independent Workflow wakeups, finish-vs-interrupt ordering,
idle no-ops, and queued follow-up boundaries.

Not yet claimed:

- child Workflow shutdown on archive/delete;
- immutable child context snapshots and explicit context-compaction events;
- advisor consultation Threads and Session-wide list-cost budget enforcement.
