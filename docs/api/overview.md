---
title: API overview
slug: /api
---

# API overview

The server implements a practical subset of the Claude Managed Agents HTTP API
under `/v1`.

:::info[Supported surface]

This reference documents repository behavior. [Claude API
coverage](../compatibility.md) summarizes which integration workflows are
supported, limited, or not supported. The [core API conformance
matrix](core-conformance.md) inventories each SDK-visible operation and its
current test evidence.

:::

## Endpoints

| Resource | Endpoints |
| --- | --- |
| Agents | `POST/GET /v1/agents`, `GET/POST /v1/agents/{id}`, versions, archive |
| Environments | `POST/GET /v1/environments`, get, archive, delete; update is not implemented |
| Sessions | `POST/GET /v1/sessions`, get, update, archive, delete |
| Events | `POST/GET /v1/sessions/{id}/events`, SSE stream |
| Operations | `GET /healthz`, `GET /readyz`, `GET /openapi.yaml` |

Resource-specific request shapes are covered in:

- [Agents](agents.md)
- [Environments](environments.md)
- [Sessions](sessions.md)
- [Events and streaming](events.md)
- [Core API conformance matrix](core-conformance.md)

## Headers

The default development server accepts requests without compatibility headers.
Run with `-strict` to require them:

```http
x-api-key: any-non-empty-value
anthropic-version: 2023-06-01
anthropic-beta: managed-agents-2026-04-01
content-type: application/json
```

`authorization` may replace `x-api-key`. Strict mode currently validates header
presence and version/beta values; it is not a production authentication system.

Every response includes a `request-id` header. JSON request bodies are limited
to 32 MiB and unknown top-level fields are rejected.

## Errors

Errors use a Claude-compatible envelope:

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "name is required"
  },
  "request_id": "req_..."
}
```

| HTTP status | Current error type |
| --- | --- |
| `400` | `invalid_request_error` |
| `401` | `authentication_error` |
| `404` | `not_found_error` |
| `409` | `conflict_error` |
| `413` | `request_too_large` |
| `422` | `invalid_request_error` |
| `500` | `api_error` |

These mappings are Mango's public contract for the supported API subset. See
the [compatibility matrix](../compatibility.md) for parity limits.

## Pagination

Top-level Agent, Environment, and Session lists, Agent version histories, and
Event lists use opaque `page` tokens. A cursor is bound to its resource and
normalized filters; Agent version cursors are additionally bound to the Agent
ID, and session cursors are bound to sort order. Reusing a cursor outside its
scope returns `400`.

List responses use `data` and nullable cursor fields:

```json
{
  "data": [],
  "next_page": null
}
```

Session lists also include `prev_page`. Agent, Agent Version, and Environment
lists are forward-only and include `next_page`.

## OpenAPI

The running server exposes `/openapi.yaml`, sourced from
`internal/httpapi/openapi.yaml`. It is currently an endpoint inventory, not a
complete generated client contract: schemas, examples, parameter definitions,
and the full event union still need to be added.

For now, use these guides and the API coverage page when integrating.
