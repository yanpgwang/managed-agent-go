---
title: Environments
---

# Environments

An environment is a named session execution configuration.

## Create

`POST /v1/environments`

```json
{
  "name": "local",
  "config": {"type": "cloud"}
}
```

`name` is required. If `config.type` is omitted, the stored type defaults to
`cloud`.

The runtime accepts `cloud` and `self_hosted` sessions. In `cloud`, enabled
built-in sandbox tools execute on the configured worker sandbox. In
`self_hosted`, the same `agent.tool_use` parks the Session with
`requires_action`; the client executes it and sends a correlated
`user.tool_result`. The server then resumes the same model loop without
executing the tool a second time.

## Get and list

```http
GET /v1/environments/{id}
GET /v1/environments
```

The list supports `include_archived`, `limit`, and the forward-only opaque
`page` cursor. Mango uses a local default limit of `100` and maximum of `1000`
because the public Environment list reference does not specify either bound.
The response is:

```json
{"data": [], "next_page": null}
```

## Archive

`POST /v1/environments/{id}/archive`

Archive is idempotent. Archived environments cannot be used for new sessions,
but existing session references remain intact.

## Update

`POST /v1/environments/{id}` is the only missing operation in the 21-operation
core HTTP inventory. Mango does not currently accept an Environment update
because cloud networking and package configuration are not yet enforced by the
runtime. Accepting and storing those fields without honoring them would create
a false compatibility claim.

## Delete

`DELETE /v1/environments/{id}`

An unreferenced environment can be deleted:

```json
{
  "id": "env_...",
  "deleted": true
}
```

Deleting an environment referenced by a session returns `409`.

## Response shape

```json
{
  "id": "env_...",
  "type": "environment",
  "name": "local",
  "config": {"type": "cloud"},
  "created_at": "2026-07-27T00:00:00Z",
  "updated_at": "2026-07-27T00:00:00Z",
  "archived_at": null
}
```

The current response does not yet include the official SDK's `description`,
`metadata`, and `scope` fields. The [core conformance
matrix](core-conformance.md) tracks that projection gap separately from route
presence.
