---
title: M1 core compatibility snapshot
slug: /maintainers/conformance/m1-core-snapshot
---

# M1 core compatibility snapshot

| Field | Value |
| --- | --- |
| Snapshot ID | `mango-m1-core-2026-08-03` |
| Captured | 2026-08-03 |
| Source revision | `297c6685494184c22f0a3628c621d76b36015551` |
| Upstream beta | `managed-agents-2026-04-01` |
| SDK evidence | Anthropic Go SDK `v1.61.0` |
| Status | Historical engineering evidence |

This page preserves the first scoped single-agent compatibility checkpoint. It
is not a Mango `v1.0.0` release: the repository had no tag, immutable image, or
release artifact at this point.

Current behavior is documented in [API compatibility](../compatibility.md).

## Captured claim

At the source revision above, Mango implemented the 21 SDK-visible Agent,
Environment, Session, and Session Event operations. The official Go SDK could
exercise the included request/response shapes, and accepted work followed the
documented durable single-agent state transitions.

| Resource | Included operations |
| --- | --- |
| Agents | Create, list, get, update, list Versions, archive |
| Environments | Create, list, get, update, archive, delete |
| Sessions | Create, list, get, update, archive, delete |
| Session Events | Send, list, stream |

The runtime evidence covered one primary Agent: model turns, the core event
union, sandbox built-ins, provider-native Web Search/Fetch, custom tools,
unauthenticated public MCP tools, confirmations, untargeted interrupts,
text-rubric outcomes, context projection, and restart recovery.

This was a scoped interoperability checkpoint, not a claim of universal API
parity, hosted-infrastructure equivalence, or production security readiness.

## Evidence captured

- Raw HTTP and OpenAPI tests for request, response, default, null, error, and
  event-union behavior.
- Official Go SDK black-box tests for every included lifecycle and paging path.
- PostgreSQL and Temporal tests for admission, transitions, event ordering,
  client-action barriers, retry/interrupt races, replay, sandbox ownership, and
  deletion fences.
- Service tests using PostgreSQL, Temporal, NATS, and Docker.
- CI gates for lint, unit/race tests, vet, docs, container build, and dependency
  security.

The operation ledger is preserved in the
[core conformance matrix](../api/core-conformance.md).

## Boundaries at capture time

- Non-empty Session Resources and create-time Vault references were rejected.
- Skill execution, resolved rosters, child Threads, delegation, and targeted
  interrupts were not included.
- Files, Skills, Memory, Vaults, Deployments, Environment Work, Session Threads,
  schedules, and webhooks were outside the checkpoint.
- Environment package installation was per Session rather than cached across
  Sessions sharing an Environment.
- Limited egress was enforceable only through OpenSandbox.
- Web Search/Fetch required provider-native support and `always_allow`.
- MCP covered unauthenticated public Streamable HTTP only.
- Context budgeting used a conservative estimate and extractive compaction.
- Streams did not replay history or interpret `Last-Event-ID`.
- Header checks were not authentication; tenant isolation, quotas, audit,
  backup, observability, Worker Versioning, and production deployment assets
  were not included.

Later work may supersede these boundaries on `main`, but it does not rewrite
what this checkpoint demonstrated.
