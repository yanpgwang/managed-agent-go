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

## Architecture re-review (2026-07-28)

A short re-review was run before extending the slice, against the newly
confirmed native **Temporal Schedules** capability and the runner-up
orchestrators. Conclusion: **no architectural blocker; Temporal stands.**

- Native Schedules (POSIX cron + IANA time zone, DST, jitter, pause, overlap
  policy, manual trigger) only **strengthens** the existing decision — scheduling
  was already the dimension where Temporal was "materially better" in the
  [fit review](orchestration-fit.md). Each Schedule firing starts a **Workflow
  Execution**; it does not by itself produce a Managed Agents *deployment-run
  record*. That run record is a product-domain fact this project owns — the
  started run workflow writes it to PostgreSQL. Schedules remove a hand-rolled
  cron/timer engine from the roadmap, not the domain run-record logic.
- **Restate** remains the credible runner-up but is not overturned: it still has
  no native cron (its guide hand-rolls a Cron Virtual Object), its Go SDK 1.0 is
  weeks old, and it does not remove the PostgreSQL event-ledger ownership, the
  idempotency boundary, or the tool-ambiguity journal — the exact boundaries this
  slice implements.
- **Hatchet** (PostgreSQL-backed task orchestration) and **Inngest**
  (event-driven durable functions) are viable durable-execution engines with
  cron, but neither is a stronger fit than Temporal for one *long-lived* workflow
  per session with Continue-As-New, child-workflow multiagent, and worker
  versioning, and neither has comparable Go + coding-agent production evidence.
  Both would still leave PostgreSQL as the event source of truth.
- **Trigger.dev** is TypeScript-first with no first-class Go SDK — disqualified
  for this Go control plane.

The data-ownership boundary is unchanged: PostgreSQL owns public events,
projections, the admission outbox, and now the tool-execution journal; Temporal
owns only in-flight orchestration. Nothing here motivates moving an
authoritative fact into the orchestrator.

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
- stops draining and ends when a turn reports it terminated the session, so a
  message queued behind a terminating one stays unprocessed and the session is
  never resurrected;
- non-blockingly coalesces buffered wakeups before terminal completion and
  Continue-As-New, so a Signal that arrived while an Activity was running is
  consumed on the next deterministic replay boundary instead of causing a
  rejected-close replay loop;
- carries the cursor across **Continue-As-New** so history stays bounded (the
  production threshold is a compile-time constant).

### Causal model history and no intermediate idle

Public receipt order is the immutable API truth, but it is deliberately **not**
what a turn replays into the model. For a batch admitted as A,B (both queued
before either runs), turn B's model history is the causal chain
`[A, agent(A), B]`, reconstructed by `pg.Store.HistoryThrough`: it walks the
prior *processed* `user.message` triggers, appends each trigger followed by the
exact output events that turn committed (correlated by `turn_event_id`), then
appends the current trigger. Replaying raw receipt order would instead place A
and B as two consecutive user turns, hiding A's answer from B. The reconstruction
is bounded to the newest `limit` events, preserving causal order.

Correspondingly, `CompleteTurn` does not emit an intermediate
`session.status_idle`: when an ordinary end_turn completion still has unprocessed
`user.message` work queued, the session stays `running` and the terminal idle
draft is dropped. Only the last turn in the batch idles the session, so a batch
produces exactly one public idle.

### Outbox relay

`temporal.Relay` lists pending wakeups (a plain `ORDER BY enqueued_at` read,
oldest first — not a lease or claim), delivers each with **Signal-With-Start**,
and deletes the row only if no later admission raised its sequence in the
meantime. Delivery is **at-least-once**: two relay instances can read the same
row and both Signal it, which is harmless because the workflow deduplicates by
receipt sequence. Correctness depends on the outbox, not on any fast path:

- a crash after signaling but before deleting leaves the row and re-delivers a
  harmless duplicate;
- a delivery failure (Worker/Temporal unavailable) records the attempt and
  leaves the row for retry.

The API/orchestrator may make a best-effort post-commit Signal to reduce latency
(`Orchestrator.Admit`), but the relay is the source of correctness.

### Vertical path for one `user.message`

`temporal.Runtime` wires the worker, relay, signaler, store, and a session-scoped
sandbox manager. The `RunTurn` Activity invokes the **existing** agent runtime
(`agentruntime.AgentCore`) and commits the authoritative output through
`pg.Store.CompleteTurn`, which is **idempotent**: an already-processed trigger
short-circuits before the model runs and replays the same committed events
instead of appending a second copy.

Two shapes are currently validated end to end:

- **No toolset** — one model round to `agent.message` + terminal
  `session.status_idle`.
- **A tool-using turn** — a `user.message` whose agent runs an always_allow
  built-in inside the session-scoped sandbox lease, under the durable
  tool-execution journal, and commits the paired `agent.tool_use` /
  `agent.tool_result` before `end_turn`. The bounded model↔tool loop in
  `AgentCore` is not artificially capped at one tool call, but the end-to-end
  path exercised and tested in this slice is a single always_allow built-in step
  to `end_turn`; a `RunTurn` retry after a tool step has run does not resume the
  loop (see the ambiguity boundary and *Next smallest slice*).

The **sandbox lease** is provided by an in-process `sandbox.SessionManager`
keyed by session id: within one worker process, later turns reuse the same
sandbox so filesystem state persists across turns. This lease is in-memory — a
worker restart drops it, and a durable/provider-backed lease that survives
restarts (and is shared across worker replicas) is future work, gated behind the
sandbox provider contract.

### Tool-execution journal and the ambiguity boundary

A tool-using turn preserves the same side-effect state machine as the SQLite
path, now in PostgreSQL (`turn_attempts`, `tool_steps`, migration `00002`):

```text
prepared ──▶ started ──▶ completed
                   └────▶ ambiguous
```

`ambiguous` is a branch out of `started`, not a state after `completed`: a step
that began (its side effect may have happened) but never recorded a trustworthy
result is `ambiguous`; a step that recorded a durable result is `completed`. The
turn is identified by `(session_id, trigger_event_id)`; each `RunTurn` Activity
execution is an attempt. The runtime calls the journal's `Prepare` / `Start` /
`Complete` around each built-in execution.

Because a Temporal Activity runs at least once, `RunTurn` protects the
side-effect boundary on entry:

1. an already-processed trigger short-circuits (no re-execution), reporting the
   session's current projected status so a prior termination is not mistaken for
   an ordinary completion;
2. otherwise, for a tool-using turn, **recovery runs first** — any tool step left
   in `started` by a crashed prior attempt is classified **ambiguous** and the
   active attempt is failed;
3. the `prepared → started` write atomically requires its parent attempt to
   remain `active`, so an overlapping stale Activity cannot execute a prepared
   step after recovery fenced its attempt;
4. if the turn already crossed the side-effect boundary (a `completed` or
   `ambiguous` step exists), `RunTurn` **refuses to re-run** and terminates the
   turn honestly (`session.error` + `session.status_terminated`) rather than
   silently replaying the side effect. The refusal wording distinguishes the two:
   a `completed` step is reported as prior tool execution that cannot yet be
   resumed (resuming from a durable result is deferred — see *Next smallest
   slice*), never as "ambiguous".

Durable writes after a side effect — recording the tool result, finalizing the
attempt, and committing the turn — each run on a context detached from the
Activity's cancellation (`context.WithoutCancel` + a fresh bounded timeout,
created per write). So a Temporal cancellation arriving after a tool's side
effect cannot prevent recording that it happened; without this the step would be
left `started` and recovered as `ambiguous` even though it actually completed.

A model error *before* any tool step leaves no started step, so it is safely
retried as a fresh attempt. Client-action tools (custom tools, `always_ask`) and
interrupts are still out of scope on this path and terminate the turn honestly if
requested.

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
| Causal history + one idle per batch (A,B → A,agent(A),B) | `pg.TestHistoryThrough_CausalBatchOrdering` |
| Idempotent completion (safe API/Activity retry) | `pg.TestCompleteTurn_Idempotent` |
| Delete respects coalesced sequence | `pg.TestOutbox_DeleteRespectsCoalescedSequence` |
| Outbox read is at-least-once (no lease) | `pg.TestOutbox_ListIsAtLeastOnce` |
| Duplicate wakeups processed once | `temporal.TestSessionWorkflow_DuplicateWakeupsProcessOnce` |
| Buffered wakeup consumed before close / Continue-As-New | `temporal.TestSessionWorkflow_DrainsBufferedWakeupBeforeCloseBoundary` |
| Relay crash after signal, before delete | `temporal.TestRelay_CrashAfterSignalBeforeDeleteRedelivers` |
| Worker temporarily unavailable | `temporal.TestRelay_WorkerUnavailableLeavesWakeup` |
| Continue-As-New carries cursor | `temporal.TestSessionWorkflow_ContinueAsNewCarriesCursor` |
| Termination stops the batch; queued msg stays unprocessed | `temporal.TestSessionWorkflow_TerminationStopsBatch`, `temporal.TestRunTurn_TerminationReportedAndBQueuedUnprocessed` |
| Durable writes survive Activity cancellation after a side effect | `temporal.TestRunTurn_DurableWritesSurviveCancellation` |
| Tool step happy path (prepared→started→completed) | `temporal.TestRunTurn_ToolStepHappyPath`, `pg.TestJournal_HappyPath` |
| Ambiguous (started, no result) not silently replayed | `temporal.TestRunTurn_AmbiguousToolNotReplayed`, `pg.TestJournal_StartedStepRecoveredAsAmbiguous` |
| Completed step not replayed, not called ambiguous | `temporal.TestRunTurn_CompletedStepNotReplayedNotCalledAmbiguous` |
| Attempt refuses to close with unclassified started step | `pg.TestJournal_FinishRefusesUnclassifiedStartedStep` |
| One active attempt per turn | `pg.TestJournal_OneActiveAttempt` |
| Recovered/concurrently fenced stale attempt cannot start a prepared step | `pg.TestJournal_StalePreparedStepCannotStartAfterRecovery`, `pg.TestJournal_StartWaitsForConcurrentAttemptFence` |
| Idempotent retry after a processed turn | `temporal.TestRunTurn_IdempotentAfterProcessed` |
| Real end-to-end (Temporal + PostgreSQL) | `temporal.TestVerticalSlice_EndToEnd`, `temporal.TestVerticalSlice_ToolStepEndToEnd` |

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
- **One tool-using turn validated; no resume across a tool step.** The path
  exercised and tested end to end is an always_allow built-in step to `end_turn`.
  A `RunTurn` retry after a tool step has run does not resume the model loop from
  the durable result — it terminates honestly. Client-action tools (custom tools,
  `always_ask` confirmations) and their park/resume protocol, and `user.interrupt`
  cancellation, are out of scope and terminate the turn honestly if requested. The
  SQLite path's own tool journal (`internal/app/tool_journal.go`,
  `internal/store/execution_store.go`) is preserved and unchanged; the Temporal
  path adds a parallel PostgreSQL journal (`turn_attempts`, `tool_steps`) rather
  than reusing the SQLite store.
- **Sandbox lease is in-process.** The session-scoped sandbox is cached in-memory
  by `sandbox.SessionManager`; a worker restart drops it and it is not shared
  across worker replicas. A durable/provider-backed lease is future work.
- **No exactly-once tool execution.** As documented in the fit review, no
  component removes the ambiguity between an external side effect and its
  acknowledgment. This slice makes that ambiguity explicit (a step left `started`
  becomes `ambiguous` and the turn is refused, never silently replayed); it does
  not eliminate it.
- **No previews/SSE over NATS.** NATS is in the stack but the workflow path does
  not yet publish previews or persisted-event wakeups through it.
- **No multiagent, schedules, webhooks, memory/vault/files, or sandbox
  checkpointing.**
- **No production deployment manifests.** `deployments/local` is for development
  and integration tests only.
- **No Worker Versioning rollout yet.** The Continue-As-New path is tested; a
  rolling worker-version deployment is a later slice.

## Next smallest slice

Two candidates keep the same boundaries and API non-cutover:

- **Client-action tools under Temporal.** Model a park on a custom tool or an
  `always_ask` confirmation as a durable wait in the `SessionWorkflow`: commit
  the pending action + `session.status_idle{requires_action}`, and resume the
  turn when the resolving `user.custom_tool_result` / `user.tool_confirmation` is
  admitted and delivered as a wakeup — reusing the existing pending-action
  semantics rather than inventing a Temporal-specific park protocol.
- **Resume from a durable tool result.** Instead of terminating a turn whose
  prior attempt has a `completed` (not ambiguous) tool step, re-project that
  durable result and continue the model loop — turning the current honest refusal
  into a genuine recovery for the unambiguous case, while still refusing the
  ambiguous one.
