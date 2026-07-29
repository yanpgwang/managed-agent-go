---
title: Sandbox backends
slug: /sandboxes
---

# Sandbox backends

Sandbox support is intentionally incremental. A backend is not presented as
production-ready merely because it can execute a command: its isolation model,
lifecycle guarantees, operational dependencies, and known limits must also be
clear.

The production selections are fixed in the
[target-platform decision](architecture/target-platform.md): Daytona is the
first managed provider adapter, while Kubernetes SIG Agent Sandbox with
Kata/gVisor is the first self-hosted production adapter. Checkpoint behavior is
delegated to provider capabilities.

The current worker does **not** call a separate sandbox HTTP service. Its
sandbox boundary is an in-process Go interface:

```text
Temporal Activity -> SessionManager -> sandbox.Provider -> sandbox.Sandbox
```

`SessionManager` gives each session one logical sandbox. `Provider` decides how
that sandbox is provisioned, while `Sandbox` exposes command execution, confined
file access, a workspace root, and teardown. This is enough for the current
local and Docker backends and is the extension point for future backends.

## Support levels

- **Available**: implemented, documented, and exercised by repository tests.
- **Candidate**: technically compatible with the provider model, but not
  implemented or committed to a release.
- **Direction**: an intended capability that needs additional lifecycle or
  control-plane work before a backend alone would be useful.

These labels describe project support, not a security certification.

## Backend matrix

| Backend | Status | Isolation model | Session state | Intended use |
|---|---|---|---|---|
| Local process | Available; default | Temporary workspace and path checks; no kernel or network isolation | Workspace survives turns while the server process lives | Offline tests and trusted local development only |
| Docker | Available; opt-in | Container filesystem, namespaces/cgroups, configurable limits, network disabled by default | Container survives turns while the server process lives | Development and controlled single-tenant self-hosting |
| [Anthropic Sandbox Runtime (SRT)](https://github.com/anthropic-experimental/sandbox-runtime) | Candidate; not implemented | Native OS process restrictions (`sandbox-exec`, Bubblewrap, and platform-specific network controls) | A session workspace could persist, but each command is a new restricted process | Optional safer local execution; SRT is a beta research preview |
| [Daytona](https://www.daytona.io/docs/en/sandboxes/) | Selected managed target; not implemented | Provider-owned isolated compute, filesystem, and network controls | Durable provider identity with pause/archive lifecycle | Managed production |
| [Kubernetes SIG Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) with Kata/gVisor | Selected self-hosted target; not implemented | Kubernetes lifecycle API plus hardened runtime isolation | Stateful sandbox identity, lifecycle, and warm-pool support | Self-hosted production |
| E2B, Modal, Cloudflare, and other Managed Agents providers | Future adapters | Provider-owned isolation boundary | Provider-specific | Add only for a concrete deployment need |

The Docker provider has not been audited for hostile multi-tenant workloads.
The local provider is not a security boundary. No backend currently carries a
production security claim.

## Compatibility contract

A backend can implement today's execution contract when it can:

1. provision a workspace and execution environment with a documented isolation
   boundary;
2. execute a command with cancellation and bounded output;
3. read and write paths relative to that workspace;
4. preserve workspace state across runs in one session;
5. destroy the workspace idempotently.

The built-in toolset currently assumes a POSIX-like environment with
`/bin/sh`, `find`, and `grep`. A backend that does not provide those commands is
not compatible with all executing built-ins yet.

This basic contract should not be confused with the future managed-sandbox
contract. A remote or restart-resilient backend also needs a durable sandbox
identity, reattachment, health/state reporting, orphan reconciliation, and
checkpoint/restore semantics. Those capabilities should be added when a real
backend requires them rather than predicted in one universal interface now.

## Lifecycle today

- The first tool-using run in a session provisions the sandbox.
- Later runs in that session reuse it.
- Different sessions never share a logical sandbox.
- Becoming idle retains it.
- Deleting the session destroys it.
- Restarting the server loses the in-memory association. The next tool-using
  run receives a fresh workspace, and resources from an ungraceful prior
  process may be orphaned.

This matches the intended session ownership model but not durable sandbox
continuity.

## Evolution path

The core sandbox path is ordered by lifecycle value, not by the number of
backend logos:

1. **Environment-backed resolution and conformance.** Resolve a session's
   Environment into a provider-neutral sandbox profile instead of selecting one
   global backend at process startup. Exercise every available provider through
   the same lifecycle and tool contract tests.
2. **Durable logical identity and cleanup.** Persist enough provider identity
   to reattach after restart, make teardown retryable, and reconcile orphaned
   resources.
3. **Selected production adapters.** Implement Daytona first for managed
   execution and Kubernetes SIG Agent Sandbox first for self-hosting. Store
   provider identity durably and use provider-reported capabilities.
4. **Checkpoint, quotas, and eviction by capability.** Expose them when the
   selected provider supports them. Do not create a checkpoint format or
   emulate provider lifecycle inside the Agent control plane.

SRT is an optional candidate alongside this path, not a prerequisite. It can
provide a useful local security improvement, but its experimental status and
process-level lifecycle mean it should not displace the durable execution work
on the main roadmap.

The upstream behavior informing the session/environment distinction is
documented in Claude's
[cloud environment setup](https://platform.claude.com/docs/en/managed-agents/environments)
and
[self-hosted sandbox](https://platform.claude.com/docs/en/managed-agents/self-hosted-sandboxes)
guides. `managed-agent-go` remains an independent implementation and does not
claim Anthropic's hosted isolation properties.

## Adding a backend

A backend contribution should:

- keep its external dependency optional and fail fast when explicitly selected
  but unavailable;
- preserve the session-scoped ownership contract;
- document its trust boundary, network defaults, resource controls, host
  requirements, and unsupported lifecycle features;
- keep default tests offline and make daemon/network-dependent tests opt-in;
- avoid changing the AgentRuntime or public HTTP API for provider-specific
  mechanics;
- avoid a production or multi-tenant safety claim without evidence and an
  explicit security review.

Open an issue before a substantial backend integration so the intended use case
and lifecycle implications can be reviewed independently of the adapter code.
