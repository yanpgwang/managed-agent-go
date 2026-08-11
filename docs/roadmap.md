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
behavior belongs in [Releases](releases.md), and current support claims belong
in [API compatibility](compatibility.md). Priorities may change as the upstream
contract and implementation evidence evolve.

## Current focus

### Finish durable multi-agent execution

Mango supports ordinary coordinator delegation and persistent child-Agent
Threads. The current focus is completing the remaining context-recovery and
coordinator-synthesis semantics so that long-running multi-agent Sessions stay
correct across retries, restarts, and context compaction.

Follow the active scope in
[Issue #116](https://github.com/yanpgwang/managed-agent-go/issues/116).

## Next adoption boundary

### Ship a developer preview

The next release boundary is a reproducible, self-hosted build for evaluation
in trusted environments. It should provide a coherent setup and real-model
walkthrough, immutable source and container artifacts, a documented upgrade
path, and repeatable conformance results with explicit limitations.

The developer preview is an integration and interoperability target. It is not
a production-readiness or hosted-service-equivalence claim.

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

Maintainer-level verification is documented separately in
[Conformance evidence](conformance.md).
