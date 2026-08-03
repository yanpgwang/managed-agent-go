---
title: Roadmap
slug: /roadmap
---

# Roadmap

Mango is focused on a reliable, self-hosted implementation of the core Claude
Managed Agents session API. Priorities are ordered by user-visible capability,
not by reproducing Anthropic's internal implementation.

## M1: core Session API alignment

The first release milestone is the single-agent Session harness exposed by the
official Agent setup, Sessions, Events, outcomes, and Tools contracts.

Implemented in the current M1 slice:

- effective model ID, effort, speed, session override, usage, and timing stats;
- all single-agent input event variants, companion system context, correlated
  model spans whose durable starts precede live message deltas, durable
  confirmation/custom/self-hosted result barriers, and cross-process interrupt;
- text-rubric outcome work with an isolated grader, revisions, terminal Session
  projection, usage, and interruption;
- lossless provider transcript plus conservative token-aware request projection,
  extractive compaction, and rich image/document/large-result handling;
- built-in sandbox tools, provider-native Web Search/Fetch, remote MCP basics,
  custom tools, and confirmation.

Remaining M1 conformance work is deliberately narrow:

1. public periodic `span.outcome_evaluation_ongoing` events during long grader
   calls and Files-backed outcome rubrics;
2. `agent.thinking` preview production and exact endpoint/model context-window
   profiles where a provider exposes them.

Repeatable black-box comparison against the hosted Managed Agents API remains
useful optional validation when a Managed Agents-capable credential is
available. It is not a blocker for implementing behavior documented by the
public API contract.

## Runtime integrations

The first integration slice now includes provider-native Web Search/Fetch and
unauthenticated remote MCP tool discovery and execution. Remaining work is:

- deployment-managed MCP authentication and private-network connectivity;
- explicit per-endpoint server-tool capability profiles;
- optional managed Web Search/Fetch executors that can honor `always_ask`;
- additional preview event types;
- promotion of preview sandbox adapters after recorded live conformance.

## M2: multi-agent

After M1 conformance is stable, implement the official roster, Session thread,
delegation/message, cross-posted confirmation, context-compaction, and targeted
interrupt events. The single-agent event ledger, pending-action barrier, private
provider transcript, and Temporal turn loop are intended to be reused rather
than replaced.

## Deferred platform hardening

Production deployments additionally need:

- authentication, authorization, and tenant isolation;
- Worker Versioning and rolling-upgrade coverage;
- metrics, traces, audit logs, and operational runbooks;
- object storage for large histories and artifacts;
- quotas and resource policy;
- versioned deployment manifests and migration procedures.

Authentication/tenancy, provider routing, quotas, audit, metrics, backups, and
deployment management are intentionally not prerequisites for M1 API alignment.
Skills, memory, vaults, schedules, and webhooks remain outside the current core
harness. Current behavior is tracked in the
[compatibility matrix](compatibility.md); architectural guarantees are
documented in the [architecture overview](architecture.md).
