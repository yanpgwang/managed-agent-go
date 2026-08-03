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
  "description": "Default analysis environment",
  "metadata": {"team": "data"},
  "config": {"type": "cloud"}
}
```

`name` is required. If `config.type` is omitted, the stored type defaults to
`cloud`. `description` and `metadata` are optional. `scope` accepts `account` or
`organization` for `self_hosted` environments and is rejected for `cloud`.

The current supported cloud policy is unrestricted networking with no requested
packages. It may be omitted or supplied explicitly. Limited networking and any
non-empty package list are rejected until the selected sandbox adapter can
enforce them; Mango does not persist unenforced policy as inert configuration.

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

`POST /v1/environments/{id}` updates `name`, `description`, metadata, explicit
self-hosted `scope`, and the Environment type. Metadata is patched per key;
`null` and empty string delete a key. Changing a self-hosted Environment to
`cloud` clears its inapplicable scope. Archived Environments are read-only.

Explicit unrestricted networking and empty package lists are accepted.
`limited` networking and non-empty package lists return `422` until the selected
sandbox adapter can enforce them. They are never accepted as inert stored
policy.

## Delete

`DELETE /v1/environments/{id}`

An unreferenced environment can be deleted:

```json
{
  "id": "env_...",
  "type": "environment_deleted"
}
```

Deleting an environment referenced by a session returns `409`.

## Response shape

```json
{
  "id": "env_...",
  "type": "environment",
  "name": "local",
  "description": "Default analysis environment",
  "metadata": {"team": "data"},
  "config": {
    "type": "cloud",
    "networking": {"type": "unrestricted"},
    "packages": {
      "type": "packages",
      "apt": [], "cargo": [], "gem": [], "go": [], "npm": [], "pip": []
    }
  },
  "created_at": "2026-07-27T00:00:00Z",
  "updated_at": "2026-07-27T00:00:00Z",
  "archived_at": null
}
```

The default cloud response includes the official SDK's resolved
unrestricted-network and empty-package defaults. Description, metadata, and
self-hosted scope persist across create, get, list, and archive. Requests for
limited networking or non-empty package sets remain unsupported until the
runtime can enforce them. The [core conformance matrix](core-conformance.md)
tracks that gap separately from route presence.
