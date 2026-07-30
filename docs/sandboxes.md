---
title: Sandbox backends
slug: /sandboxes
---

# Sandbox backends

Sandbox support is intentionally incremental. A backend is not presented as
production-ready merely because it can execute a command: its isolation model,
lifecycle guarantees, operational dependencies, and known limits must also be
clear.

Local and Docker execution do not add another HTTP service. The worker uses the
same in-process Go boundary that remote service adapters will implement:

```text
Temporal Activity -> SessionManager -> sandbox.Provider -> sandbox.Sandbox
```

`SessionManager` gives each session one logical sandbox. PostgreSQL persists the
provider name and opaque external ID; a restarted worker calls `Attach` instead
of creating an empty replacement. `Sandbox` exposes command execution, confined
file access, a workspace root, and teardown.

## Support levels

- **Available**: implemented, documented, and exercised by repository tests.
- **Planned**: selected for a dedicated adapter, but not implemented.
- **Evaluating**: useful integration shape, without a committed adapter.

These labels describe project support, not a security certification.

## Backend matrix

| Backend | Status | Isolation model | Session state | Intended use |
|---|---|---|---|---|
| Local process | Available; default | Host process plus confined workspace; not an isolation boundary | Reattaches by durable workspace path on the same host | Offline tests and trusted local development only |
| Docker | Available; opt-in | Container filesystem, namespaces/cgroups, configurable limits, network disabled by default | Reattaches by container ID on the same Docker daemon | Controlled single-host self-hosting |
| [E2B](https://github.com/e2b-dev/E2B) | Planned | Remote sandbox service | Provider-owned durable sandbox ID | Managed production |
| [Tencent CubeSandbox](https://github.com/TencentCloud/CubeSandbox) | Planned | E2B-compatible microVM service | Provider-owned durable sandbox ID | Self-hosted production |
| [OpenSandbox](https://github.com/opensandbox-group/OpenSandbox) | Planned | Docker or Kubernetes-backed sandbox service | Provider-owned durable sandbox ID | Self-hosted production |
| [Daytona](https://www.daytona.io/docs/en/sandboxes/) | Planned | Managed sandbox service | Durable identity and lifecycle API | Managed production |
| [Modal](https://modal.com/docs/guide/sandboxes) | Planned | Managed sandbox service | Provider-owned durable sandbox ID | Managed production |
| [Runloop](https://docs.runloop.ai/docs/devboxes/overview) | Planned | Managed devbox service | Suspend, resume, and snapshot lifecycle | Managed production |
| [Kubernetes SIG Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | Planned | Kubernetes CRD, controller, and routing layer | Stateful sandbox resource | Kubernetes deployments |
| Anthropic Sandbox Runtime, Vercel Sandbox, and Cloudflare Sandbox | Evaluating | Backend-specific | Backend-specific | Later adapters |

The Docker provider has not been audited for hostile multi-tenant workloads.
The local provider is not a security boundary. No backend currently carries a
production security claim.

## Compatibility contract

A backend implements the core lifecycle contract when it can:

1. expose a stable provider name;
2. idempotently create one resource for a session key;
3. attach to a persisted opaque reference after restart;
4. execute a command with cancellation and bounded output;
5. read and write paths relative to the workspace;
6. destroy the resource idempotently.

The built-in toolset currently assumes a POSIX-like environment with
`/bin/sh`, `find`, and `grep`. A backend that does not provide those commands is
not compatible with all executing built-ins yet.

## Lifecycle today

- The first tool-using run idempotently creates the provider resource and
  persists `{provider, external_id, spec_hash}` in PostgreSQL.
- Later turns reuse the cached client; a restarted worker attaches through the
  persisted reference.
- Different sessions never share a logical sandbox.
- Becoming idle retains it.
- Deleting the session fences admission, stops its Session Workflow, durably
  retries provider teardown on the worker, removes the binding, and only then
  deletes the session row.
- A persisted reference that no longer exists fails explicitly; Mango does not
  silently replace lost workspace state with an empty sandbox.

## Required production lifecycle

Remote adapters must use the same lifecycle tests. Production deployments still
need orphan reconciliation for the create-before-binding crash window, provider
health reporting, and a provider registry suitable for heterogeneous workers.
Pause, snapshot, fork, quotas, and eviction remain optional capabilities rather
than requirements of the core interface.

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
