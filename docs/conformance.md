---
title: Conformance evidence
slug: /maintainers/conformance
---

# Conformance evidence

This section is an engineering ledger for maintainers. Users should start with
[API compatibility](compatibility.md), which states what workflows are usable
and where important constraints apply.

Conformance describes evidence for the current `main` branch. It is not a
Mango release number and does not imply byte-for-byte equivalence with
Anthropic's hosted implementation.

## Current upstream baseline

| Contract | Baseline |
| --- | --- |
| Managed Agents beta | `managed-agents-2026-04-01` |
| Files beta | `files-api-2025-04-14` |
| Skills beta | `skills-2025-10-02` |
| Memory beta | `agent-memory-2026-07-22` |
| Official black-box client | Anthropic Go SDK `v1.62.0` |

## Operation inventory

| Surface | Operations | Detailed ledger |
| --- | ---: | --- |
| Core Agents, Environments, Sessions, and Events | 21 | [Core](api/core-conformance.md) |
| Files | 5 | [Files](api/files-conformance.md) |
| Session Resources | 5 | [Session Resources](api/session-resources-conformance.md) |
| Skills | 9 | [Skills](api/skills-conformance.md) |
| Memory | 14 | [Memory](api/memory-conformance.md) |
| Vaults and Credentials | 13 | [Vaults](api/vaults-conformance.md) |
| Deployments and Deployment Runs | 10 | [Deployments](api/deployments-conformance.md) |
| Environment Work | 8 | [Environment Work](api/environment-work-conformance.md) |
| Session Threads and Thread Events | 5 | [Session Threads](api/session-threads-conformance.md) |
| **Total** | **90** | `/openapi.yaml` |

Route presence is only the inventory floor. Each behavior claim should have the
smallest appropriate evidence:

- raw HTTP and OpenAPI tests for transport, schemas, defaults, null handling,
  headers, errors, and event unions;
- official Go SDK tests for client interoperability;
- PostgreSQL tests for durable resource and transaction semantics;
- Temporal replay and integration tests for orchestration and crash recovery;
- PostgreSQL/NATS tests for stream repair and backpressure;
- real PostgreSQL, Temporal, NATS, MinIO, and Docker service tests for composed
  behavior.

The [core compatibility snapshot](compatibility/core-2026-08-03.md) preserves
the scope and evidence captured on 2026-08-03. It is a historical engineering
snapshot, not a published Mango release.

Official sources and verification boundaries are listed in
[Upstream provenance](provenance.md).
