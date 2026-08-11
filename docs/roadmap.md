---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango's core single-agent runtime and all 90 pinned HTTP operations now have an
implemented route and conformance evidence. The roadmap tracks only work that
is still open; completed implementation history belongs in Git commits and,
after the first tag, release notes.

Priorities are ordered by the next usable product boundary rather than by the
shape of Anthropic's private implementation.

## M2: complete the multi-agent contract

The ordinary coordinator and persistent child-Agent path is implemented. The
remaining M2 work is:

1. **Immutable Context Snapshots** — persist the compacted child context and
   recovery boundary without changing the public primary Session ledger.
2. **Report-only preview behavior** — match the documented visibility boundary
   when a child report wakes coordinator synthesis without a normal user turn.
3. **Advisor lifecycle** — implement the reserved, automatically terminating
   consultation Thread rather than representing it as an ordinary roster
   Agent.
4. **Shared Session budgets** — aggregate provider list cost across concurrent
   Threads and enforce one durable Session limit.

[Issue #116](https://github.com/yanpgwang/managed-agent-go/issues/116) should
close when ordinary child context compaction and recovery are complete, which
finishes its accepted scope. Advisor and shared-budget work should be tracked
independently so that one issue does not become a permanent project ledger.

## M3: developer-preview release

The first tagged release requires:

- coherent task-oriented documentation and a real-model walkthrough;
- one immutable source tag and versioned API/worker images;
- explicit database migration and API/worker upgrade ordering;
- repeatable 90-operation service conformance;
- a published compatibility summary and known limitations;
- checksum, image, and release-note provenance.

This milestone is a developer preview for trusted environments, not a
production-readiness claim.

## M4: production platform

Production promotion requires work that is separate from API alignment:

- authentication, authorization, workspace identity, and tenant isolation;
- Temporal Worker Versioning, draining, rolling upgrade, and rollback evidence;
- metrics, traces, structured audit events, alerts, and operational runbooks;
- explicit migration, backup, restore, and disaster-recovery procedures;
- quotas, rate limits, storage retention, and resource policy;
- production Compose and/or Kubernetes manifests with external stateful
  dependencies;
- distributed Files/Skills lifecycle reconciliation and large-payload
  offload;
- provider-aware sandbox routing, capacity management, eviction, and repeated
  live conformance for remote adapters.

## Deferred compatibility work

The following capabilities are useful but do not block the first developer
preview:

- private-network MCP connectivity, deprecated-SSE fallback, resources, and
  prompts;
- optional managed Web Search/Fetch executors for endpoints without native
  server tools;
- File-sourced messages, File outcome rubrics, and arbitrary sandbox-output
  export;
- GitHub repository Session Resources and Skill loading;
- environment-variable secret egress and Vault refresh-failure webhooks;
- automatic Deployment archival after Agent archive;
- automatic Memory Version retention.

Current behavior is summarized in [API compatibility](compatibility.md).
Engineering evidence is kept separately in
[Conformance evidence](conformance.md).
