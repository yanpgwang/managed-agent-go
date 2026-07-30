---
title: Runtime and sandbox
---

# Runtime and sandbox

The runtime is split into three replaceable boundaries: conversation
orchestration, model inference, and tool isolation.

## Agent runtime

`AgentRuntime.Run` receives a single trigger, the immutable session agent
snapshot, the conversation already projected from event history, the parsed
tool configuration, and a provisioned sandbox.

The default `AgentCore`:

1. sends alternating `messages[]` to the model client;
2. streams assistant text as an optional preview;
3. emits the complete `agent.message`;
4. executes allowed built-ins and feeds results back to the model;
5. repeats until the model ends the turn or requires client action.

The loop is capped at 20 model/tool rounds to prevent unbounded execution.

## Model clients

The model boundary is a small stateless interface with regular and streaming
message creation.

- The offline fake is deterministic and keeps the default binary and test suite
  network-free.
- The real client targets an Anthropic-shaped `/v1/messages` endpoint and
  supports API-key or bearer authentication.

Conversation ownership remains in this server. This is why earlier
self-contained harness integrations were removed: a second component owning
history would create competing sources of truth.

## Tool runtime

Agent tool configuration is parsed into:

- the `agent_toolset_20260401` built-in toolset;
- custom tool definitions;
- MCP toolset references.

`bash`, `read`, `write`, `edit`, `glob`, and `grep` currently execute.
`web_fetch` and `web_search` can be declared but return a not-implemented tool
result. MCP references are parsed but not resolved.

Built-ins with `always_allow` execute inside the current run. Custom tools and
`always_ask` built-ins park the session for a client response.

## Sandbox provider

The application provisions a sandbox only when the resolved toolset contains
tools. The same interface supports process execution and confined file reads
and writes. This is currently an in-process Go interface, not a separate
sandbox HTTP service. See the [sandbox backend matrix](../sandboxes.md) for
support levels, backend requirements, and the ordered evolution path.

### Local provider

The default provider creates a temporary work directory, rejects path escapes,
clears the child environment, limits command duration, and caps output.

:::danger[Not a security boundary]

The local provider shares the host process and filesystem namespaces. Never use
it to execute untrusted code.

:::

Because of this, **a real (network-backed) model paired with the local sandbox
is refused at startup by default**: a real model can be steered into running
tool commands on the host with no isolation. The offline deterministic fake
model plus the local sandbox (the zero-config default) always starts. To run a
real model, either select the Docker provider (below) or, as a dev-only explicit
override, set `MANAGED_AGENT_ALLOW_UNSAFE_LOCAL_SANDBOX=1` — this accepts the
risk of running tool commands on the host unisolated and must never be used with
untrusted input or in production.

### Docker provider

Set `MANAGED_AGENT_SANDBOX=docker` to run each sandbox in a container. The
provider uses a separate filesystem, Linux namespaces/cgroups, configurable
resource limits, and `--network none` by default. This is the recommended
sandbox for a real model, and it satisfies the startup guard without the unsafe
override.

Containers share the host kernel. This provider has not been audited for
hostile multi-tenant use; stronger isolation such as gVisor or a remote sandbox
can be added behind the same provider interface.

### Remote providers

E2B, CubeSandbox, OpenSandbox, and Daytona are selected through the same
deployment-level registry. The worker remains the lifecycle owner: it maps one
session to one external sandbox, persists only the provider name and opaque ID,
reattaches after restart, and deletes the resource through a durable Temporal
Activity. The external service owns isolation and workspace storage.

Provider credentials, endpoint routing, templates, images, and auto-pause
settings stay in worker environment variables. They do not change the Managed
Agents Environment or Session wire models.

## Session-scoped ownership

A sandbox is scoped to the session, not to a single run. The first run in a
session that needs tools provisions a logical sandbox; every later run in the
same session reuses that same instance, so filesystem state a tool creates in
one run is visible to the next. Different sessions acquire under different keys
and never share a sandbox, so they stay isolated even when they use the same
agent and environment.

Ownership lives in a session-scoped manager that wraps the provider inside the
`internal/sandbox` package. PostgreSQL stores the provider name, opaque external
ID, and spec hash; the in-memory map is only a live client cache. The
`AgentRuntime` is unaware of this — the application resolves the sandbox and
passes it in the run request.

Entering idle does not tear the sandbox down. After a worker restart, `Attach`
reconstructs the client from the persisted reference; if the resource has
disappeared, acquisition fails instead of silently replacing session state.

Session deletion runs provider teardown as a retryable Temporal Activity and
removes the binding before PostgreSQL permits the Session row to be deleted.
Local references require the same host filesystem and Docker references require
the same daemon. Remote multi-worker execution therefore needs a service-backed
provider. Checkpoint/restore, quotas, eviction, and orphan reconciliation are
not implemented.

## Streaming previews

The runtime may report `event_start` and `event_delta` through an optional
preview interface. These frames bypass durable storage and go directly to
subscribers that requested the matching event type. The complete event is still
buffered and committed through the normal completion transaction.

This split keeps the event log authoritative while improving latency, but it
also means clients must tolerate a preview ending without a persisted event if
the process or upstream stream fails.
