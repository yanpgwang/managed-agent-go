---
title: API overview
slug: /api
---

# API overview

The server implements a practical subset of the Claude Managed Agents and
Files HTTP APIs under `/v1`.

:::info

This reference documents repository behavior. See
[Compatibility & conformance](../compatibility.md) for exactly what is
supported, limited, or not supported — and for the per-operation conformance
matrices.

:::

## Endpoints

| Resource | Endpoints |
| --- | --- |
| Agents | `POST/GET /v1/agents`, `GET/POST /v1/agents/{id}`, versions, archive |
| Environments | `POST/GET /v1/environments`, get, update, archive, delete |
| Sessions | `POST/GET /v1/sessions`, get, update, archive, delete |
| Events | `POST/GET /v1/sessions/{id}/events`, SSE stream |
| Files | `POST/GET /v1/files`, metadata, content download, delete |
| Skills | Create/list/get/delete custom Skills and immutable Versions; download Version zip archives |
| Memory | Create/list/get/update/archive/delete Stores; create/list/get/update/delete Memories; get/list/redact immutable Versions |
| Vaults | Create/list/get/update/archive/delete Vaults; create/list/get/update/archive/delete encrypted Credentials; validate MCP OAuth Credentials |
| Deployments | Create/list/get/update/archive/pause/unpause/run under `/v1/deployments`; get/list immutable records under `/v1/deployment_runs` |
| Session Resources | Add, list, get, update contract, and delete under `/v1/sessions/{id}/resources` |
| Operations | `GET /healthz`, `GET /readyz`, `GET /openapi.yaml` |

Resource-specific request shapes are covered in:

- [Agents](agents.md)
- [Environments](environments.md)
- [Sessions](sessions.md)
- [Events and streaming](events.md)
- [Deployments and Deployment Runs](deployments.md)

## Headers

The default development server accepts requests without compatibility headers.
Run with `-strict` to require them:

```http
x-api-key: any-non-empty-value
anthropic-version: 2023-06-01
anthropic-beta: managed-agents-2026-04-01
content-type: application/json
```

Files routes instead require `anthropic-beta: files-api-2025-04-14` in strict
mode. Upload uses `multipart/form-data`; the other Files requests do not require
a JSON content type.

Skills routes require `anthropic-beta: skills-2025-10-02`. Creating a Skill or
Skill Version uses `multipart/form-data` and is limited to a bundle smaller
than 30 MB.

Memory routes require `anthropic-beta: agent-memory-2026-07-22`. Do not combine
that header with `managed-agents-2026-04-01` on Memory routes. Session creation
continues to use the Managed Agents beta when attaching a Memory Store.

`authorization` may replace `x-api-key`. Strict mode currently validates header
presence and version/beta values; it is not a production authentication system.

Every response includes a `request-id` header. JSON request bodies are limited
to 32 MiB and unknown top-level fields are rejected. A file upload is limited
to 500 MB and requires configured S3-compatible storage.

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

A failed Memory SHA-256 precondition is the more specific
`409 memory_precondition_failed_error`.

These mappings are Mango's public contract for the supported API subset. See
the [compatibility matrix](../compatibility.md) for parity limits.

## Pagination

Top-level Agent, Environment, and Session lists, Agent version histories, and
Event lists use opaque `page` tokens. Skill and Skill Version lists use the same
forward-only token convention. A cursor is bound to its resource and normalized
filters; Agent and Skill version cursors are additionally bound to their parent
resource ID, and Session cursors are bound to sort order. Reusing a cursor
outside its scope returns `400`.

List responses use `data` and nullable cursor fields:

```json
{
  "data": [],
  "next_page": null
}
```

Session lists also include `prev_page`. Agent, Agent Version, and Environment
lists are forward-only and include `next_page`.

Session Resource lists use a forward-only opaque `page` cursor. Omitting
`limit` returns all resources for the Session, whose active-resource limit is
500.

Files use their upstream ID-based pagination instead: `after_id` and
`before_id` select a direction, while the response contains `has_more`,
`first_id`, and `last_id`. The two direction parameters cannot be combined.

Vault, Credential, Deployment, and Deployment Run lists use forward-only opaque
`page` cursors and return `data` with nullable `next_page`. Cursors are bound to
their normalized filters; Credential cursors are additionally bound to their
parent Vault ID and archive filter.

## OpenAPI

The running server exposes `/openapi.yaml`, sourced from
`internal/httpapi/openapi.yaml`. All 21 core operations, ten Deployment and
Deployment Run operations, five Files operations, five Session Resources
operations, nine custom Skills operations, fourteen Memory operations, and
thirteen Vault/Credential operations define
stable operation IDs, path and query parameters, request and response schemas,
list envelopes, and shared error responses. The Session Event
contract includes the seven client-submittable variants, the 25 persisted core
variants, and the ephemeral SSE `event_start` and `event_delta` preview frames.

Repository tests keep all local references resolvable and lock both the core
operation inventory and the event unions.
