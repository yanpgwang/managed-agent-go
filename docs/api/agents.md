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

A successful create returns `200` and version `1`.

## Get and list

```http
GET /v1/agents/{id}
GET /v1/agents
GET /v1/agents/{id}/versions
```

`GET /v1/agents/{id}` returns the latest version. The versions route returns all
stored versions in a `data` array.

Agent list pagination and filtering are not implemented. The list response
contains `next_page: null`.

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

Some nested validation and full upstream response semantics remain partial.
