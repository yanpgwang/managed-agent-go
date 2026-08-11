---
title: Environment Work
slug: /api/environment-work
---

# Environment Work

Environment Work is the activation and lease protocol used by Anthropic's
prebuilt Environment workers for `self_hosted` Environments. It is not a second
Agent runtime. The worker claims a Session, listens to the existing Session
event stream, executes `agent_toolset_20260401` tools in customer-hosted
infrastructure, and posts results through the existing `user.tool_result`
event.

Mango creates a Work item only when a self-hosted Session has runnable input.
The Work insert, public event admission, Session projection update, and Temporal
outbox wakeup commit in one PostgreSQL transaction. Further runnable input is
coalesced while the Session has a live Work item; input received after Stop
creates a new activation.

## Worker flow

```text
Poll -> Ack -> Heartbeat(NO_HEARTBEAT) -> Heartbeat(previous timestamp) -> Stop
 queued   starting             active                         stopping/stopped
```

- `Poll` tentatively claims the oldest available item. A stale unacknowledged
  claim may be reclaimed; `Anthropic-Worker-ID` contributes to queue stats.
- `Ack` removes the item from the queue and changes it from `queued` to
  `starting`.
- The first heartbeat uses `expected_last_heartbeat=NO_HEARTBEAT`. Every later
  heartbeat echoes the exact timestamp returned by the previous response. A
  mismatch returns `412` so a worker that lost its lease stops executing.
- Graceful Stop changes active Work to `stopping`; the next heartbeat tells the
  worker to cancel. Forced Stop immediately records `stopped`.

The API exposes Get, Update, List, Ack, Heartbeat, Poll, Stats, and Stop beneath:

```text
/v1/environments/{environment_id}/work
```

The official Go SDK `environments.NewWorkPoller` is exercised directly in the
repository compatibility suite. Stop returns `204 No Content`, matching the
hosted worker protocol and the SDK helper's response-body bypass. An empty Poll
returns an empty JSON object, which the current official Go helper recognizes
as a drained queue.

## Skills and Session state

The official `EnvironmentWorker` retrieves the Session's immutable Agent
snapshot and downloads its pinned custom Skill Versions into the worker
workspace before running tools. This path is independent of Mango's cloud
sandbox adapter capability: a cloud Session still requires a Skill-capable
adapter, while a self-hosted Session may use Skills whenever the Skills API and
object store are configured.

## Security boundary

The official worker sends an Environment key as a bearer credential for Work,
Session, event, and Skill requests. Mango strict mode currently checks only
that an API key or bearer header is present; it does not issue Environment keys
or scope them to one Environment. Work `secret` is therefore returned as
`null`. Put authentication, tenant isolation, and environment-scoped
authorization in front of this surface before production exposure.

See [API compatibility](../compatibility.md) for the current support boundary.
