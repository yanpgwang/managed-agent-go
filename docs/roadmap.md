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

1. conformance tests for supported Managed Agents API behavior and removal of
   the frozen SQLite comparison backend;
2. bounded context management and compaction over server-owned history.

Durable cross-process interrupt, deterministic finish-vs-interrupt ordering,
Sandbox identity, worker-restart reattachment, and deletion cleanup are durable
for the local and Docker providers.

## Runtime integrations

After the core loop is complete:

- MCP tool discovery and execution;
- supported server tools such as web search and fetch;
- token usage and model/tool execution spans;
- additional preview event types;
- promotion of preview sandbox adapters after recorded live conformance.

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
