---
title: Product direction
slug: /product
sidebar_position: 2
---

# Product direction

Mango is a self-hosted runtime for durable AI agents. It owns the control
plane, execution lifecycle, public API, and product roadmap needed to run
long-lived agent work on infrastructure an operator controls.

Mango began with resource and workflow ideas documented by Claude Managed
Agents. Those ideas remain useful design references, but drop-in use with an
Anthropic service or SDK is not a product goal. Mango can keep a good idea,
simplify it, extend it, or replace it when the self-hosted runtime needs a
different contract.

## What Mango optimizes for

- **Durable work.** Accepted input, events, tool calls, waits, retries, and
  cancellation survive process restarts.
- **Operator control.** State, credentials, sandboxes, schedules, and model
  traffic remain within infrastructure and providers selected by the operator.
- **Recoverable execution.** Crash boundaries, idempotency, reconciliation,
  and upgrade behavior are part of a feature rather than follow-up work.
- **Composable infrastructure.** Model, sandbox, object-storage, and messaging
  integrations sit behind explicit boundaries and can evolve independently.
- **A coherent native API.** Mango's own documentation and OpenAPI definition
  describe the supported contract. New surface area must improve a real user
  or operator workflow.

## Relationship to external contracts

Public specifications and SDKs may provide terminology, workflow examples, and
edge cases. They are inputs to design research, not authorities over Mango.
In particular:

- users are not required to use an Anthropic SDK;
- Mango does not promise drop-in use with a hosted agent platform;
- an external SDK release does not automatically create Mango work;
- interoperability tests are development evidence, not obligations; they may
  change or be removed with the surface they cover;
- vendor-specific previews are considered only when they solve an independently
  selected Mango problem.

Mango currently has no customers and no supported stable release. `/v1` is the
single development namespace, not a Mango 1.0 or stable API declaration.
Routes, fields, schemas, and behavior change directly on
`/v1` when that produces a clearer product. Earlier commits, SDK behavior,
fixtures, tests, and development databases create no compatibility obligation.
Do not add `/v2`, legacy routes, dual behavior, deprecation windows, translation
layers, or data migrations solely to preserve them. This policy changes only
after an explicit maintainer decision establishes a supported release or a real
customer migration requirement.

## Choosing work

A substantial change should answer three questions before implementation:

1. Which concrete Mango user or operator workflow improves?
2. Which durability, security, recovery, and operational boundaries apply?
3. What is the smallest independently testable slice?

Priority goes to deployment, upgrades, observability, reliable Session
execution, safe tool and sandbox operation, Files and deliverables, persistent
Memory, and a clear native developer experience. Breadth, endpoint counts, and
parity with another product are not measures of progress.

## Current transition

The implementation already contains a broad API, but existing surface area is
not automatically permanent. During the transition:

- [Capabilities and limits](capabilities.md) is the honest inventory of what
  Mango currently does;
- [API reference](api/overview.md) and the served OpenAPI document define the
  current HTTP surface;
- [GitHub Issues](https://github.com/yanpgwang/mango/issues) hold active Mango
  engineering work;
- differences from external contracts are not backlog items unless an Issue
  gives them a Mango-specific rationale.
