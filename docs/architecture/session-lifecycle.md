---
title: Session lifecycle
---

# Session lifecycle

## State transitions

```mermaid
stateDiagram-v2
  [*] --> idle
  idle --> running: input admitted
  running --> idle: end turn / requires action
  running --> rescheduling: retryable failure (planned)
  rescheduling --> running: retry claimed (planned)
  running --> terminated: terminal failure
  idle --> terminated: terminal action
```

`rescheduling` is part of the public status model but is not currently emitted:
the implementation has no automatic retry policy and avoids promising one.

## Input-to-output sequence

```mermaid
sequenceDiagram
  participant C as Client
  participant A as HTTP/Application
  participant DB as SQLite
  participant R as Agent runtime
  participant M as Messages API
  participant S as Sandbox

  C->>A: POST user.message (batch)
  A->>DB: input + status_running + one queued run per trigger
  DB-->>A: commit
  A-->>C: accepted input events
  A->>DB: claim next queued run (admission order)
  A->>DB: reconstruct causal run history
  A->>R: Run(snapshot, projected messages)
  R->>M: create streamed message
  M-->>R: text and/or tool_use
  opt built-in tool
    R->>S: execute
    S-->>R: tool result
    R->>M: continue with tool_result
  end
  R-->>A: buffered authoritative events
  A->>DB: output + processed_at + final status + completion
  DB-->>A: commit
  Note over A,DB: next run is claimed only after this commit,<br/>so it projects the previous run's committed output
```

## Admission invariant

The server commits all of the following atomically, in submitted input order:

- submitted client events;
- a `session.status_running` event when the session was not already running;
- the mutable session status;
- one queued internal run **per processable trigger event**, in admission
  order. A run references exactly one trigger; multiple triggers are never
  grouped into a single run.

The client is never told that input was accepted unless the corresponding work
item is durable.

## Two orderings of the event log

The event log is read in two distinct orders:

- **Public event history** — `GET .../events`, list, and the live SSE stream —
  is the immutable receipt/commit sequence. It is never reordered.
- **Model-facing conversation order** is reconstructed per turn from run
  causality. For each prior completed (or failed) run, in admission order, the
  projection replays that run's trigger event IDs followed by its persisted
  output event IDs, then appends the current run's trigger. The run's output
  event IDs are durable state committed in the same transaction that closes the
  run, so this ordering survives a restart and a run never sees a later trigger
  queued while it was still running. Tested by
  `TestRunStore_ModelHistorySurvivesReopenInCausalOrder` (file-backed reopen)
  and `TestSessionService_BatchedTriplePerRunCausalProjection` (three batched
  triggers project as three causal turns).

## Per-run boundary

Runs drain one at a time in admission order. A run commits its buffered
authoritative output and stamps its trigger processed *before* the next run is
claimed, so each run's causal history already includes the previous run's
committed output — a later user event in the same batch sees the earlier agent
reply (`TestRunStore_CompletionBeforeNextClaimObservesOutput`,
`TestSessionService_SecondUserEventObservesFirstAgentOutput`). A terminated
session is final: its leftover queued runs are never claimed, and it is never
flipped back to `running`.

## Per-session ordering

A partial unique database index permits at most one `running` item per session.
Additional input can be committed while a run executes, but it becomes later
queued work. Different sessions may run concurrently.

The process also uses sharded in-memory locks to serialize short state-changing
operations. Runtime and sandbox work happens outside both those locks and SQL
transactions.

## Completion invariant

On success or failure, one transaction:

- appends buffered authoritative runtime events;
- stamps the run's trigger events as processed;
- updates the session projection;
- marks the run completed or failed.

Events are published to active SSE subscribers only after the commit.

## Restart recovery

At startup, interrupted `running` runs are returned to `queued` and drained
again. This prevents silent loss, but it is at-least-once execution. A crash
after a tool side effect and before completion commit can repeat that side
effect.

Production-grade retries therefore require an attempt model, durable tool
journal, and idempotency contract before multiple workers are introduced.

## Client-required actions

A custom tool or `always_ask` built-in can park a run:

1. the runtime emits `agent.custom_tool_use` or `agent.tool_use`;
2. the session returns to `idle` with `stop_reason.type=requires_action`;
3. the stop reason names the event IDs the client must answer;
4. a custom tool response starts a new durable run.

Custom-tool resume is implemented and tested for a single park/result cycle.
Built-in `user.tool_confirmation` is accepted by the HTTP API, but its
allow/deny resume semantics are not complete.

There is no first-class durable pending-action gate. If other trigger runs were
already queued before a run parks with `requires_action`, their gating and
correlation against the parked action are not yet fully modeled; this case is
not claimed to work.
