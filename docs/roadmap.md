---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango is working toward an open, self-hosted implementation of Claude Managed
Agents with compatible public behavior and durable orchestration underneath.

This roadmap describes project direction, not a delivery schedule. Concrete
work and acceptance criteria belong in
[GitHub Issues](https://github.com/yanpgwang/managed-agent-go/issues); shipped
behavior belongs in
[GitHub Releases](https://github.com/yanpgwang/managed-agent-go/releases), and
current support claims belong in [API compatibility](compatibility.md).
Priorities may change as the upstream contract and implementation evidence
evolve.

## Current focus

### Extend advanced orchestration before the first preview

The immediate focus is the current official Go SDK contract and the two
high-value advanced orchestration gaps: advisor consultations within a running
multi-agent Session, and asynchronous Dreams that consolidate Session
transcripts into durable Memory Stores. These features should reuse ordinary
Thread, Session, usage, budget, transcript, Memory, and Temporal primitives
where their public semantics match, while preserving their distinct lifecycle
and redaction rules.

The first packaged developer preview remains a later integration boundary, not
the current feature cutoff.

## Capability priority

Work within a tier may be split into independent PRs. A lower tier can move
earlier when it is a prerequisite for a higher-tier slice.

| Tier | Capability | Next concrete boundary |
| --- | --- | --- |
| 0 | Contract tracking | Keep the official Go SDK and documented unions pinned to a reviewed version; classify new research-preview surfaces explicitly instead of folding them into the stable operation count. |
| 0 | Multi-agent | Implement the reserved `advisor` roster form, primary-only consultation tool, automatically terminating Thread lifecycle, redacted result delivery, interrupts, and Session usage/budget aggregation. Resolve exact report-only preview suppression separately. |
| 0 | Dreams | Add the five Dream operations and durable asynchronous lifecycle, then implement transcript-plus-Memory consolidation with `create_new` and `update_existing` output behavior. |
| 1 | Model and context runtime | Add complete per-provider-request audit recipes, model context profiles, and compaction quality/evaluation evidence before pursuing provider-private tokenizer parity. |
| 1 | Webhooks | Emit compact, signed, retryable lifecycle notifications from a transactional outbox, covering Session/Thread first and the remaining documented resources incrementally. |
| 1 | Vaults | Implement environment-variable credential egress with injection-location policy and publish refresh-failure lifecycle events through Webhooks. |
| 2 | MCP tools | Add explicit private connectivity or tunnels, deprecated SSE fallback, and MCP resources/prompts without weakening the current public-network fence. |
| 2 | Session Resources | Add GitHub repository resources and extend mount execution beyond Docker through provider capabilities. |
| 2 | Skills | Add GitHub, self-hosted, Anthropic-managed, and remote-sandbox activation on top of the existing immutable custom-Skill contract. |
| 2 | Memory | Add automatic Version retention and non-Docker mounts after Dreams exercise the current read/write and immutable-history foundation. |
| 3 | Files | Add client-upload download, message/outcome inputs, arbitrary output export, and distributed reconciliation. |
| 3 | Deployments | Add GitHub resources and explicit propagation behavior when a pinned Agent is archived. |
| 3 | Environment Work | Add environment-key issuance, tenant-scoped authorization, Work secrets, and health-check Work. |
| 3 | Sandbox tools and adapters | Accumulate live conformance evidence, broaden mount-capable remote execution, and make provider routing/capacity policy explicit. |
| 3 | Distributed operation | Add Worker Versioning, heterogeneous routing, distributed reconcilers, rollout evidence, and production observability. |

## Longer-term direction

### Make self-hosting production-ready

Production use requires security and operational work beyond API alignment.
The principal themes are identity and tenant isolation, policy and quotas,
safe worker and database upgrades, observability, backup and recovery, and
production deployment guidance.

### Broaden infrastructure support

Mango should run the same Agent contract across local and remote sandbox
providers. This includes provider-aware routing and capacity management,
distributed File and Skill lifecycle handling, and conformance evidence for
each supported backend.

### Extend optional compatibility surfaces

After the core workflows are dependable, compatibility can expand across
additional MCP transports and capabilities, resource sources, managed tool
executors, and lifecycle automation. Individual gaps are tracked as focused
issues rather than as a permanent checklist on this page.
