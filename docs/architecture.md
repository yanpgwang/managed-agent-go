---
title: Architecture overview
slug: /architecture
---

# Architecture overview

`managed-agent-go` is one codebase with explicit API and worker process roles.
PostgreSQL owns public resources and events, Temporal owns in-flight
orchestration, and NATS Core carries ephemeral wakeups and previews. The local
Compose stack runs those roles separately; they can also be packaged in one
deployment for development.

The selected stack is Temporal orchestration, PostgreSQL, NATS Core,
S3-compatible File and Skill archive storage, and replaceable sandbox providers.
Provider sandbox bindings and File/Skill lifecycle intents are persisted in
PostgreSQL; File bytes and immutable custom Skill archives live in object
storage when those optional surfaces are enabled.

```mermaid
flowchart LR
  Client["Managed Agents client"] --> API["HTTP API"]
  API --> PG[("PostgreSQL resources<br/>events + outbox")]
  API --> Objects[("S3-compatible<br/>Files + Skill archives")]
  API --> Temporal["Temporal frontend"]
  API <--> NATS["NATS Core"]
  Relay["Outbox relay"] --> PG
  Relay --> Temporal
  Temporal --> Worker["Temporal worker"]
  Worker --> PG
  Worker --> Model["Messages API"]
  Worker --> Sandbox["Sandbox provider"]
  Worker --> NATS
  NATS --> API
```

## Design principles

### The server owns history

The event log is the source of truth for public session history. It is not the
lossless provider transcript and must not be used to reconstruct provider-native
context. Every event belongs to exactly one Session Thread. A Session-wide
sequence preserves total order across child activity and explicit primary
cross-posts, while Session history and the primary workflow read only the
primary Thread ledger. Two different public orderings are read from the event
ledger:

- **Public event history** (`GET .../events`, list, and the live SSE stream) is
  the immutable receipt/commit sequence. It never reorders or hides events.
- **Model-facing conversation order** is reconstructed per turn from causality,
  not from raw commit order. PostgreSQL tags committed output with the trigger
  event ID; prior processed triggers are replayed with their exact output before
  the current trigger. A turn never sees a later message queued while it was
  still running.

Each Thread continues model conversations from its own lossless Provider
Transcript. Every transcript row has a database foreign key to its public
trigger event, and Thread ownership is derived from that event rather than
duplicated, so private context cannot drift between Threads. The causal
public-event projection remains only as a compatibility fallback for histories
created before transcript support. Immutable Context Snapshots remain follow-up
work. This separation is required for native server-tool blocks, citations,
compaction, and large results. See
[Storage, context, and connected tools](architecture/storage-context-and-tools.md).
The model endpoint performs inference; it does not own session state.

### Wire and domain models are separate

`internal/httpapi` owns request decoding and response encoding.
`internal/domain` models persisted resources and execution facts. Mapping is
explicit in both directions so internal sequence numbers, run states, and
storage details cannot leak into the compatibility wire.

### Public history and execution bookkeeping are different things

Session events are the public append-only history. Temporal Workflow history,
turn attempts, and tool steps are private execution facts. Keeping those models
separate lets orchestration recover without leaking Temporal or retry details
onto the public API.

### Interfaces sit at expensive boundaries

The model client, agent runtime, and sandbox provider are interfaces because
they cross process, trust, or infrastructure boundaries. Domain entities stay
concrete. This keeps the code easy to follow without locking the project to one
model vendor, sandbox backend, or worker topology.

## Package boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/managed-agent` | Composition root, configuration, process lifecycle |
| `internal/httpapi` | HTTP routes, strict validation, DTO mapping, SSE |
| `internal/app` | Shared resource validation and transport-neutral use-case types |
| `internal/blob` | S3-compatible storage for public File bytes and immutable Skill archives |
| `internal/controlplane` | PostgreSQL-backed public Session/Event use cases |
| `internal/domain` | Resource, event, message, tool, and run semantics |
| `internal/pg` | PostgreSQL repositories, ledger, outbox, and tool journal |
| `internal/temporal` | Session Workflow, Activities, worker, and relay |
| `internal/live` | NATS wakeups/previews plus PostgreSQL cursor reconciliation |
| `internal/agentruntime` | Reusable model, message, and tool execution primitives |
| `internal/model` | Offline and Messages API model clients |
| `internal/sandbox` | Provider registry, lifecycle contract, and local/remote adapters |

The dependency direction points inward: transport and infrastructure depend on
application/domain semantics, while the domain has no HTTP, SQL, model-client,
or sandbox dependencies.

## Durable write path

Submitting input is not “write an event, then call Temporal.” PostgreSQL commits
the client events, `session.status_running`, Session projection, and one
coalescible outbox wakeup in one transaction. A crash therefore cannot leave
accepted input without a durable path to orchestration. A direct
Signal-With-Start is only a latency optimization; the relay is the correctness
path.

Model and tool calls happen as Temporal Activities outside SQL transactions.
Before each provider call, PostgreSQL durably appends its model-request start;
completed intermediate model/tool rounds are appended idempotently before a
later provider call can start. Turn completion atomically commits the remaining
output, provider transcript, trigger `processed_at`, final Session status, and
optional attempt finalization. PostgreSQL emits best-effort NATS wakeups after
each commit; SSE subscribers read the committed rows by sequence.

Physical session deletion is a small saga: PostgreSQL first marks the row as
deleting under the admission lock, the API terminates its Session Workflow, a
short Temporal Workflow durably releases the provider sandbox and binding, and
only then does PostgreSQL remove the projection. The binding foreign key blocks
deletion from discarding the last reference to a live sandbox. Workers scan the
durable deletion fence and resume this sequence if the API process exits before
cleanup or finalization completes.

Live text deltas are the exception: they are explicitly ephemeral previews,
delivered only to opted-in SSE subscribers. They are never returned by event
history.

## Scaling boundaries

API replicas are stateless around PostgreSQL and NATS. Temporal assigns Workflow
and Activity tasks to workers; the PostgreSQL tool journal records the
side-effect ambiguity boundary. Core NATS is at-most-once, so streams
periodically reconcile their durable cursor and never treat a wakeup as data.
Worker Versioning and promotion of remote sandbox adapters through repeatable
live conformance are still required before production rolling deployments.

Workflow changes use Temporal version markers where replay compatibility
requires them, and `internal/temporal` carries an offline `worker.WorkflowReplayer`
harness that replays synthetic pre-change histories against the current code.
The harness covers every recorded prefix of the ordered turn-level version
gates and both sides of the Session Workflow durable-interrupt gate. A version
marker is scoped to one Workflow execution, so it can only keep a code branch
consistent inside that execution. `SessionWorkflow` continues-as-new and
PostgreSQL outlives every execution, so any semantic that must agree with
already-published events — such as which tool-result variant answers a parked
tool call — is derived from the durable event rather than from a version gate.
Production rolling deployments still need Worker Versioning.

## Current implementation boundaries

The strongest current risks are semantic rather than structural:

1. Model requests are bounded by a conservative server-owned token estimate and
   extractive compaction policy. Provider-exact tokenizers, per-model context
   profiles, and durable Context Snapshot resources are not yet implemented.
2. Sandboxes are session-scoped and durably bound to opaque provider IDs.
   Restart reattachment and deletion cleanup are implemented for local and
   Docker on the same host/daemon. Provisioning intent closes the
   create-before-binding crash window and workers autonomously resume fenced
   deletions. Provider-aware routing for heterogeneous workers, quotas, and
   eviction are not implemented.
3. Worker Versioning, observability, authentication, large-payload offload, and
   production manifests remain open.
4. Provider Transcript, native Web Search/Fetch, sandbox result
   materialization, and unauthenticated MCP tools are implemented. Context
   Snapshots, provider-round records, deployment-managed MCP authentication, and
   reference-only Temporal payloads remain open.

Current API support is tracked in the [compatibility matrix](compatibility.md);
planned capability work is kept in the [roadmap](roadmap.md).
