---
title: Core API conformance matrix
---

# Core API conformance matrix

This matrix tracks the 21 SDK-visible operations in Mango's core single-agent
scope. It is based on the public Managed Agents API reference and the official
Anthropic Go SDK v1.61.0 types. It was last verified against those sources on
2026-08-03.

Route presence is only an inventory signal. A **yes** in the route column does
not claim full compatibility: accepted fields, defaults, null behavior, error
responses, durable state transitions, and runtime semantics still require
separate evidence.

| Resource | Operation | HTTP route | Route | Official SDK black box | Durable/service path | Remaining core gap |
| --- | --- | --- | --- | --- | --- | --- |
| Agent | Create | `POST /v1/agents` | Yes | Yes | Yes | None identified for documented core fields, defaults, null handling, or errors. |
| Agent | List | `GET /v1/agents` | Yes | Yes | Yes | None identified for the documented core filters and forward cursor. |
| Agent | Get | `GET /v1/agents/{agent_id}` | Yes | Yes | Yes | None identified for the core resource projection. |
| Agent | Update | `POST /v1/agents/{agent_id}` | Yes | Yes | Yes | None identified for documented core fields, defaults, null handling, or errors. |
| Agent | List versions | `GET /v1/agents/{agent_id}/versions` | Yes | Yes | Yes | None identified for forward pagination. |
| Agent | Archive | `POST /v1/agents/{agent_id}/archive` | Yes | Yes | Yes | None identified for the core idempotent archive path. |
| Environment | Create | `POST /v1/environments` | Yes | Yes | Yes | None identified; packages and capable-backend limited egress are enforced before sandbox binding. |
| Environment | List | `GET /v1/environments` | Yes | Yes | Yes | None identified for configured package and networking projections. |
| Environment | Get | `GET /v1/environments/{environment_id}` | Yes | Yes | Yes | None identified for configured package and networking projections. |
| Environment | Update | `POST /v1/environments/{environment_id}` | Yes | Yes | Yes | None identified; nested limited fields preserve omission and future Sessions snapshot updates. |
| Environment | Archive | `POST /v1/environments/{environment_id}/archive` | Yes | Yes | Yes | None identified for configured package and networking projections. |
| Environment | Delete | `DELETE /v1/environments/{environment_id}` | Yes | Yes | Yes | None identified for the core unreferenced-delete path. |
| Session | Create | `POST /v1/sessions` | Yes | Yes | Yes | Core field/default/null behavior is covered; unsupported resources and vaults remain explicit. |
| Session | List | `GET /v1/sessions` | Yes | Yes | Yes | None identified for the in-scope filters and bidirectional cursors. |
| Session | Get | `GET /v1/sessions/{session_id}` | Yes | Yes | Yes | None identified for the in-scope resource projection. |
| Session | Update | `POST /v1/sessions/{session_id}` | Yes | Yes | Yes | None identified for the idle precondition, atomic update event, or next-turn replacement visibility. |
| Session | Archive | `POST /v1/sessions/{session_id}/archive` | Yes | Yes | Yes | None identified for the core idempotent archive path. |
| Session | Delete | `DELETE /v1/sessions/{session_id}` | Yes | Yes | Yes | None identified for the deletion fence, sandbox teardown, stream close, or restart recovery. |
| Session event | Send | `POST /v1/sessions/{session_id}/events` | Yes | Yes | Yes | None identified for core cross-turn projection or tool-result legality. |
| Session event | List | `GET /v1/sessions/{session_id}/events` | Yes | Yes | Yes | None identified for processed-time filters and deterministic forward pagination. |
| Session event | Stream | `GET /v1/sessions/{session_id}/events/stream` | Yes | Yes | Yes | None identified for ordering, reconnection, bounded backpressure, or API-process replacement. |

## Evidence map

- Official SDK lifecycle, update, event, and paging tests:
  `internal/httpapi/sdk_test.go` and
  `internal/httpapi/sdk_session_list_test.go`.
- Exact wire and error-envelope tests:
  `internal/httpapi/sdk_golden_test.go`.
- PostgreSQL resource, pagination, admission, and transition tests:
  `internal/pg/resource_list_test.go`, `internal/pg/resources_test.go`,
  `internal/pg/session_update_test.go`, and `internal/pg/store_test.go`.
- PostgreSQL/Temporal service-path tests:
  `internal/controlplane/integration_test.go`,
  `internal/controlplane/session_update_test.go`, the runtime suites under
  `internal/temporal`, and the version-gate matrix in
  `internal/temporal/replay_test.go`.
- PostgreSQL/NATS stream recovery and backpressure tests:
  `internal/live/integration_test.go` and `internal/live/recovery_test.go`.
- OpenAPI lifecycle, Session Event union, and reference invariants:
  `internal/httpapi/openapi_test.go`.

The user-facing support claim remains the
[Claude API coverage](../compatibility.md) page. This matrix is the narrower
engineering ledger used to identify missing evidence without treating a
registered route as compatibility.

The frozen claim based on this evidence is
[core compatibility statement v1.0.0](../compatibility/core-v1.md).

## Normative references

- [Agents](https://platform.claude.com/docs/en/api/beta/agents)
- [Environments](https://platform.claude.com/docs/en/api/beta/environments)
- [Sessions](https://platform.claude.com/docs/en/api/beta/sessions)
- [Session events](https://platform.claude.com/docs/en/api/beta/sessions/events)
