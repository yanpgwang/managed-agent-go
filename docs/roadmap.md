---
title: Roadmap
slug: /roadmap
---

# Roadmap

The roadmap prioritizes semantic correctness and recoverability before adding
surface-area compatibility.

## 1. Execution semantics

- Define batch input as one turn or one durable run per trigger, then make
  projection and commit behavior match that definition.
- Add a first-class durable pending-action model for custom tools and
  permission confirmations.
- Complete `always_ask` confirmation resume.
- Implement `user.interrupt` and propagate cancellation through model and tool
  execution.
- Harden stream behavior around upstream errors, late subscribers, preview
  completion, and slow consumers.
- Use newest-history windows or explicit compaction when a session exceeds the
  projection limit.

## 2. Durable execution

- Record run attempts separately from logical runs.
- Add a durable tool/side-effect journal and idempotency keys.
- Commit meaningful execution checkpoints instead of buffering all
  authoritative output until the end of a run.
- Define retryable versus terminal failures and project `rescheduling`
  truthfully.
- Add an outbox for reliable post-commit event delivery.

## 3. Session continuity

- Add durable checkpoint/restore so an idle session's sandbox survives a process
  restart, plus quota and eviction policies for session-scoped workspaces.
- Add context compaction, token usage accounting, and model-request spans.
- Persist resumable runtime checkpoints where the public contract requires
  continuity.

## 4. Compatibility breadth

- Complete request/response fields, raw JSON key-set tests, filters, and
  pagination behavior.
- Implement the remaining built-ins: `web_fetch` and `web_search`.
- Resolve and execute MCP toolsets.
- Add files, skills, memory, vaults, deployment scheduling, and webhooks.
- Model resolved multiagent rosters, threads, delegation, and orchestration as
  first-class domain concepts.
- Expand preview support to `agent.thinking` and add `span.*` lifecycle events.

## 5. Production topology

When real deployment requirements demand it:

- introduce versioned schema migrations and a production database adapter;
- split API and worker roles;
- add leases and fencing for distributed claims;
- add distributed event fan-out and observability;
- support stronger sandbox backends such as gVisor or a remote sandbox service.

## Completed foundations

- Server-owned, multi-turn Messages API history projection.
- Versioned agents and immutable session snapshots.
- Atomic input/run admission and single-node restart recovery.
- Multi-step model/tool loop.
- Local sandbox plus optional Docker provider.
- Session-scoped sandbox ownership: reused across a session's runs, isolated
  between sessions, released on session deletion (in-memory manager; no durable
  restore yet).
- `bash`, `read`, `write`, `edit`, `glob`, and `grep` execution.
- Custom-tool handoff with `requires_action`.
- Opt-in streaming preview of `agent.message`.
- Cursor pagination and SDK-backed compatibility tests for the implemented
  subset.
