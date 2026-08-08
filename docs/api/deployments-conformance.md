---
title: Deployments conformance matrix
---

# Deployments conformance matrix

This matrix tracks the ten SDK-visible Deployment and Deployment Run operations
in the public Managed Agents API. It is based on the official API reference and
Anthropic Go SDK v1.61.0, verified on 2026-08-09.

| Resource | Operation | HTTP route | Route | Official SDK black box | Durable/service path | Known gap |
| --- | --- | --- | --- | --- | --- | --- |
| Deployment | Create | `POST /v1/deployments` | Yes | Yes | Yes | GitHub repository resources are rejected. |
| Deployment | List | `GET /v1/deployments` | Yes | Yes | Yes | None identified for documented filters and forward pagination. |
| Deployment | Get | `GET /v1/deployments/{deployment_id}` | Yes | Yes | Yes | None identified for the current SDK response shape. |
| Deployment | Update | `POST /v1/deployments/{deployment_id}` | Yes | Yes | Yes | Automatic archival after Agent archival is not implemented. |
| Deployment | Archive | `POST /v1/deployments/{deployment_id}/archive` | Yes | Yes | Yes | None identified for idempotent terminal archive. |
| Deployment | Pause | `POST /v1/deployments/{deployment_id}/pause` | Yes | Yes | Yes | None identified for suppressing scheduled triggers. |
| Deployment | Unpause | `POST /v1/deployments/{deployment_id}/unpause` | Yes | Yes | Yes | None identified for future-only resume without missed-run backfill. |
| Deployment | Run | `POST /v1/deployments/{deployment_id}/run` | Yes | Yes | Yes | Exact hosted admission quotas are operator-owned. |
| Deployment Run | List | `GET /v1/deployment_runs` | Yes | Yes | Yes | None identified for documented filters and forward pagination. |
| Deployment Run | Get | `GET /v1/deployment_runs/{deployment_run_id}` | Yes | Yes | Yes | None identified for success, failure, and trigger projections. |

## Behavioral evidence

- `internal/httpapi/deployment_sdk_test.go` exercises all ten operations through
  the official Go SDK, including filters, lifecycle transitions, manual Run,
  and Run retrieval.
- `internal/app/deployment_service_test.go` verifies immutable Agent Version
  resolution, Session creation, scheduled failure recording, and error
  auto-pause.
- `internal/pg/deployment_test.go` verifies that a successful Session and Run
  commit atomically, rollback together on conflict, and preserve
  `deployment_id` Session filtering. It also verifies schedule claim leases,
  occurrence advancement, and Environment reference protection.
- `internal/httpapi/openapi_test.go` locks the ten-operation inventory and
  resolves the Deployment schemas and responses.

Scheduled execution runs only in the `orchestrate` worker role. PostgreSQL
leases recover abandoned work, and each scheduled occurrence has a unique
durable identity. The implementation deliberately does not claim Anthropic's
internal scheduler, jitter distribution, quotas, or infrastructure.

## Normative references

- [Scheduled deployments](https://platform.claude.com/docs/en/managed-agents/scheduled-deployments)
- [Deployments API](https://platform.claude.com/docs/en/api/beta/deployments)
- [Deployment Runs API](https://platform.claude.com/docs/en/api/beta/deployment-runs)
- [Anthropic Go SDK v1.61.0](https://github.com/anthropics/anthropic-sdk-go/tree/v1.61.0)
