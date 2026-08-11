---
title: Sandbox backends
slug: /sandboxes
---

# Sandbox backends

Sandbox support is intentionally incremental. A backend is not presented as
production-ready merely because it can execute a command: its isolation model,
lifecycle guarantees, operational dependencies, and known limits must also be
clear.

Local and Docker execution do not add another HTTP service. Remote adapters
call an independently deployed sandbox service through the same in-process Go
boundary:

```text
Temporal Activity -> SessionManager -> sandbox.Provider -> sandbox.Sandbox
```

`SessionManager` gives each session one logical sandbox. PostgreSQL persists the
provider name and opaque external ID; a restarted worker calls `Attach` instead
of creating an empty replacement. `Sandbox` exposes command execution, confined
file access, a workspace root, and teardown. The execution worker selects one
compiled adapter through an internal registry. `MANAGED_AGENT_SANDBOX` accepts
`local` (the default), `docker`, `e2b`, `cube`, `opensandbox`, or `daytona`; an
unknown name fails startup instead of falling back to host execution. Provider
selection does not add fields to the Managed Agents Environment or Session
APIs.

The `serve` and `orchestrate` processes for one deployment must use the same
`MANAGED_AGENT_SANDBOX` value. API admission reads that provider's declared
capabilities without loading worker credentials; the worker verifies the same
capability again before provisioning.

## Support levels

- **Available**: implemented, documented, and exercised by repository tests.
- **Preview**: implemented with offline and opt-in live conformance, but still
  awaiting promotion based on repeatable service-level validation.
- **Planned**: selected for a dedicated adapter, but not implemented.
- **Evaluating**: useful integration shape, without a committed adapter.

These labels describe project support, not a security certification.

## Backend matrix

| Backend | Status | Isolation model | Limited egress | Session state | Intended use |
|---|---|---|---|---|---|
| Local process | Available; default | Host process plus confined workspace; not an isolation boundary | No; rejected | Reattaches by durable workspace path on the same host | Offline tests and trusted local development only |
| Docker | Available; opt-in | Container filesystem, namespaces/cgroups, configurable limits; provider calls default to no network while cloud Environments request bridge networking | No; rejected | Reattaches by container ID on the same Docker daemon | Controlled single-host self-hosting |
| [E2B](https://github.com/e2b-dev/E2B) | Preview | Managed microVM service | No; rejected | E2B ID plus auto-pause filesystem persistence | Managed production |
| [Tencent CubeSandbox](https://github.com/TencentCloud/CubeSandbox) | Preview | E2B-compatible microVM service | No; rejected | Provider-owned durable sandbox ID | Self-hosted production on Linux/KVM |
| [OpenSandbox](https://github.com/opensandbox-group/OpenSandbox) | Preview; Docker runtime manually live-verified | Docker or Kubernetes-backed sandbox service | Yes; host allowlist | Provider-owned durable sandbox ID | Self-hosted production |
| [Daytona](https://www.daytona.io/docs/en/sandboxes/) | Preview | Managed or self-hosted sandbox service | No; rejected | Deterministic name, durable ID, and auto-pause | Managed production |
| [Modal](https://modal.com/docs/guide/sandboxes) | Planned | Managed sandbox service | Planned | Provider-owned durable sandbox ID | Managed production |
| [Runloop](https://docs.runloop.ai/docs/devboxes/overview) | Planned | Managed devbox service | Planned | Suspend, resume, and snapshot lifecycle | Managed production |
| [Kubernetes SIG Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | Planned | Kubernetes CRD, controller, and routing layer | Planned | Stateful sandbox resource | Kubernetes deployments |
| Anthropic Sandbox Runtime, Vercel Sandbox, and Cloudflare Sandbox | Evaluating | Backend-specific | Evaluating | Backend-specific | Later adapters |

The Docker provider has not been audited for hostile multi-tenant workloads.
The local provider is not a security boundary. No backend currently carries a
production security claim.

## File Resource mounts

File-backed Session Resources require more than ordinary workspace writes: the
provider must stream an independently stored object to its documented absolute
path, publish it atomically, make it read-only inside the sandbox, and remove it
idempotently after a crash. Provider capability admission is explicit.

Docker is currently the only adapter that advertises this capability. It stages
validated bytes in a provider-owned host directory and bind-mounts that
directory read-only at `/mnt/session/uploads`. The local-process provider would
have to write into the worker host's absolute `/mnt` path and therefore rejects
the feature. Current remote adapters also reject it until their service APIs
can prove an equivalent isolated read-only mount contract.

## Custom Skill mounts

Custom Skill execution uses a separate provider capability because a bundle is
an immutable directory tree, not a File Resource. Docker stages pinned
canonical archives beneath the same provider-owned per-Session root, verifies
their compressed size and SHA-256, revalidates archive paths and entry types,
and atomically publishes each tree beneath `/workspace/skills/<name>/`. The
complete `skills` directory is an unconditional read-only bind mount on new
containers, so attach after worker restart can recover the same host root.

The worker checks the marker and materialized tree before every relevant tool
step, repairs missing or damaged staging, and removes abandoned extraction
directories. Sandbox destruction removes the shared root containing both File
and Skill staging. Containers created before this mount existed fail closed for
pinned Skills and must be recreated; Docker cannot add a bind mount to a live
container. Local, self-hosted, and current remote adapters do not advertise the
capability.

## Memory Store mounts

Memory Stores use a distinct provider capability because they are durable,
cross-Session, writable resources rather than immutable attachments. Docker is
currently the only adapter that advertises it. Each attached Store is exposed
at `/mnt/memory/<store-slug>` as ordinary UTF-8 files. A `read_only` attachment
is a read-only bind mount even to container root; a `read_write` attachment is
writable during the tool step.

Before the first tool in a concurrent wave runs, the worker writes any surviving
local changes from an earlier interrupted Activity, merges the current
PostgreSQL heads, and records their IDs and SHA-256 values in provider-owned
state outside the mount. Concurrent Threads then share that filesystem wave;
the mount is not refreshed underneath an active tool. After every active tool
in the wave has released its shared resource lock, changed, created, and deleted
files are committed in one PostgreSQL transaction as immutable `session_actor`
Versions and the baseline is refreshed under an exclusive provider lock. A
concurrent external change to the same baseline returns a precondition error
instead of silently overwriting it. Session deletion performs a final
writeback before destroying the sandbox so a crash between tool execution and
ordinary writeback does not discard Memory changes.

## Compatibility contract

A backend implements the core lifecycle contract when it can:

1. expose a stable provider name;
2. idempotently create one resource for a session key;
3. attach to a persisted opaque reference after restart;
4. execute a command with cancellation and bounded output;
5. read and write paths relative to the workspace;
6. destroy the resource idempotently.

These requirements are executable in `internal/sandbox/sandboxtest`. Local,
Docker, and every remote provider's opt-in live test run the same suite,
including cross-client Create/Attach, workspace preservation, ownership
rejection, cancellation, and post-delete missing-reference behavior. Offline
adapter tests cover the same contract without credentials. Provider-specific
tests cover protocol translation, isolation, and resource controls separately.

The built-in toolset currently assumes a POSIX-like environment with
`/bin/sh`, `find`, and `grep`. A backend that does not provide those commands is
not compatible with all executing built-ins yet.

## Lifecycle today

- The first tool-using run idempotently creates the provider resource and
  persists `{provider, external_id, spec_hash}` in PostgreSQL. Before calling
  the provider it writes a non-secret provisioning intent, installs the
  Session's snapshotted Environment packages, and publishes the binding only
  after every package-manager command succeeds. A worker reconciler recovers
  and completes any resource left by a crash between those commits.
- Package configuration supports `apt`, `cargo`, `gem`, `go`, `npm`, and `pip`.
  Commands use argument vectors rather than shell interpolation. The selected
  image must contain each requested manager, and package validation remains the
  caller's responsibility. An install failure leaves the provisioning intent
  for retry and does not expose the sandbox to tool execution.
- A deployment using the local-process backend rejects non-empty package
  configuration at API admission because installing there would mutate the
  worker host. Use Docker or a remote isolated backend for package-configured
  cloud Environments.
- Limited networking is admitted only when the selected provider declares and
  implements exact host-level egress reconciliation. OpenSandbox creates a
  deny-by-default policy, temporarily expands it for configured package setup,
  restores the final allowlist before binding, and reconciles MCP-derived
  changes on later turns and worker attach. Other implemented backends reject
  the policy at API admission.
- Remote services receive a fixed-length hash of the session key as their
  ownership label; credentials and raw session identifiers are not persisted in
  the provider reference.
- Later turns reuse the cached client; a restarted worker attaches through the
  persisted reference.
- Different sessions never share a logical sandbox.
- Becoming idle retains it.
- Deleting the session fences admission, stops its Session Workflow, durably
  retries provider teardown on the worker, removes the binding, and only then
  deletes the session row.
- A worker that discovers an interrupted deletion restarts or joins its
  deterministic cleanup Workflow and finalizes the fenced PostgreSQL row. An
  unbound provisioning intent is recovered and destroyed before finalization.
- A persisted reference that no longer exists fails explicitly; Mango does not
  silently replace lost workspace state with an empty sandbox.
- A deployment must keep a worker for every provider name still referenced by
  a binding or provisioning intent. Changing the configured provider does not
  migrate existing resources; remove their sessions or restore the old provider
  before retiring that adapter.

## Required production lifecycle

Remote adapters must use the same lifecycle tests. Production deployments still
need provider health reporting and provider-aware task routing when
heterogeneous workers share a control plane. Pause, snapshot, fork, quotas, and
eviction remain optional capabilities rather than requirements of the core
interface.

## Remote provider configuration

Credentials stay in worker configuration. They are never written to PostgreSQL
or returned by the Managed Agents API.

| Provider | Required | Common optional values |
|---|---|---|
| `e2b` | `E2B_API_KEY` | `E2B_API_URL`, `E2B_TEMPLATE_ID`, `E2B_DOMAIN`, `E2B_IDLE_TIMEOUT` |
| `cube` | `CUBE_API_URL`, `CUBE_TEMPLATE_ID` | `CUBE_API_KEY`, `CUBE_SANDBOX_DOMAIN`, `CUBE_PROXY_*`, `CUBE_IDLE_TIMEOUT` |
| `opensandbox` | `OPEN_SANDBOX_DOMAIN` | `OPEN_SANDBOX_API_KEY`, `OPEN_SANDBOX_IMAGE`, `OPEN_SANDBOX_USE_SERVER_PROXY` |
| `daytona` | `DAYTONA_API_KEY` | `DAYTONA_API_URL`, `DAYTONA_TARGET`, `DAYTONA_SNAPSHOT`, `DAYTONA_IMAGE`, `DAYTONA_AUTO_PAUSE_MINUTES` |

For local development, `make dev-env-init` creates
`~/.config/mango/dev.env` from `config/dev.env.example` with mode `0600`.
`scripts/with-dev-env <command>` loads it explicitly and works across
worktrees.

Live conformance is opt-in:

```bash
scripts/with-dev-env env MANGO_LIVE_E2B=1 \
  go test ./internal/sandbox -run '^TestE2BLiveConformance$' -count=1

scripts/with-dev-env env MANGO_LIVE_OPENSANDBOX=1 \
  go test ./internal/sandbox -run '^TestOpenSandboxLiveConformance$' -count=1
```

The equivalent gates are `MANGO_LIVE_CUBE` and `MANGO_LIVE_DAYTONA`.
Ordinary tests never contact a service or create billable resources.

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
- register a lazy factory under a stable lowercase provider name and pass the
  shared `sandboxtest` lifecycle suite;
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
