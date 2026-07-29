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

Anthropic Sandbox Runtime (SRT) is a technically compatible candidate for a
future local process provider, but it is not implemented. SRT is an
experimental command wrapper rather than a persistent container service, so it
would preserve a session workspace without providing a durable container root
filesystem or remote sandbox identity.

## Session-scoped ownership

A sandbox is scoped to the session, not to a single run. The first run in a
session that needs tools provisions a logical sandbox; every later run in the
same session reuses that same instance, so filesystem state a tool creates in
one run is visible to the next. Different sessions acquire under different keys
and never share a sandbox, so they stay isolated even when they use the same
agent and environment.

Ownership lives in a session-scoped manager that wraps the provider inside the
`internal/sandbox` package: acquisition provisions on first use and returns the
cached instance afterwards; release destroys it. The `AgentRuntime` is unaware
of this — the application resolves the sandbox and passes it in the run request.

Entering idle does not tear the sandbox down; it stays live between turns.
Deleting the session releases it, running the provider teardown exactly once. A
provisioning failure is not cached, so a later run may retry.

The manager holds sandboxes in memory. Restart does not restore an idle
session's sandbox: a process restart starts from an empty workspace, and the
first run after restart provisions a fresh one. Durable checkpoint/restore,
quotas, and eviction are not implemented.

This is a process-boundary limitation, not just a persistence gap. Because
ownership lives only in the in-memory manager, a new process cannot reattach to
sandboxes an earlier process provisioned. A crash or an ungraceful restart
therefore leaves those provider resources — Docker containers or local temp
directories — orphaned, since the only code that would tear them down (`Release`
on session deletion) died with the process. Nothing reclaims them until an
external cleanup step; there is no built-in reaper.

## Streaming previews

The runtime may report `event_start` and `event_delta` through an optional
preview interface. These frames bypass durable storage and go directly to
subscribers that requested the matching event type. The complete event is still
buffered and committed through the normal completion transaction.

This split keeps the event log authoritative while improving latency, but it
also means clients must tolerate a preview ending without a persisted event if
the process or upstream stream fails.
