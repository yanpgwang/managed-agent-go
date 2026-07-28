---
title: Roadmap
slug: /roadmap
---

# Roadmap

The roadmap prioritizes semantic correctness and recoverability before adding
surface-area compatibility.

## 1. Execution semantics

- A first-class durable pending-action model with a claim gate is implemented
  for custom tools (park → durable pending action → matching
  `user.custom_tool_result` → resume → `end_turn`) and for the single built-in
  `always_ask` confirmation allow/deny **execution** resume (park →
  `user.tool_confirmation` → recover original `agent.tool_use` from causal
  history → allow executes the built-in / deny rejects with `deny_message` →
  `agent.tool_result` correlated to the original id → `end_turn`). Remaining: an
  aggregated multi-action resume protocol (a multi-action park currently gates
  all actions but each must be resolved individually), and — because there is no
  durable side-effect journal yet — crash-replay safety for an allowed built-in
  whose side effect committed before its result did.
- Single-process, single-agent `user.interrupt` is implemented: an admitted
  interrupt promptly cancels the session's active run through a
  `context.WithCancelCause` scoped to the runtime (propagating to model and tool
  calls), the canceled run completes gracefully (no `session.error` /
  `terminated`, no extra idle terminal), and the interrupt's own control run emits
  the single `session.status_idle{end_turn}`; a same-batch redirect
  `user.message` then runs normally. Remaining: `session_thread_id`
  routing / multi-agent interrupt targeting, interrupting a parked
  (`requires_action`) session — currently the pending-action gate blocks the
  interrupt until the blocking event resolves, so aborting a parked turn is not yet
  supported — cross-process (multi-node) delivery,
  and a durable cancellation signal that survives a process crash between
  interrupt admission and the active run's completion commit (the signal is
  currently in-memory only).
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
- Per-trigger durable runs: a request batch is admitted atomically in input
  order, each processable trigger gets its own durable run and commit boundary,
  and each run persists the exact IDs of the output events it committed. The
  model-facing conversation is reconstructed durably from run causality — each
  prior run's trigger IDs followed by its persisted output IDs, then the current
  trigger — so a run projects earlier runs' committed output as separate turns,
  the ordering survives a restart, and a run never sees a later queued trigger.
  This is our own causal association; it does not claim to mirror any exact
  Anthropic-internal ordering. A terminated session never claims its leftover
  queued work.
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
