---
title: Platform spine milestone
slug: /architecture/platform-spine-milestone
---

# Platform spine milestone

Status: **Implemented (first vertical slice)**
Date: **2026-07-28**

This document describes exactly what the first bounded Temporal platform-spine
slice implements, how to run it, and its explicit limitations. It is the
concrete counterpart to the [target-platform decision](target-platform.md) and
the [orchestration fit review](orchestration-fit.md): those fix the architecture;
this records the first working increment of it.

The milestone deliberately does **not** cut over all traffic. The SQLite
dispatcher (`internal/store`, `internal/app`) remains the default and is
unchanged. The PostgreSQL/Temporal path is additive and feature-gated.

## What is implemented

### Local development stack

`deployments/local/` brings up PostgreSQL, Temporal (with UI), and NATS Core
with pinned versions and health checks. See
[the stack README](https://github.com/yanpgwang/managed-agent-go/tree/main/deployments/local).

```sh
make -C deployments/local up
make -C deployments/local health
```

| Service    | Image                          | Purpose                                    |
| ---------- | ------------------------------ | ------------------------------------------ |
| PostgreSQL | `postgres:17.5-alpine`         | Event ledger, projections, admission outbox |
| Temporal   | `temporalio/auto-setup:1.29.7` | Durable session orchestration              |
| Temporal UI| `temporalio/ui:2.52.1`         | Workflow explorer (`localhost:8233`)       |
| NATS Core  | `nats:2.11-alpine`             | Previews / SSE wakeups (later slice)       |

### PostgreSQL plumbing

`internal/pg` adds a `pgx` connection pool, embedded `goose` migrations
(`internal/pg/migrations`), and `sqlc`-generated typed queries
(`internal/pg/pgstore`, regenerated with `go generate ./internal/pg`). The
schema is intentionally minimal: a `sessions` projection, an append-only
`events` ledger with a durable per-session receipt sequence, and a coalescible
`orchestration_outbox`.

SQLite remains the default compatibility store; nothing here migrates or
replaces it.

### Event-admission transaction

`pg.Store.AdmitEvents` is the PostgreSQL admission boundary. In one transaction
it:

1. locks the session row (`SELECT ... FOR UPDATE`);
2. validates the batch is client-submittable and the session is not terminated;
3. assigns durable per-session receipt sequences;
4. appends the public events and, on a client trigger, the synthetic
   `session.status_running` plus the projection update;
5. writes a **coalescible** orchestration outbox wakeup carrying the highest
   receipt sequence.

The outbox is a wakeup, not a run queue: a second admission before delivery
coalesces into the same row and raises its sequence (`UpsertOutbox` uses
`ON CONFLICT ... GREATEST`). The client is told its input was accepted only after
this transaction commits, so admission survives an execution-plane outage.

### SessionWorkflow

`temporal.SessionWorkflow` is keyed by the public session ID (so
Signal-With-Start is idempotent). It holds only a small durable cursor — the
last observed receipt sequence — and:

- waits on a wakeup Signal carrying **metadata only** (the highest known
  sequence), never event payloads;
- loads authoritative events from PostgreSQL after its cursor via the
  `LoadEvents` Activity;
- drives one `RunTurn` Activity per `user.message`, in receipt order;
- advances the cursor monotonically, so a duplicate or out-of-order wakeup at or
  below the cursor is a harmless no-op (sequence-based duplicate/gap protection);
- carries the cursor across **Continue-As-New** so history stays bounded.

### Outbox relay

`temporal.Relay` claims pending wakeups (`FOR UPDATE SKIP LOCKED`), delivers each
with **Signal-With-Start**, and deletes the row only if no later admission raised
its sequence in the meantime. Correctness depends on the outbox, not on any fast
path:

- a crash after signaling but before deleting leaves the row and re-delivers a
  harmless duplicate;
- a delivery failure (Worker/Temporal unavailable) records the attempt and
  leaves the row for retry.

The API/orchestrator may make a best-effort post-commit Signal to reduce latency
(`Orchestrator.Admit`), but the relay is the source of correctness.

### Vertical path for one `user.message`

`temporal.Runtime` wires the worker, relay, signaler, and store. The `RunTurn`
Activity invokes the **existing** agent runtime (`agentruntime.AgentCore`) for a
plain `user.message` with no toolset — one model round — and commits the
authoritative `agent.message` and terminal `session.status_idle` through
`pg.Store.CompleteTurn`, which is **idempotent**: a duplicate Activity execution
finds the trigger already processed and replays the same committed events instead
of appending a second copy.

Run it with the feature-gated subcommand:

```sh
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
go run ./cmd/managed-agent orchestrate
```

## Configuration

| Variable | Used by | Meaning |
| --- | --- | --- |
| `MANAGED_AGENT_DATABASE_URL` | `orchestrate` | PostgreSQL connection string (required). |
| `MANAGED_AGENT_TEMPORAL_HOSTPORT` | `orchestrate` | Temporal frontend (default `localhost:7233`). |
| `MANAGED_AGENT_TEMPORAL_NAMESPACE` | `orchestrate` | Temporal namespace (default `default`). |
| `MANAGED_AGENT_TEST_DATABASE_URL` | tests | Enables PostgreSQL integration tests; unset skips them. |
| `MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT` | tests | With the DB var, enables the real end-to-end test. |

The default `serve` command (SQLite) reads none of these and is unchanged.

## Tests

`go test ./...` passes with no local stack: the PostgreSQL and Temporal
integration tests skip unless their env vars are set. Failure-boundary coverage:

| Property | Test |
| --- | --- |
| Atomic event + outbox admission | `pg.TestAdmitEvents_AtomicEventAndOutbox` |
| Coalescing outbox (not a queue) | `pg.TestAdmitEvents_CoalescesWakeup` |
| Ordered event consumption | `pg.TestAdmitEvents_OrderedConsumption`, `temporal.TestSessionWorkflow_OrderedConsumption` |
| Idempotent completion (safe API/Activity retry) | `pg.TestCompleteTurn_Idempotent` |
| Delete respects coalesced sequence | `pg.TestOutbox_DeleteRespectsCoalescedSequence` |
| `SKIP LOCKED` claim | `pg.TestOutbox_ClaimSkipsLocked` |
| Duplicate wakeups processed once | `temporal.TestSessionWorkflow_DuplicateWakeupsProcessOnce` |
| Relay crash after signal, before delete | `temporal.TestRelay_CrashAfterSignalBeforeDeleteRedelivers` |
| Worker temporarily unavailable | `temporal.TestRelay_WorkerUnavailableLeavesWakeup` |
| Continue-As-New carries cursor | `temporal.TestSessionWorkflow_ContinueAsNewCarriesCursor` |
| Real end-to-end (Temporal + PostgreSQL) | `temporal.TestVerticalSlice_EndToEnd` |

To run the integration tests locally:

```sh
make -C deployments/local up && make -C deployments/local health
export MANAGED_AGENT_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT="localhost:7233"
go test ./internal/pg/... ./internal/temporal/...
```

## Explicit limitations

This is a foundation, not the migration. The following are deliberately **out of
scope** and not implemented on the Temporal path:

- **No traffic cutover.** The HTTP API still runs entirely on the SQLite
  dispatcher. The `orchestrate` command runs the execution plane only.
- **Only `user.message` with no toolset.** Tools, sandboxes, the tool journal at
  the Activity boundary, custom-tool/`always_ask` parking, and interrupts are not
  wired into the Temporal path. The existing tool ambiguity journal
  (`internal/app/tool_journal.go`, `internal/store/execution_store.go`) is
  preserved and unchanged.
- **No exactly-once tool execution.** As documented in the fit review, no
  component removes the ambiguity between an external side effect and its
  acknowledgment. This slice does not claim otherwise.
- **No previews/SSE over NATS.** NATS is in the stack but the workflow path does
  not yet publish previews or persisted-event wakeups through it.
- **No multiagent, schedules, webhooks, memory/vault/files, or sandbox
  checkpointing.**
- **No production deployment manifests.** `deployments/local` is for development
  and integration tests only.
- **No Worker Versioning rollout yet.** The Continue-As-New path is tested; a
  rolling worker-version deployment is a later slice.

## Next smallest slice

The next increment that keeps the same boundaries: route a tool-using
`user.message` through the `RunTurn` Activity by threading the existing
`ToolExecutionJournal` and a sandbox lease into the Activity, so a single
built-in tool step runs under Temporal with the durable prepared/started/
completed/ambiguous boundary preserved — still without cutting over the API.
