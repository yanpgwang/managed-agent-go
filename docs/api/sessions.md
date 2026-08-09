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
model ID, speed, or inference geography; effort remains an Agent-level setting
and a session override does not replace it. Overrides also apply to `self`
copies in a coordinator roster. Independently referenced Agents are unaffected,
so a geography override that would make the coordinator disagree with one of
those pinned Agents is rejected.

For a coordinator, `session.agent.multiagent.agents` expands the Agent
resource's Version references into full immutable Agent definitions. The
definitions are captured with the Session, in roster order, rather than loaded
again when a child Thread starts. A `self` definition reflects the effective
Session overrides and every roster member omits its own `multiagent` field,
preserving the one-level topology. Existing Sessions keep those snapshots even
if a referenced Agent is later updated or archived.

The effective custom Skill list is revalidated when the Session is created.
Every omitted or `latest` value is replaced by a concrete immutable Version in
the returned `session.agent.skills` snapshot. PostgreSQL pins those Versions in
the same transaction as the Session; deleting a pinned Version is rejected
until the Session is physically deleted. The pin migration backfills concrete,
still-ready custom references from existing Session snapshots; former opaque
values remain readable but are not treated as executable references. Up to 500
Skills are accepted, subject to a 500 MB aggregate expanded-size limit and
unique runtime names. In Docker-backed cloud Sessions, pinned archives are
verified and exposed read-only at
`/workspace/skills/<name>/`. The model first receives every Skill name plus
descriptions bounded to one percent of the configured context window.
When it invokes the private `Skill` dispatcher, the runtime returns
`Launching skill: <name>` and injects the complete selected `SKILL.md`, prefixed
with its base directory, into the provider conversation. Referenced supporting
files remain on disk for ordinary `read` or `bash` access. Local, self-hosted,
and current remote-provider Sessions reject custom Skill execution.

Optional `initial_events` may contain up to 50 `user.message` or
`user.define_outcome` objects. A non-empty list starts execution immediately.
The optional `title`, `metadata`, `initial_events`, `resources`, and `vault_ids`
fields must use their documented non-null shapes when present; omission supplies
the empty/default value. `budget: null` explicitly selects no spend ceiling.
A non-null budget currently returns `422`: the API does not claim enforcement
until provider list cost can be aggregated durably across all Session Threads.
`resources` accepts File inputs and up to eight Memory Store inputs when the
corresponding Docker sandbox capability is configured:

```json
{
  "type": "file",
  "file_id": "file_...",
  "mount_path": "/reports/input.csv"
}
```

Each attachment creates an independent, downloadable Session-scoped File copy
and a read-only mount beneath `/mnt/session/uploads`. A Memory Store input uses
`type: "memory_store"`, `memory_store_id`, optional `instructions`, and
`read_write` or `read_only` access; it is mounted beneath `/mnt/memory` and can
only be attached at creation. GitHub repository, self-hosted Environment,
local-process, and current remote-provider resources return `422`.
`vault_ids` is an ordered list of active Vault references. The order is frozen
with the Session: for an MCP endpoint, the first Vault containing a matching
credential wins. Admission requires the Vault keyring to be configured and
rejects missing, archived, empty, or duplicate references. Updating
`vault_ids` after creation remains unsupported, matching the upstream contract.

## Get and update

```http
GET /v1/sessions/{id}
POST /v1/sessions/{id}
```

The update body accepts `agent`, `metadata`, `title`, and `budget`:

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
- `budget: null` is accepted as a no-op because this release has no configured
  spend ceiling. A non-null limit returns `422` rather than being ignored.

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
| `deployment_id` | Match Sessions created by the Deployment |
| `memory_store_id` | Match Sessions attached to the Memory Store |

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

The response embeds the resolved agent snapshot and includes nullable `budget`,
`resources`, `vault_ids`, `outcome_evaluations`, `stats`, `usage`, and
`deployment_id`.
`stats` and `usage` are cumulative live projections, and
`outcome_evaluations` reflects each admitted outcome. `resources` embeds active
File and Memory Store Resource objects. Ordered `vault_ids` are resolved at
creation; update-time vault replacement is rejected, matching the official API.
Until cost accounting is implemented, `budget`, `usage.list_cost`, and
`usage.server_tool_use` are explicit null values; `usage.active_seconds` is the
duration currently priced by the upstream contract.
`deployment_id` is null for direct Session creation and contains the parent
Deployment ID for Deployment-created Sessions.
See [Claude API coverage](../compatibility.md).
