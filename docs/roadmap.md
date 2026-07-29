---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango is focused on a reliable, self-hosted implementation of the core Claude
Managed Agents session API. Priorities are ordered by user-visible capability,
not by reproducing Anthropic's internal implementation.

## Core compatibility

These are the release-blocking capabilities for the managed-agent harness:

1. durable custom-tool and `always_ask` park/resume;
2. durable cross-process interrupt with deterministic event ordering;
3. bounded context management and compaction over server-owned history;
4. restart-resilient sandbox identity and lifecycle;
5. conformance tests for supported Managed Agents API behavior.

## Runtime integrations

After the core loop is complete:

- MCP tool discovery and execution;
- supported server tools such as web search and fetch;
- token usage and model/tool execution spans;
- additional preview event types;
- provider-backed sandbox adapters.

## Production hardening

Production deployments additionally need:

- authentication, authorization, and tenant isolation;
- Worker Versioning and rolling-upgrade coverage;
- metrics, traces, audit logs, and operational runbooks;
- object storage for large histories and artifacts;
- quotas, resource policy, and orphan reconciliation;
- versioned deployment manifests and migration procedures.

Files, skills, memory, vaults, multi-agent orchestration, schedules, and webhooks
remain outside the core harness scope. Current behavior is tracked in the
[compatibility matrix](compatibility.md); architectural guarantees are
documented in the [architecture overview](architecture.md).
