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
supported, limited, or not supported.

:::

## Endpoints

| Resource | Endpoints |
| --- | --- |
| Agents | `POST/GET /v1/agents`, `GET/POST /v1/agents/{id}`, versions, archive |
| Environments | `POST/GET /v1/environments`, get, archive, delete |
| Sessions | `POST/GET /v1/sessions`, get, update, archive, delete |
| Events | `POST/GET /v1/sessions/{id}/events`, SSE stream |
| Operations | `GET /healthz`, `GET /readyz`, `GET /openapi.yaml` |

Resource-specific request shapes are covered in:

- [Agents](agents.md)
- [Environments](environments.md)
- [Sessions](sessions.md)
- [Events and streaming](events.md)

## Headers

The default development server accepts requests without compatibility headers.
Run with `-strict` to require them:

```http
anthropic-version: 2023-06-01
anthropic-beta: managed-agents-2026-04-01
content-type: application/json
```

Every response includes a `request-id` header. JSON request bodies are limited
to 32 MiB and unknown top-level fields are rejected.

## Authentication

Authentication is configured with key material, not with `-strict`:

```http
x-api-key: <a key configured in MANAGED_AGENT_API_KEYS>
```

`MANAGED_AGENT_API_KEYS` holds a comma- or whitespace-separated list of
`<key-id>:<secret>` entries. Multiple keys can be configured so a key can be
rotated without a window where none is valid. Keys are stored as SHA-256
digests and compared in constant time; the key id is a non-secret label and is
the only part that appears in logs.

- When at least one key is configured, every request outside `GET /healthz` and
  `GET /readyz` must present an accepted key.
- When no key is configured, authentication is **disabled**, the server logs a
  warning at startup, and `-strict` refuses to start. This is the zero-config
  local development path; see [Security](https://github.com/yanpgwang/managed-agent-go/blob/main/SECURITY.md).
- A missing key and an unknown key are both rejected with `401` and
  `authentication_error`. Header presence alone never authenticates.

:::note[Local choice]

The official Managed Agents documentation describes the `x-api-key` header and
lists an `authentication_error` error type, but binds **no HTTP status code** to
an authentication failure and draws no missing-versus-invalid distinction.
Returning `401` with `authentication_error` is therefore Mango's local choice,
not a reproduced contract.

Upstream documents **no** rate-limit response headers, so Mango emits none
(`retry-after`, `anthropic-ratelimit-*`, and `x-should-retry` are never set).
The published Managed Agents rate limits — 300 requests per minute for create
endpoints and 1,200 for read endpoints — are Anthropic organization policy;
Mango does not implement inbound rate limiting.

:::

`authorization: Bearer <key>` is accepted only when
`MANAGED_AGENT_AUTH_ALLOW_AUTHORIZATION_HEADER=true`. It is a non-upstream
convenience extension and is off by default.

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

Session and event lists use opaque `page` tokens. A cursor is bound to its
resource, sort order, and normalized filters. Reusing it with different filters
returns `400`.

List responses use `data` and nullable cursor fields:

```json
{
  "data": [],
  "next_page": null
}
```

Session lists also include `prev_page`. Agent listing is currently single-page
and always returns `next_page: null`.

## OpenAPI

The running server exposes `/openapi.yaml`, sourced from
`internal/httpapi/openapi.yaml`. It is currently an endpoint inventory, not a
complete generated client contract: schemas, examples, parameter definitions,
and the full event union still need to be added.

For now, use these guides and the API coverage page when integrating.
