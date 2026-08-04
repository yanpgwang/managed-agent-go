---
title: Agents
---

# Agents

Agents are versioned definitions. Sessions resolve an agent version once and
store an immutable snapshot.

## Create an agent

`POST /v1/agents`

```json
{
  "name": "Repository assistant",
  "model": "claude-model-id",
  "system": "Work carefully and explain changes.",
  "description": "Helps maintain a repository",
  "tools": [],
  "mcp_servers": [],
  "skills": [],
  "metadata": {"team": "platform"}
}
```

Required fields:

- `name`: non-empty string;
- `model`: model ID string or an object with a non-empty `id`.

The object model form also preserves supported `effort` and `speed` values.
`effort` accepts either a level string such as `"high"` or the tagged object
`{"type":"high"}`; responses use the tagged object form.
`multiagent` may be an object, but the server currently stores it opaquely and
does not resolve or execute its topology.
Optional collection and metadata fields may be omitted or supplied with their
documented array/object shape; explicit `null` is not a create-time default.

Custom Skills use the documented tagged reference:

```json
{"type": "custom", "skill_id": "skill_...", "version": "latest"}
```

`version` may be omitted or set to `latest`. Mango validates the Skill and
stores the concrete immutable Version in the Agent response and version
history. Updating unrelated Agent fields preserves that pin; replacing
`skills` resolves the replacement list again. The latest active Agent Version
also holds a relational retention pin, so its Skill archive cannot be deleted
until the list is replaced or the Agent is archived. Anthropic-managed
references return `422` because Mango does not mirror their archives.

A successful create returns `200` and version `1`.

## Get and list

```http
GET /v1/agents/{id}
GET /v1/agents
GET /v1/agents/{id}/versions
```

`GET /v1/agents/{id}` returns the latest version. The versions route returns
stored versions in ascending version order. It supports the documented `limit`
and opaque `page` parameters; `limit` defaults to `20` and has a maximum of
`100`. Its response contains `data` and nullable `next_page` fields.

The Agent list supports the documented `created_at[gte]`, `created_at[lte]`,
`include_archived`, `limit`, and `page` parameters. `limit` defaults to `20` and
has a maximum of `100`. Results contain only the latest version of each Agent,
are ordered newest-first by a stable `(created_at, id)` key, and use a
forward-only opaque `next_page` cursor.

## Update

`POST /v1/agents/{id}`

Updates create a new version only when a material field changes:

```json
{
  "version": 1,
  "system": "Use short answers.",
  "metadata": {
    "team": "developer-experience",
    "obsolete_key": null
  }
}
```

When supplied, `version` is an optimistic concurrency check. A stale version
returns `409`.

Field behavior:

- omitted fields preserve the current value;
- `system` and `description` accept `null` to clear;
- `tools`, `mcp_servers`, and `skills` replace the whole list and accept
  `null` to clear;
- `multiagent` replaces the object and accepts `null` to clear;
- metadata keys patch the map, and a `null` value removes a key;
- `name`, `version`, and the `metadata` object itself cannot be `null`;
- `model` may be replaced but cannot be `null`.

An update with no material change returns the current version.

## Archive

`POST /v1/agents/{id}/archive`

Archive is idempotent and does not create a new version. An archived agent is
read-only and cannot be selected for a new session. Existing sessions keep
their stored snapshot.

## Response shape

```json
{
  "id": "agent_...",
  "type": "agent",
  "version": 1,
  "name": "Repository assistant",
  "model": {"id": "claude-model-id"},
  "system": "Work carefully and explain changes.",
  "description": "Helps maintain a repository",
  "tools": [],
  "mcp_servers": [],
  "skills": [],
  "multiagent": null,
  "metadata": {"team": "platform"},
  "created_at": "2026-07-27T00:00:00Z",
  "updated_at": "2026-07-27T00:00:00Z",
  "archived_at": null
}
```

Custom Skill references are validated and version-pinned, but Skill execution
and multi-agent topology execution remain outside the supported core. Accepted
Agent tool and MCP shapes are validated before a version is stored.
