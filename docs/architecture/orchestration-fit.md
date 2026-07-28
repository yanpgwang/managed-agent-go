---
title: Managed Agents orchestration fit review
slug: /architecture/orchestration-fit
---

# Managed Agents orchestration fit review

Status: **Accepted**  
Decision date: **2026-07-28**

This review validates the selected orchestration architecture against the
documented Claude Managed Agents API rather than against a generic "durable
agent" model. It also evaluates Restate as a serious alternative to Temporal.

## Conclusion

The complete target architecture is compatible with the documented Managed
Agents behavior. Temporal remains the selected orchestrator.

That conclusion needs two qualifications:

1. Temporal alone is not a Managed Agents implementation. PostgreSQL, NATS, the
   sandbox provider, object storage, and this repository's domain logic each own
   behavior that does not belong in an orchestrator.
2. Event admission must not use a synchronous Workflow Update that writes
   PostgreSQL from an Activity. The API must first commit the public events and
   a transactional outbox record in PostgreSQL, return the admitted events, and
   then wake the Workflow with a Temporal Signal. This lets the control plane
   accept work while execution workers are unavailable and removes a
   PostgreSQL/Temporal dual-write race.

Restate can implement the core Agent loop. Its keyed Virtual Objects,
invocation cancellation, durable signals, and short per-turn invocations form a
credible architecture. It is not selected because the complete API surface
would require more product infrastructure around it, especially a custom cron
scheduler, while its Go SDK and production evidence are substantially newer.
It does not remove the need for the PostgreSQL event ledger, an idempotent
PostgreSQL-to-orchestrator boundary, or the side-effect-ambiguity journal.

## What "compatible" means

The review distinguishes four kinds of fit:

- **Native**: the selected component directly provides the required lifecycle
  or reliability primitive.
- **Composed**: two selected components provide the behavior through a small,
  explicit integration pattern.
- **Domain**: this repository must implement the rule because it is part of the
  Managed Agents product contract rather than infrastructure.
- **Provider**: the capability belongs to a sandbox, model, MCP, storage, or
  secret provider.

A Domain or Provider result is not an architectural gap. A gap exists only when
the behavior cannot be expressed safely, or when expressing it would recreate a
general-purpose workflow, database, stream, or sandbox system in this
repository.

## API behavior inventory

The following behaviors materially constrain the architecture.

### Session and admission

- A Session is a long-lived Agent instance that retains conversation history
  across interactions.
- `initial_events` is an atomic, all-or-nothing part of Session creation. It
  accepts at most 50 supported events, persists them in list order, and starts
  the Session in `running` when non-empty.
- Submitted events can wait behind earlier events. Their public
  `processed_at` remains null until processing finishes, except for the
  documented on-receipt event types.
- A Session progresses through `idle`, `running`, `rescheduling`, and
  `terminated`.
- Only `agent.tools` and `agent.mcp_servers` can be replaced mid-Session, and
  only while the Session is idle.
- A running Session cannot be archived or deleted.

### Steering and client actions

- One request can contain `user.interrupt` followed by a redirecting
  `user.message`.
- The interrupt must stop current work, but the interrupted turn still closes
  with the ordinary `end_turn` stop reason.
- Custom tools and permission checks can produce several blocking events.
  Every blocking event must be resolved before execution resumes.
- A result is addressed by the original event ID. In a multiagent Session, the
  server routes it to the correct thread.

### Events and streaming

- Persisted buffered events are authoritative and listable.
- Text/thinking previews are opt-in, thread-scoped, best-effort, non-replayable,
  and never persisted.
- A reconnect lists authoritative history instead of replaying missed preview
  deltas.

### Multiagent execution

- The coordinator and subagents have separate, persistent thread histories.
- Threads can execute concurrently and are dynamically created.
- The Session is `running` while any thread is running.
- All threads share one sandbox, filesystem, and vault binding while retaining
  separate Agent configuration and conversation context.
- The documented limit is 25 concurrent threads.

### Deployments and delivery

- Scheduled deployments use POSIX cron plus an IANA time zone, wall-clock DST
  semantics, bounded jitter, pause/resume, manual triggering, and a run record
  for every attempt.
- Webhooks may be duplicated, have no ordering guarantee or backfill, make at
  most three attempts, and are not the authoritative event log.
- A self-hosted environment worker can be absent; work remains queued until an
  execution worker claims it.

## Correct target data path

The public event ledger is committed before orchestration is notified.

```mermaid
sequenceDiagram
  participant C as Client
  participant A as API
  participant P as PostgreSQL
  participant O as Outbox relay
  participant T as Temporal
  participant W as Session Workflow

  C->>A: POST ordered event batch
  A->>P: lock Session; validate batch and pending actions
  A->>P: append events, receipt sequence, projection, outbox wakeup
  P-->>A: commit
  A-->>C: admitted event objects
  A-->>T: best-effort Signal-With-Start
  O->>P: claim undelivered wakeup
  O->>T: retry Signal-With-Start
  T-->>O: Signal durably accepted
  O->>P: mark wakeup delivered
  T->>W: deliver Signal when a Worker is available
  W->>P: load admitted events after durable cursor
```

The outbox entry is a coalescible wakeup, not an executable job. It contains the
Session identity and highest known event sequence. The Workflow loads the
authoritative commands from PostgreSQL and ignores sequences it already
observed. Therefore:

- a crash before commit exposes neither events nor work;
- a crash after commit leaves a retryable outbox wakeup;
- a crash after signaling but before marking the outbox delivered causes a
  harmless duplicate wakeup;
- Signals that arrive out of order cannot reorder public events;
- an API replica and a Temporal Worker do not have to be online at the same
  time.

The API may make the first post-commit Signal attempt to reduce latency. The
outbox relay remains responsible for eventual delivery.

Temporal Workflow Updates remain useful for synchronous operator commands that
require a live Workflow response. They are not the admission boundary for
public Session events: an Update reaches its accepted stage only after a Worker
has processed its validator.

## Compatibility matrix

| Managed Agents behavior | Required owner | Temporal architecture | Restate architecture | Result |
| --- | --- | --- | --- | --- |
| Agent, Environment, Session, file, memory, vault, and deployment CRUD/list/filter | PostgreSQL and object storage | Orchestrator is not used for ordinary resource reads | Restate K/V cannot replace relational listing and filtering | Domain; equal |
| Atomic Session plus ordered `initial_events` | PostgreSQL transaction | Transaction also writes an outbox wakeup; Signal-With-Start creates the Workflow idempotently | A Restate admission handler can make the SQL transaction an idempotent durable step, or a conventional API can use the same outbox pattern | Composed; equal |
| Accept input while execution workers are unavailable | Durable control-plane ingress | Temporal Service accepts a Signal without a Workflow Worker; the PostgreSQL relay retries service outages | Restate ingress durably persists the admission invocation before an Agent turn executes | Native/composed in both |
| Long-lived, multi-turn Session identity | Orchestrator | One stable Session Workflow ID, carried across Continue-As-New | One persistent Session Virtual Object plus separate turn invocations | Native in both; Temporal has one lifecycle |
| One active turn with later events queued in receipt order | Orchestrator plus event ledger | Workflow is the sole turn driver and consumes PostgreSQL sequences | A short exclusive Virtual Object handler queues input and starts a turn invocation | Native/composed in both |
| Batch admission and per-event `processed_at` | PostgreSQL | Workflow stamps events idempotently after each causal turn | Turn completion callback stamps events idempotently | Domain; equal |
| Durable interrupt followed by redirect | Event ledger plus cancellation | Commit interrupt first; Signal handler cancels the scoped model/tool Activity; redirect remains queued | Session object cancels the stored turn invocation and starts or queues the redirect | Native/composed in both |
| Multiple custom tool or confirmation waits | Domain plus orchestrator | Pending-action IDs live in Workflow state and PostgreSQL; resume only when all are resolved | Signals/awakeables support the wait; parking the turn and starting a continuation avoids a months-long pinned invocation | Native primitives in both |
| Explicit `rescheduling` projection | Domain | Use an explicit Workflow retry loop around a single-attempt Activity when the retry must be public | Use bounded one-attempt `Run` steps and explicit delayed attempts when the retry must be public | Domain in both; hidden engine retries are insufficient |
| Authoritative buffered events | PostgreSQL | Activity writes append-only event ledger | Durable step writes the same ledger | Domain; equal |
| Best-effort thread-scoped previews | NATS Core | Activity publishes to NATS; no Workflow-history payload | Handler publishes to NATS; Restate's documented SSE helper is not needed | Composed; equal |
| Persistent concurrent agent threads | Orchestrator | Child Workflows provide explicit parent/child lifecycle, cancellation, and independent history | One Virtual Object and turn-invocation chain per thread; Session object aggregates status through callbacks | Temporal is more direct |
| Shared sandbox with isolated histories | Sandbox provider plus domain IDs | Parent passes one lease reference to child Workflows | Session and thread objects pass one lease reference to invocations | Provider; equal |
| Idle-only config mutation and archive/delete guards | PostgreSQL transaction | Public projection is checked under lock; Workflow receives the committed change/close wakeup | Object can serialize the command, but public SQL still owns API state | Domain; equal |
| POSIX cron, IANA time zone, DST, jitter, pause, overlap, and manual runs | Scheduler | Temporal Schedules natively provide calendar specs, time zones, jitter, pause, manual trigger, overlap and catch-up policies | Restate explicitly has no native cron; its guide implements a custom Virtual Object scheduler and calls out time-zone/failure work | Temporal materially better |
| Independent deployment-run records | PostgreSQL plus scheduler | Each Schedule action starts a run Workflow that writes one run record | Custom scheduler must create and reconcile run invocations | Temporal more direct |
| Signed, duplicate, unordered, maximum-three-attempt webhooks | Orchestrator plus PostgreSQL | Delivery Workflow/Activity with an explicit three-attempt policy | Durable invocation with a bounded retry policy | Native primitives in both |
| Session code upgrades over a long lifetime | Orchestrator deployment model | Worker Versioning supports pinned or auto-upgrade Workflows; Continue-As-New keeps the stable ID and freshens history | Immutable deployments pin each in-flight invocation; Restate recommends avoiding handlers that last days or months, so the design must split every turn/park boundary | Temporal materially better |
| Go SDK and coding-agent production evidence | Technology maturity | Mature Go SDK and a documented Replit Agent control-plane deployment using one Workflow per Agent, Updates, and Activities | Official Go SDK reached 1.0 on 2026-06-30; AI patterns are strong but equivalent public coding-agent production evidence is not yet available | Temporal lower adoption risk |
| Local operational weight | Developer experience | Local dev server is simple, but a production self-hosted cluster is heavier | Single-binary local/server model and Virtual Objects are lighter | Restate better |
| Non-idempotent external tool side effects | Domain journal and provider idempotency | Activities are at least once; an external commit followed by a lost result is ambiguous | A `Run` result is durable only after Restate records it; official database/saga guidance still uses idempotency, conditional writes, or two-phase commit | Neither removes ambiguity |

## Temporal execution model

### Session Workflow

One `SessionWorkflow` owns only orchestration state:

- last observed public event sequence;
- current turn and cancellation scope;
- pending action IDs;
- active child thread IDs and aggregate state;
- retry timers and attempt numbers;
- small provider references, never large model/tool payloads.

PostgreSQL owns the complete public transcript and resource projections. The
Workflow loads new event IDs after a wakeup and processes them in receipt
sequence. Continue-As-New carries the small cursor and orchestration state to a
fresh history under the same Workflow ID.

### Turn cancellation

The Workflow waits on both the current Activity future and its Signal channel.
When a committed interrupt sequence is observed, it cancels the current
cancellable Activity context. Model, MCP, and sandbox adapters must propagate
that context and heartbeat where appropriate. The Workflow then records the
ordinary `end_turn` projection before admitting the redirecting message.

Cancellation is scoped to the active turn or selected thread; it does not
terminate the long-lived Session Workflow.

### Client-required actions

A model turn that requests client work commits:

- the tool-use events;
- all pending-action rows;
- `session.status_idle{requires_action}`;
- the completed turn boundary.

The Workflow then waits rather than holding compute. Result events are
validated and committed by the API against the pending rows. Wakeup Signals
cause the Workflow to reload the pending set. Only the transition from a
non-empty set to an empty set starts the continuation turn.

### Retries

Infrastructure-level Activity retries are useful when they are intentionally
invisible. A retry that must expose `rescheduling` uses an explicit Workflow
loop:

1. execute one model/sandbox attempt;
2. classify the failure;
3. commit `rescheduling` and attempt metadata;
4. wait on a durable backoff timer;
5. commit `running` and try again.

This is API-specific status logic, not a replacement retry engine.

### Multiagent

The primary Session Workflow creates child thread Workflows dynamically. Each
child owns its conversation cursor and active turn. The parent enforces the
thread limit, aggregates status, and receives durable child state changes.
Every child uses the same sandbox lease ID but separate Agent snapshot and
event stream.

External confirmations are first validated against the PostgreSQL event ledger,
which identifies the target thread. The corresponding thread wakeup is then
sent directly; clients do not need to supply routing data.

## Restate design considered

The strongest Restate design is not one months-long Workflow handler. It is an
actor-like split:

```text
SessionObject(session_id)              // short exclusive handlers
  Kick(max_event_sequence)
  TurnFinished(turn_id, result)
  Cancel/Close
  state: active invocation, queued sequences, pending actions, thread IDs

AgentTurn.Run(turn_id)                 // finite durable invocation
  model -> tools -> model loop
  write authoritative results
  callback SessionObject.TurnFinished

ThreadObject(thread_id)                // same pattern per subagent
```

This is a valid Stateful Agent architecture:

- keyed objects serialize control changes;
- a turn invocation is independently cancelable;
- new messages reach a short control handler rather than blocking behind the
  Agent loop;
- signals and awakeables handle external responses;
- state survives service restarts.

It is not selected for this project for four concrete reasons.

1. **It does not replace PostgreSQL.** Managed Agents exposes relational
   resources, cursor pagination, filters, an immutable public event history,
   audit records, and cross-resource lookups. Restate state is keyed K/V and is
   mutable only through its owning object. Keeping the public contract in
   PostgreSQL means database side effects still need stable IDs and conditional
   writes. A Restate-fronted admission handler can avoid an explicit outbox, but
   it still has to make a PostgreSQL commit idempotent across the interval
   before Restate records the durable-step result.
2. **Scheduled deployments become our scheduler.** Restate provides durable
   timers and delayed calls but explicitly does not include native cron. Its
   official recipe implements a Cron Virtual Object and leaves time zones and
   overlapping/failing-run policy to the application. That is exactly the
   general infrastructure this project is trying not to hand-roll.
3. **The long-lived shape must be decomposed for upgrades.** Restate pins an
   invocation to an immutable deployment and recommends avoiding handlers that
   last days or months. Splitting at every turn and client-action boundary
   solves this, but it makes Session lifecycle, callback fencing, and aggregate
   multiagent state application code. Temporal directly supports long-lived
   Workflow histories, Continue-As-New, child lifecycles, and Worker
   Versioning.
4. **Go adoption risk is currently higher.** Restate Server is a credible
   project and its Go SDK has now made a 1.0 compatibility commitment, but that
   1.0 release is only weeks old at this decision date. Temporal's Go SDK and
   production evidence for a cloud coding Agent control plane are much more
   established.

Restate wins on local simplicity, can make admission itself a durable handler
instead of using an explicit outbox relay, and offers a cleaner keyed-actor
programming model for products whose main surface is independent chat Sessions.
Those advantages are real, but they do not outweigh the schedule, lifecycle,
and maturity differences for the complete Managed Agents API.

## No component solves external side-effect ambiguity

Temporal and Restate both durably remember a completed step. Neither can
atomically commit an arbitrary third-party side effect and its own durable
acknowledgement when that third party offers no idempotency or transaction
protocol.

Every side-effecting tool therefore receives a stable operation ID and is
classified as one of:

- provider-idempotent;
- safely retryable read;
- recoverable by status lookup;
- compensatable;
- non-idempotent and therefore `ambiguous` after an uncertain failure.

The existing tool journal remains part of the target architecture for this
reason. It must not grow into a scheduler.

## Decision and revisit triggers

Adopt Temporal with the PostgreSQL-first admission path described above.
Proceed with the first vertical slice only after its design includes:

- transactional event/outbox admission;
- Signal-With-Start wakeups;
- sequence-based duplicate and gap handling;
- interrupt-after-commit cancellation;
- explicit public retry projection;
- Continue-As-New and Worker Versioning tests.

Re-evaluate Restate if at least one of these becomes true:

- Restate ships native schedules covering time zones, jitter, pause, overlap,
  and run inspection;
- its Go SDK has a longer production record for comparable multiagent or coding
  Agent control planes;
- a measured prototype proves that the Virtual Object design removes enough
  application lifecycle code to offset the missing schedule and deployment
  features;
- self-hosting Temporal, rather than using Temporal Cloud, becomes a hard
  product requirement and its operational cost dominates the system.

## Primary sources

Claude Managed Agents:

- [Start a Session](https://platform.claude.com/docs/en/managed-agents/sessions)
- [Session operations](https://platform.claude.com/docs/en/managed-agents/session-operations)
- [Session event stream](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)
- [Multiagent orchestration](https://platform.claude.com/docs/en/managed-agents/multiagent-orchestration)
- [Scheduled deployments](https://platform.claude.com/docs/en/managed-agents/scheduled-deployments)
- [Webhooks](https://platform.claude.com/docs/en/managed-agents/webhooks)
- [Self-hosted sandboxes](https://platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes)

Temporal:

- [Go Workflow message passing](https://docs.temporal.io/develop/go/workflows/message-passing)
- [Continue-As-New](https://docs.temporal.io/develop/go/workflows/continue-as-new)
- [Child Workflows](https://docs.temporal.io/develop/go/workflows/child-workflows)
- [Worker Versioning](https://docs.temporal.io/worker-versioning)
- [Schedules](https://docs.temporal.io/schedule)
- [Replit Agent case study](https://temporal.io/resources/case-studies/replit-uses-temporal-to-power-replit-agent-reliably-at-scale)

Restate:

- [Durable Sessions](https://docs.restate.dev/ai/patterns/sessions)
- [Interrupt and invocation control](https://www.restate.dev/blog/a-remote-control-for-your-agents)
- [Go Services, Virtual Objects, and Workflows](https://docs.restate.dev/develop/go/services)
- [Go external events](https://docs.restate.dev/develop/go/external-events)
- [Service communication and cancellation](https://docs.restate.dev/develop/go/service-communication)
- [Cron guide](https://docs.restate.dev/guides/cron)
- [Versioning](https://docs.restate.dev/services/versioning)
- [Databases and Restate](https://docs.restate.dev/guides/databases)
- [Go SDK 1.0 release](https://github.com/restatedev/sdk-go/releases/tag/v1.0.0)
