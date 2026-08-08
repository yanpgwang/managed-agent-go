---
title: Environment Work conformance matrix
slug: /api/environment-work-conformance
---

# Environment Work conformance matrix

Baseline: `managed-agents-2026-04-01`, Anthropic Go SDK `v1.61.0`.

| Operation | Route | HTTP | Official Go SDK | Durable semantics | Known limitation |
| --- | --- | --- | --- | --- | --- |
| Get | `GET /v1/environments/{environment_id}/work/{work_id}` | Yes | Yes | PostgreSQL resource lookup | Work `secret` is null. |
| Update | `POST /v1/environments/{environment_id}/work/{work_id}` | Yes | Yes | Metadata merge; raw JSON null deletes a key | The generated Go map cannot express null deletion. |
| List | `GET /v1/environments/{environment_id}/work` | Yes | Yes | Stable descending keyset page | Local limit default/max are 100/1000 because upstream documents no bounds. |
| Ack | `POST /v1/environments/{environment_id}/work/{work_id}/ack` | Yes | Yes | Locked `queued` to `starting` transition | None identified. |
| Heartbeat | `POST /v1/environments/{environment_id}/work/{work_id}/heartbeat` | Yes | Yes | Exact optimistic heartbeat and durable TTL | Environment-key ownership is not yet enforced. |
| Poll | `GET /v1/environments/{environment_id}/work/poll` | Yes | Yes | Concurrent PostgreSQL claim, reclaim, optional long poll | Long polling uses bounded database reconciliation rather than Redis Streams. |
| Stats | `GET /v1/environments/{environment_id}/work/stats` | Yes | Yes | Queue depth, pending claims, oldest item, recent workers | Processed with PostgreSQL metrics rather than Redis consumer groups. |
| Stop | `POST /v1/environments/{environment_id}/work/{work_id}/stop` | Yes | Yes, including `WorkPoller` | Graceful and forced terminal transitions; `204` response | The generated direct Go method needs its documented response-body bypass until its spec declares 204. |

Additional behavior evidence includes atomic self-hosted Session activation,
coalescing one live Work item per Session, concurrent poll exclusion,
first/subsequent heartbeat compare-and-set, stale lease recovery, graceful
shutdown signaling, terminal reactivation, and direct use of the official
`lib/environments.WorkPoller` helper.

Not yet claimed:

- Environment-key issuance, rotation, or Environment/tenant authorization;
- Work secret payloads;
- health-check Work item production;
- hosted Redis Stream implementation details.
