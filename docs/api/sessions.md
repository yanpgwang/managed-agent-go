---
title: Sessions
---

# Sessions

Sessions bind an immutable agent snapshot to an environment and own an
append-only event history.

## Create

`POST /v1/sessions`

The `agent` field supports three forms.

Latest version:

```json
{
  "agent": "agent_...",
  "environment_id": "env_...",
  "title": "Investigation"
}
```

Pinned version:

```json
{
  "agent": {"type": "agent", "id": "agent_...", "version": 2},
  "environment_id": "env_..."
}
```

Session-local overrides:

```json
{
  "agent": {
    "type": "agent_with_overrides",
    "id": "agent_...",
    "version": 2,
    "system": "Focus on production incidents.",
    "tools": []
  },
  "environment_id": "env_..."
}
```

Overrides replace model, system, tools, MCP servers, or skills for this session
only. They do not mutate or renumber the agent.

Optional `initial_events` may contain up to 50 `user.message` or
`user.define_outcome` objects. A non-empty list starts execution immediately.
Non-empty `resources` and `vault_ids` are currently unsupported.

## Get and update

```http
GET /v1/sessions/{id}
POST /v1/sessions/{id}
```

Only title update is implemented:

```json
{"title": "New title"}
```

A changed title and its `session.updated` event commit together. An omitted or
unchanged title is a no-op.

## List

`GET /v1/sessions`

Supported query parameters:

| Parameter | Meaning |
| --- | --- |
| `limit` | Page size, `1`–`1000`; default `100`. Values above `1000` return a validation error. |
| `order` | `asc` or `desc`; default `desc` |
| `page` | Opaque next or previous cursor |
| `agent_id` | Match agent ID |
| `agent_version` | Match version; requires `agent_id` |
| `statuses[]` | Repeatable public status filter |
| `include_archived` | `true` or `false` |
| `created_at[gt\|gte\|lt\|lte]` | RFC 3339 timestamp bounds |
| `deployment_id` | Accepted; current records do not match deployments |
| `memory_store_id` | Accepted; current records do not match memory stores |

The response includes both directions:

```json
{
  "data": [],
  "next_page": null,
  "prev_page": null
}
```

## Archive and delete

```http
POST /v1/sessions/{id}/archive
DELETE /v1/sessions/{id}
```

A running session cannot be archived or deleted and returns `409`. A
single-process `user.interrupt` can cancel the active run; interrupt routing to
a specific multi-agent thread and cross-process delivery are not supported.

Delete removes the session and persisted history, sends a final
`session.deleted` event to active subscribers, and closes their streams:

```json
{"id": "session_...", "type": "session_deleted"}
```

## Response notes

The response embeds the resolved agent snapshot and includes `resources`,
`vault_ids`, `outcome_evaluations`, `stats`, `usage`, and `deployment_id`.
Several of those fields are currently empty or zero placeholders. SDK decoding
does not imply that every upstream field is implemented; see
[Claude API coverage](../compatibility.md).
