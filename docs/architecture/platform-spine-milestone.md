---
title: Platform spine milestone
slug: /architecture/platform-spine-milestone
---

# Platform spine milestone

Status: **Implemented (primary HTTP + Workflow-owned agent-loop slice)**
Date: **2026-07-29**

This document describes the implemented Temporal platform spine, the HTTP
cutover built on it, and the remaining compatibility gates. It is the concrete
counterpart to the [target-platform decision](target-platform.md) and the
[orchestration fit review](orchestration-fit.md).

PostgreSQL/Temporal/NATS is the default `serve` backend. SQLite remains
available as a deprecated compatibility backend until durable client-action
waits and interrupt ordering reach parity.

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

`deployments/local/` brings up PostgreSQL, Temporal (with UI), NATS Core, the
HTTP API, and the worker with pinned versions and health checks. See
[the stack README](https://github.com/yanpgwang/managed-agent-go/tree/main/deployments/local).

```sh
make local-up
make local-health
```

| Service    | Image                          | Purpose                                    |
| ---------- | ------------------------------ | ------------------------------------------ |
| PostgreSQL | `postgres:17.5-alpine`         | Event ledger, projections, admission outbox |
| Temporal   | `temporalio/auto-setup:1.29.7` | Durable session orchestration              |
| Temporal UI| `temporalio/ui:2.52.1`         | Workflow explorer (`localhost:8233`)       |
| NATS Core  | `nats:2.11.17-alpine`          | Ephemeral previews / SSE wakeups           |
| API        | local image                     | PostgreSQL-backed HTTP API (`localhost:8080`) |
| Worker     | local image                     | Temporal worker and outbox relay           |

### PostgreSQL plumbing

`internal/pg` adds a `pgx` connection pool, embedded `goose` migrations
(`internal/pg/migrations`), and `sqlc`-generated typed queries
(`internal/pg/pgstore`, regenerated with `go generate ./internal/pg`). The
schema contains versioned Agents, Environments, a `sessions` projection, an
append-only `events` ledger with a durable per-session receipt sequence, a
coalescible `orchestration_outbox`, and the turn/tool journal.

The HTTP resource services now use these PostgreSQL repositories. SQLite is not
part of the primary data path.

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
- drives one deterministic plan-act-observe loop per `user.message`, in receipt
  order, with each model call and each tool call as a separate Activity;
- advances the cursor monotonically, so a duplicate or out-of-order wakeup at or
  below the cursor is a harmless no-op (sequence-based duplicate/gap protection);
- stops draining and ends when a turn reports it terminated the session, so a
  message queued behind a terminating one stays unprocessed and the session is
  never resurrected;
- non-blockingly coalesces buffered wakeups before terminal completion and
  Continue-As-New, so a Signal that arrived while an Activity was running is
  consumed on the next deterministic replay boundary instead of causing a
  rejected-close replay loop;
- carries the cursor across **Continue-As-New** so history stays bounded (at the
  compile-time turn threshold or when the Temporal server recommends it).

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

Session deletion uses a two-phase database fence around Temporal termination.
The first transaction rejects a running Session and marks an idle Session as
deleting, which blocks later admission. The API then terminates the Workflow and
physically deletes the fenced projection. An ambiguous termination error leaves
the fence in place so retrying DELETE is safe.

### Vertical path for one `user.message`

`temporal.Runtime` wires the worker, relay, signaler, store, and a session-scoped
sandbox manager. New Workflow executions use the standard Temporal AI-agent
shape:

1. `PrepareTurn` loads the immutable agent snapshot and causal model history.
2. `CallModel` performs one model call and returns the complete ordered response
   (including text plus every tool request) into Workflow history.
3. `ExecuteTool` performs exactly one built-in tool step under the PostgreSQL
   journal.
4. Workflow code appends the assistant round and tool results to its local
   message state and repeats until `end_turn`.
5. `CompleteWorkflowTurn` atomically finalizes the optional tool attempt and
   commits every public output event.

This boundary is the recovery mechanism: after a worker crash, Temporal
deterministically replays completed Activity results and schedules only the next
unfinished call. It does not reconstruct a conversation from the tool journal.
The legacy opaque `RunTurn` Activity remains registered behind
`workflow.GetVersion` solely so Workflow histories created by the previous
version remain replayable.

Two shapes are currently validated end to end:

- **No toolset** — one model round to `agent.message` + terminal
  `session.status_idle`.
- **A tool-using turn** — a `user.message` whose agent runs an always_allow
  built-in inside the session-scoped sandbox lease, under the durable
  tool-execution journal, and commits the paired `agent.tool_use` /
  `agent.tool_result` before `end_turn`. Unit coverage also proves one response
  can carry assistant text plus multiple tool calls without losing their round
  structure, and that retrying one tool Activity does not rerun the preceding
  model Activity.

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
turn is identified by `(session_id, trigger_event_id)`. Explicit attempt,
tool-step, and public tool-use event IDs are returned by Activities into
Workflow history, making `EnsureAttempt` / `EnsureToolStep` idempotent across
Activity retries without coupling one ID namespace to another.

Because a Temporal Activity may run more than once, each `ExecuteTool` attempt
first reads the durable step:

1. `completed` returns the recorded result without acquiring a sandbox or
   invoking the executor;
2. `prepared` provisions the sandbox, records `started`, executes, then records
   `completed`;
3. `started` with no result becomes `ambiguous` and returns that classification
   to the Workflow, which commits an honest terminal error instead of replaying
   the side effect;
4. `ambiguous` remains terminal and is never executed.

Durable writes after a side effect — recording the tool result, finalizing the
attempt, and committing the turn — run on a context detached from the Activity's
cancellation (`context.WithoutCancel` + a fresh bounded timeout). Attempt
finalization and public turn completion share one PostgreSQL transaction, so
there is no new crash window between those facts. Recording a known tool result
is idempotent and receives a small bounded write-only retry inside the same
Activity; this absorbs a transient or lost database acknowledgment before a
later Activity attempt must conservatively classify a still-started step as
ambiguous.

Transient model failures (connection errors, timeouts, conflicts, rate limits,
server errors, and overload) are retried at the model Activity boundary and
never touch the tool journal. Permanent provider failures are returned through
the Activity's existing terminal-result channel, so the Workflow commits an
honest `session.error` plus `session.status_terminated` instead of retrying an
unchanged request forever. Client-action tools (custom tools, `always_ask`) and
interrupts are still out of scope on this path and are rejected by the primary
HTTP backend before admission.

Run the API and worker as separate processes:

```sh
export MANAGED_AGENT_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_NATS_URL="nats://localhost:4222"
go run ./cmd/managed-agent serve
# In a second terminal with the same variables:
go run ./cmd/managed-agent orchestrate
```

## Configuration

| Variable | Used by | Meaning |
| --- | --- | --- |
| `MANAGED_AGENT_DATABASE_URL` | `serve`, `orchestrate` | PostgreSQL connection string (required). |
| `MANAGED_AGENT_TEMPORAL_HOSTPORT` | `serve`, `orchestrate` | Temporal frontend (default `localhost:7233`). |
| `MANAGED_AGENT_TEMPORAL_NAMESPACE` | `serve`, `orchestrate` | Temporal namespace (default `default`). |
| `MANAGED_AGENT_NATS_URL` | `serve`, `orchestrate` | NATS Core URL (default `nats://localhost:4222`). |
| `MANAGED_AGENT_TEST_DATABASE_URL` | tests | Enables PostgreSQL integration tests; unset skips them. |
| `MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT` | tests | With the DB var, enables the real end-to-end test. |
| `MANAGED_AGENT_TEST_NATS_URL` | tests | With the DB var, enables NATS integration tests. |

For temporary compatibility testing, `serve -backend sqlite -db <path>` uses
none of these services.

## Tests

`go test ./...` passes with no local stack: the PostgreSQL and Temporal
integration tests skip unless their env vars are set. The Docker-specific
end-to-end test also skips when the Docker CLI or daemon is unavailable.

Failure-boundary coverage:

| Property | Test |
| --- | --- |
| Atomic event + outbox admission | `pg.TestAdmitEvents_AtomicEventAndOutbox` |
| Agent/Environment archival cannot cross Session dependency locks | `pg.TestActiveResourceLocksFenceConcurrentArchival` |
| Session cursor precision cannot duplicate a page boundary | `pg.TestSessionPaginationUsesPostgresTimestampPrecision` |
| Delete/admission race is fenced before Workflow termination | `pg.TestSessionDeletionFenceBlocksAdmission`, `controlplane.TestDeleteFencesAdmissionBeforeWorkflowTermination` |
| Ambiguous Workflow termination is safely retryable | `controlplane.TestDeleteTerminationFailureKeepsFenceAndRetryCompletes` |
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
| New histories use granular Workflow loop; old histories retain RunTurn | `temporal.TestSessionWorkflow_NewExecutionUsesWorkflowOwnedLoop`, legacy-version session workflow tests |
| Assistant text + several tools retain one coherent model round | `temporal.TestWorkflowTurn_PreservesTextAndMultipleTools` |
| Tool Activity retry does not repeat the completed model step | `temporal.TestWorkflowTurn_ToolActivityRetryDoesNotRepeatModelStep` |
| Workflow ambiguity commits an honest terminal error | `temporal.TestWorkflowTurn_AmbiguousToolTerminatesHonestly` |
| Unsupported client-action batch is rejected before its first side effect | `temporal.TestWorkflowTurn_RejectsClientActionBatchBeforeSideEffects` |
| Termination stops the batch; queued msg stays unprocessed | `temporal.TestSessionWorkflow_TerminationStopsBatch`, `temporal.TestRunTurn_TerminationReportedAndBQueuedUnprocessed` |
| Durable writes survive Activity cancellation after a side effect | `temporal.TestRunTurn_DurableWritesSurviveCancellation` |
| Tool step happy path (prepared→started→completed) | `temporal.TestRunTurn_ToolStepHappyPath`, `pg.TestJournal_HappyPath` |
| Ambiguous (started, no result) not silently replayed | `temporal.TestRunTurn_AmbiguousToolNotReplayed`, `pg.TestJournal_StartedStepRecoveredAsAmbiguous` |
| Completed step not replayed, not called ambiguous | `temporal.TestRunTurn_CompletedStepNotReplayedNotCalledAmbiguous` |
| Completed result + lost database acknowledgment retries the write without re-execution | `temporal.TestWorkflowTurn_ToolResultWriteRetryDoesNotReexecute`, `temporal.TestExecuteTool_CompletedStepReturnsWithoutReexecution`, `pg.TestJournal_WorkflowCompletedStepReturnsDurableResult` |
| Workflow started step becomes ambiguous without sandbox/executor | `temporal.TestExecuteTool_StartedStepBecomesAmbiguousWithoutReexecution` |
| Attempt finalization + public completion are one idempotent transaction | `pg.TestCompleteWorkflowTurn_FinalizesAttemptAndTurnAtomically` |
| Attempt refuses to close with unclassified started step | `pg.TestJournal_FinishRefusesUnclassifiedStartedStep` |
| One active attempt per turn | `pg.TestJournal_OneActiveAttempt` |
| Recovered/concurrently fenced stale attempt cannot start a prepared step | `pg.TestJournal_StalePreparedStepCannotStartAfterRecovery`, `pg.TestJournal_StartWaitsForConcurrentAttemptFence` |
| Idempotent retry after a processed turn | `temporal.TestRunTurn_IdempotentAfterProcessed` |
| Real end-to-end (Temporal + PostgreSQL) | `temporal.TestVerticalSlice_EndToEnd`, `temporal.TestVerticalSlice_ToolStepEndToEnd` |
| Real tool execution inside Docker sandbox | `temporal.TestVerticalSlice_DockerToolStepEndToEnd` |

To run the integration tests locally:

```sh
make local-up && make local-health
export MANAGED_AGENT_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/managed_agent?sslmode=disable"
export MANAGED_AGENT_TEST_TEMPORAL_HOSTPORT="localhost:7233"
export MANAGED_AGENT_TEST_NATS_URL="nats://localhost:4222"
go test ./internal/pg/... ./internal/controlplane/... ./internal/live/... ./internal/temporal/...
```

## Explicit limitations

The primary traffic cutover is complete, but these limitations remain:

- **Always-allow built-ins only on the Workflow path.** Completed tool steps
  resume from their durable result, including multi-tool/multi-round message
  structure. Client-action tools (custom tools, `always_ask` confirmations) and
  their park/resume protocol, and `user.interrupt` cancellation, are out of
  scope and receive `422 unsupported_error` before admission. The
  SQLite path's own tool journal (`internal/app/tool_journal.go`,
  `internal/store/execution_store.go`) is preserved and unchanged; the Temporal
  path adds a parallel PostgreSQL journal (`turn_attempts`, `tool_steps`) rather
  than reusing the SQLite store.
- **Sandbox lease is in-process.** The session-scoped sandbox is cached in-memory
  by `sandbox.SessionManager`; a worker restart drops it and it is not shared
  across worker replicas. A durable/provider-backed lease is future work.
- **No large-payload offload yet.** Model responses and tool results enter
  Temporal history so replay preserves exact round structure. Sandbox command
  output is bounded, but object-store/payload-codec offload for unusually large
  file and model content is still a production hardening item.
- **No exactly-once tool execution.** As documented in the fit review, no
  component removes the ambiguity between an external side effect and its
  acknowledgment. This slice makes that ambiguity explicit (a step left `started`
  becomes `ambiguous` and the turn is refused, never silently replayed); it does
  not eliminate it.
- **No multiagent, schedules, webhooks, memory/vault/files, or sandbox
  checkpointing.**
- **No production deployment manifests.** `deployments/local` is for development
  and integration tests only.
- **No Worker Versioning rollout yet.** The Continue-As-New path is tested; a
  rolling worker-version deployment is a later slice.

## Next slices

Two parity gates remain before the SQLite backend can be deleted:

- **Client-action tools under Temporal.** Model a park on a custom tool or an
  `always_ask` confirmation as a durable wait in the `SessionWorkflow`: commit
  the pending action + `session.status_idle{requires_action}`, and resume the
  turn when the resolving `user.custom_tool_result` / `user.tool_confirmation` is
  admitted and delivered as a wakeup — reusing the existing pending-action
  semantics rather than inventing a Temporal-specific park protocol.
- **Durable interrupt.** Deliver `user.interrupt` across API/worker processes,
  cancel the active model/tool Activity, and preserve the documented
  finish-vs-interrupt event ordering.

Harness work can proceed in parallel because both gates use the established
PostgreSQL/Temporal boundaries rather than changing the runtime/provider
contracts.
