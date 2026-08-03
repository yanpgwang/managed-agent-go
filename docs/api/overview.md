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

Every response includes a `request-id` header. Mango does **not** send
`anthropic-organization-id`, the other documented response header: it is
single-tenant and has no organization to name. JSON request bodies are limited
to 32 MiB and unknown top-level fields are rejected.

See [Authentication](#authentication) for the credential headers, which are
configured separately from `-strict`.

## Authentication

Authentication is configured with key material, not with `-strict`. Both
documented Claude API credential headers are accepted:

```http
x-api-key: <a key configured in MANAGED_AGENT_API_KEYS>
```

```http
authorization: Bearer <a key configured in MANAGED_AGENT_API_KEYS>
```

`MANAGED_AGENT_API_KEYS` holds a comma- or whitespace-separated list of
`<key-id>:<secret>` entries. Multiple keys can be configured so a key can be
rotated without a window where none is valid. Keys are stored as SHA-256
digests and compared in constant time; the key id is a non-secret label and is
the only part that appears in logs.

- When at least one key is configured, every request outside `/healthz` and
  `/readyz` must present an accepted credential.
- When no key is configured, authentication is **disabled**, the server logs a
  warning at startup, and `-strict` refuses to start. This is the zero-config
  local development path; see [Security](https://github.com/yanpgwang/managed-agent-go/blob/main/SECURITY.md).
- A missing credential and an unknown one are both rejected with `401` and
  `authentication_error`. Header presence alone never authenticates.

:::note[Documented contract]

The [API overview](https://platform.claude.com/docs/en/api/overview)
authentication table lists both headers, each marked "One of `x-api-key` or
`Authorization`", so both are first-class. The
[errors page](https://platform.claude.com/docs/en/api/errors) binds status
codes to error types explicitly, including `401` — `authentication_error`, so
that pairing is reproduced rather than invented.

:::

:::warning[Mango accepts the header shape, not federated tokens]

Upstream, the `Authorization` value is a short-lived access token obtained from
`POST /v1/oauth/token` through
[Workload Identity Federation](https://platform.claude.com/docs/en/manage-claude/workload-identity-federation).

**Mango implements neither.** There is no `POST /v1/oauth/token` endpoint and no
federation trust evaluation. Mango accepts only the *header shape* and validates
the presented bearer value against the same configured key set as `x-api-key`.
The token is opaque: it is not parsed, and it carries no expiry of its own
beyond the operator rotating `MANAGED_AGENT_API_KEYS`.

:::

### Presenting both headers

A request may carry both headers. Each is tried in turn — `x-api-key` first —
and the request authenticates if either value is an accepted key.

This is a **design inference** from official SDK behavior rather than a stated
rule. `anthropic.NewClient` reads `ANTHROPIC_AUTH_TOKEN` from the environment
into an `Authorization: Bearer` header, and an explicit `option.WithAPIKey`
adds `x-api-key` alongside it, so a developer with that variable exported sends
both headers with *different* values on every request. Rejecting the
combination would break the official client. Trying both grants nothing extra:
a caller must still present at least one configured key, exactly as if that
header had been sent alone.

`MANAGED_AGENT_AUTH_DISABLE_AUTHORIZATION_HEADER=true` narrows Mango to
`x-api-key` only. It exists for a deployment whose ingress already uses
`Authorization` for something else, and it removes a documented header, so
leave it unset unless that applies.

### Rate limiting

Mango implements no inbound rate limiting, so it never returns `429`
`rate_limit_error` and has no occasion to emit `retry-after`. The
`retry-after` header is documented — the errors page says the official SDKs
retry "honoring the `retry-after` header when present" — Mango simply has
nothing to report with it. The published Managed Agents rate limits (300
requests per minute for create endpoints, 1,200 for read endpoints) are
Anthropic organization policy.

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

| HTTP status | Error type | Source |
| --- | --- | --- |
| `400` | `invalid_request_error` | Documented contract |
| `401` | `authentication_error` | Documented contract |
| `404` | `not_found_error` | Documented contract |
| `409` | `conflict_error` | Documented contract |
| `413` | `request_too_large` | Documented contract |
| `422` | `invalid_request_error` | Local choice — see below |
| `500` | `api_error` | Documented contract |

Every pairing above except `422` reproduces the status/type table on the
[Claude API errors page](https://platform.claude.com/docs/en/api/errors).

`422` is the one status Mango invents. It answers "this documented capability
is not implemented here" — a file-backed outcome rubric, file-sourced message
content, session resources, or `vault_ids` — and is kept distinct from a
malformed request. The *type* stays documented, because the errors page says
`invalid_request_error` "may also be used for other 4XX status codes not listed
in this section", so a client branching on the error type is unaffected. A
client branching on the exact status will see a `422` upstream would not send;
treat 4xx `invalid_request_error` as one class.

Documented statuses Mango never produces: `402` `billing_error` (no billing),
`403` `permission_error` (no authorization or per-key scoping), `429`
`rate_limit_error` (no inbound rate limiting), `504` `timeout_error`, and `529`
`overloaded_error`.

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
