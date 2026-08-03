---
title: Events and streaming
---

# Events and streaming

Events are flat tagged-union objects. The `type` field selects the remaining
shape; persisted events receive an `id` and `processed_at`.

## Send events

`POST /v1/sessions/{id}/events`

```json
{
  "events": [{
    "type": "user.message",
    "content": [{"type": "text", "text": "Inspect the failure"}]
  }]
}
```

The list must be non-empty. Clients cannot provide `id` or `processed_at`, and
server-emitted event types are rejected.

The PostgreSQL/Temporal control plane currently accepts:

| Event | Current behavior |
| --- | --- |
| `user.message` | Starts a model turn |
| `user.interrupt` | Cancels the active turn, or is acknowledged as an idle no-op; `session_thread_id` is not supported |
| `user.custom_tool_result` | Supplies a result for a pending custom tool call |
| `user.tool_result` | Supplies a client-executed built-in result for a `self_hosted` environment |
| `user.tool_confirmation` | Allows or denies a pending `always_ask` built-in |
| `user.define_outcome` | Starts outcome work and independent evaluation/revision cycles |
| `system.message` | Text-only companion context; must be the final event immediately after a message or tool result |

`user.tool_result` is rejected unless it resolves a pending self-hosted
`agent.tool_use`. Targeted multi-agent interrupt shapes return
`422 unsupported_error`.

Content blocks are validated as closed tagged unions. Images accept `base64`
and `url` sources; documents accept `base64`, `text`, and `url` sources, with
`text` sources requiring `media_type: text/plain`. File sources return
`422 unsupported_error` until the Files API is implemented. Tool-result search
blocks require `source`, `title`, `citations.enabled`, and an array of text
blocks. Unknown fields are rejected at every nested level. Text outcome rubrics
are limited to 262,144 characters.

An interrupt is first committed to PostgreSQL and then delivered to the
Session Workflow as a metadata-only wakeup. An interrupt that commits before
turn completion wins that ordering point: the turn ends with exactly one
`session.status_idle` whose stop reason is `end_turn`. If completion commits
first, a later interrupt is an idle control event. A batch may place a new
`user.message` after `user.interrupt` to redirect the Session into another turn.

The response echoes only the submitted events:

```json
{"data": []}
```

Status and agent output are asynchronous and appear in list/stream results.

## List events

`GET /v1/sessions/{id}/events`

Supported query parameters:

| Parameter | Meaning |
| --- | --- |
| `limit` | Page size, `1`–`1000`; default `100`. Values above `1000` return a validation error. |
| `order` | `asc` or `desc`; default `asc` |
| `page` | Opaque forward cursor |
| `types[]` | Repeatable event type filter |
| `created_at[gt\|gte\|lt\|lte]` | RFC 3339 bounds currently applied to `processed_at` |

```json
{
  "data": [{
    "id": "sevt_...",
    "type": "agent.message",
    "content": [{"type": "text", "text": "Done"}],
    "processed_at": "2026-07-27T00:00:01Z"
  }],
  "next_page": null
}
```

Ordering and cursors currently use the internal session sequence, even for
queries whose public name is `created_at`. Exact `processed_at` null/tie
semantics remain incomplete.

## Stream events

`GET /v1/sessions/{id}/events/stream`

The endpoint returns `text/event-stream`. Persisted frames use their event type
as the SSE discriminator:

```text
event: agent.message
data: {"id":"sevt_...","type":"agent.message","content":[...],"processed_at":"..."}
```

The stream starts after the latest committed event at subscription time. It does
not replay earlier history and does not implement `Last-Event-ID`.

For reconnect without gaps:

1. open a new stream;
2. list persisted history while the stream is open;
3. merge both sources and deduplicate by event `id`.

An active stream receives `session.deleted` and then EOF when its session is
deleted.

## Live message previews

Opt in to ephemeral assistant text:

```http
GET /v1/sessions/{id}/events/stream?event_deltas[]=agent.message
```

The stream may first emit:

```text
event: event_start
data: {"type":"event_start","event":{"type":"agent.message","id":"sevt_..."}}

event: event_delta
data: {"type":"event_delta","event_id":"sevt_...","delta":{"type":"content_delta","index":0,"content":{"type":"text","text":"partial"}}}
```

The preview and eventual persisted `agent.message` share the same event ID.
Preview frames:

- are delivered only to opted-in subscribers;
- are never written to the event log;
- never appear in list results;
- may end without an authoritative event if generation or the process fails.

If generation is interrupted after preview delivery, the terminal
`span.model_request_end` closes the preview even when no buffered
`agent.message` is produced.

Model request span IDs are allocated before provider execution. The durable
`span.model_request_start` is appended before its provider call can publish a
preview; the authoritative message and correlated `span.model_request_end`
follow when that model round completes. If a preview arrives before its
persisted-event wakeup, the stream reconciles its PostgreSQL cursor before
forwarding the first preview frame.

An outcome evaluation durably publishes `span.outcome_evaluation_start` before
the grader runs and emits periodic `span.outcome_evaluation_ongoing` events
while it remains active. Its terminal `span.outcome_evaluation_end` references
the start event. This correlation is preserved when an active grader is
interrupted. If interruption happens before any evaluation start can be
published, the documented `outcome_evaluation_start_id` is the empty string.
Completed `needs_revision` evaluation pairs remain in history and the
interrupt end uses the next zero-based iteration.

`agent.thinking` is accepted as an opt-in value but no thinking previews are
currently emitted.

## Backpressure

NATS Core carries best-effort wakeups and previews across API/worker processes;
PostgreSQL remains authoritative. Each subscriber periodically reconciles its
durable PostgreSQL cursor, so a lost wakeup delays a persisted event but does
not lose it. The output buffer is bounded: a slow subscriber is disconnected
and should reconnect using the open-stream-then-list procedure above. Preview
frames are ephemeral and can be lost.
