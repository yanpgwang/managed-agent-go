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

The current runtime accepts sessions against `cloud` environment records.
Creating a session against `self_hosted` returns `422`; no remote worker
protocol is implemented yet.

## Get and list

```http
GET /v1/environments/{id}
GET /v1/environments
```

The list response is:

```json
{"data": []}
```

Environment pagination and filtering are not implemented.

## Archive

`POST /v1/environments/{id}/archive`

Archive is idempotent. Archived environments cannot be used for new sessions,
but existing session references remain intact.

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
