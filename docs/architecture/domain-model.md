---
title: Domain model
---

# Domain model

The public API is resource-oriented, while execution uses an additional
internal run model. That distinction is central to the architecture.

```mermaid
erDiagram
  AGENT ||--o{ AGENT_VERSION : has
  AGENT_VERSION ||--o{ SESSION : snapshots
  ENVIRONMENT ||--o{ SESSION : hosts
  SESSION ||--o{ EVENT : records
  SESSION ||--o{ SESSION_RUN : schedules
  SESSION_RUN }o--o{ EVENT : triggered_by
```

## Agent and agent version

An agent defines model configuration, system prompt, tools, MCP server
references, skills, metadata, and optional multiagent configuration.

The logical identity is the agent `id`; each material update produces a new
integer `version`. A conditional update may send the expected `version` to
detect concurrent changes. Archiving is idempotent, creates no new version, and
makes the agent read-only.

## Environment

An environment is a named configuration record selected when creating a
session. The current `cloud` record routes to the in-process runtime.
`self_hosted` records can be stored but sessions against them are explicitly
unsupported.

An environment cannot be deleted while a session references it. Archiving
prevents it from being selected by new sessions without invalidating existing
references.

## Session

A session is the long-lived public container for conversation and execution
state. At creation, it resolves:

1. the latest or explicitly pinned agent version;
2. optional session-local overrides;
3. the selected environment.

The resulting agent snapshot is stored with the session. Later agent updates or
archival cannot mutate it.

The public session status is a projection:

- `idle` — waiting for input or a required client action;
- `running` — accepted work is executing or queued;
- `rescheduling` — reserved for automatic retry behavior;
- `terminated` — terminal failure or completion.

The current implementation does not retry, so runtime failures project directly
to `terminated`.

## Event

Events are the public, append-only session history. On the wire each event is a
flat tagged union:

```json
{
  "id": "sevt_...",
  "type": "user.message",
  "content": [{"type": "text", "text": "hello"}],
  "processed_at": null
}
```

Internally, each event also has a session-local sequence number used for stable
ordering and cursors. That sequence is never exposed.

Client input, agent output, status changes, errors, and session updates all use
the same log. This gives clients one replayable history rather than separate
message, tool-call, and execution-status stores.

## Session run

A session run is internal execution bookkeeping, not a public Managed Agents
resource. It records a logical admitted unit of work and its trigger event IDs.

```text
queued → running → completed
                 ↘ failed
```

Runs allow accepted input to survive process restart. Startup requeues a run
that was interrupted while `running`, which gives at-least-once recovery.
Attempts, leases, fencing, and side-effect journals are not implemented yet.

## Why events are not runs

Events describe observable session history; runs describe how this server
attempts to produce that history. A future distributed executor may make
several attempts for one logical run without exposing each attempt as a new
public API concept.
