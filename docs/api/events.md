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

Accepted input types are:

| Event | Current behavior |
| --- | --- |
| `user.message` | Starts a model turn |
| `user.custom_tool_result` | Resumes a parked custom tool |
| `user.tool_confirmation` | Resumes one pending built-in tool confirmation; allow executes it and deny emits an error result |
| `user.tool_result` | Accepted; self-hosted worker flow is incomplete |
| `user.interrupt` | Cancels the active run in the current process; multi-agent targeting and cross-process delivery are not supported |
| `user.define_outcome` | Stored and validated |
| `system.message` | Stored and admitted as input |

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

The stream publishes new events only. It does not replay history and does not
implement `Last-Event-ID`.

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

`agent.thinking` is accepted as an opt-in value but no thinking previews are
currently emitted.

## Backpressure

The stream hub is in-process and bounded. A slow subscriber can be disconnected
and should reconnect using the open-stream-then-list procedure above. A
distributed fan-out/replay service is not implemented.
