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
only. They do not mutate or renumber the agent. A model override may change the
model ID or speed; effort remains an Agent-level setting and a session override
does not replace it.

Optional `initial_events` may contain up to 50 `user.message` or
`user.define_outcome` objects. A non-empty list starts execution immediately.
The optional `title`, `metadata`, `initial_events`, `resources`, and `vault_ids`
fields must use their documented non-null shapes when present; omission supplies
the empty/default value.
Non-empty `resources` and `vault_ids` are currently unsupported by Mango at
creation time. The `vault_ids` rejection is a Mango limitation: the official
Create Session API accepts vault IDs.

## Get and update

```http
GET /v1/sessions/{id}
POST /v1/sessions/{id}
```

The update body accepts `agent`, `metadata`, and `title`:

```json
{
  "title": "New title",
  "metadata": {"owner": "sre", "stale": null},
  "agent": {
    "tools": [
      {"type": "agent_toolset_20260401"},
      {"type": "mcp_toolset", "mcp_server_name": "linear"}
    ],
    "mcp_servers": [
      {"type": "url", "name": "linear", "url": "https://mcp.linear.app/sse"}
    ]
  }
}
```

- `metadata` is a per-key patch: a string upserts the key, `null` deletes it,
  and omitting the field preserves the whole bag.
- `title` may be omitted or set to a string; `null` is not a no-op update.
- `agent` updates only `tools` and `mcp_servers`, as a full replacement: the
  array you send becomes the new value, `[]` clears, and omitting preserves.
  `model`, `system`, and `skills` are fixed for the session's lifetime and are
  rejected; set them with `agent_with_overrides` at create time instead.
- An `agent` update is session-local. It never renumbers or mutates the agent
  resource, and it applies from the next turn.
- **An `agent` update requires an `idle` session.** A request that arrives while
  a turn is in flight returns `409`; send an untargeted `user.interrupt` first.
  `title` and `metadata` carry no such precondition.
- `vault_ids` is rejected on update, matching the official Update Session API.

Changed fields and their `session.updated` event commit together. The event
carries only the fields the request actually changed; a request that changes
nothing emits no event.

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

A running session cannot be archived or deleted and returns `409`. An
untargeted `user.interrupt` durably cancels the active model or tool Activity
across API and worker processes so the Session can return to `idle`. Interrupt
routing to a specific multi-agent thread is not supported.

Delete removes the session and persisted history, sends a final
`session.deleted` event to active subscribers, and closes their streams:

```json
{"id": "session_...", "type": "session_deleted"}
```

## Response notes

The response embeds the resolved agent snapshot and includes `resources`,
`vault_ids`, `outcome_evaluations`, `stats`, `usage`, and `deployment_id`.
`stats` and `usage` are cumulative live projections, and
`outcome_evaluations` reflects each admitted outcome. Non-empty resources and
vaults are rejected at create time, so their required response arrays are
truthfully empty. Create-time vault rejection is a Mango limitation; update-time
vault rejection matches the official API. `deployment_id` is null because
deployment-created sessions are not implemented.
See [Claude API coverage](../compatibility.md).
