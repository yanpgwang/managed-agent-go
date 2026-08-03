---
title: Core compatibility statement v1.0.0
slug: /compatibility/core-v1
---

# Core compatibility statement v1.0.0

| Field | Value |
| --- | --- |
| Statement ID | `mango-core-1.0.0` |
| Published | 2026-08-03 |
| Upstream beta | `managed-agents-2026-04-01` |
| SDK evidence | Anthropic Go SDK `v1.61.0` |
| Status | Published |

This statement applies to source revisions that contain this document and pass
the repository CI gates. It freezes Mango's first compatibility claim for the
core single-agent Claude Managed Agents integration surface.

## Claim

Mango implements all 21 SDK-visible operations in the core Agent, Environment,
Session, and Session Event lifecycle. For the fields and behaviors included
below, requests can be made with the official Anthropic Go SDK against Mango's
base URL, and accepted work follows the documented durable single-agent state
transitions.

This is a scoped interoperability claim. It is not a claim of universal API
parity, hosted-infrastructure equivalence, or production security readiness.

## Included surface

| Resource | Included operations |
| --- | --- |
| Agents | Create, list, get, update, list versions, archive |
| Environments | Create, list, get, update, archive, delete |
| Sessions | Create, list, get, update, archive, delete |
| Session Events | Send, list, stream |

The runtime claim covers one primary Session agent: model turns, the core
single-agent event union, sandbox built-ins, provider-native Web Search/Fetch,
custom tools, unauthenticated public MCP tools, confirmations, untargeted
interrupts, text-rubric outcomes, context projection, and restart recovery.

The normative operation ledger is the
[core API conformance matrix](../api/core-conformance.md). The living
[coverage matrix](../compatibility.md) remains the source for current behavior
after this statement.

## Required deployment profile

- The API and worker use PostgreSQL, Temporal, and NATS as documented, and use
  the same sandbox-provider selection.
- Real model execution requires a Messages-compatible endpoint that supports
  the selected model and any provider-native tools requested by the Agent.
- Strict transport validation requires Mango's `-strict` mode. This validates
  compatibility headers and credential presence; it is not authentication.
- Package-configured Environments require Docker or a remote isolated provider.
  Limited networking requires OpenSandbox. Incapable providers reject the
  configuration with `422` instead of storing unenforced intent.

## Evidence

- Raw HTTP and OpenAPI tests lock request, response, default, null, error, and
  event-union behavior.
- Official Go SDK black-box tests cover every core lifecycle and paging path.
- PostgreSQL and Temporal suites cover admission, state transitions, event
  ordering, pending-action barriers, retry and interrupt races, restart replay,
  sandbox ownership, and deletion fences.
- Service conformance runs against PostgreSQL, Temporal, NATS, and Docker.
- Race, lint, vet, documentation, container, and dependency-security gates run
  in CI.
- Safe live text and sandbox-tool turns and OpenSandbox lifecycle behavior have
  separate opt-in evidence. Hosted Managed Agents differential testing is not
  part of this statement because no suitable credential was available.

## Known differences and limitations

### Resource and wire boundaries

- Non-empty Session `resources` and create-time `vault_ids` are rejected.
  Upstream accepts create-time vault IDs. File-sourced message content and
  file-backed outcome rubrics are rejected because the Files and Session
  Resources surfaces are outside this claim.
- Agent skills and `multiagent` configuration can round-trip, but skill
  execution, resolved rosters, Session Threads, delegation, and targeted
  thread interrupts are outside this claim and do not execute.
- Session list filters for deployment and memory-store membership are parsed,
  but no current Session can match them because those resources are outside the
  included surface.
- Environment and Session list limits use documented local defaults and maxima
  where the upstream reference and SDK types do not specify bounds. Exact
  undocumented error wording and hosted pagination-token encoding are not
  claimed.

### Runtime and integration boundaries

- Anthropic documents package caching across Sessions that share an
  Environment. Mango snapshots the Environment and installs its packages once
  per Session sandbox instead; this can add provisioning latency and registry
  traffic. The selected image must contain each configured package manager.
- Limited egress is enforced only by OpenSandbox. Package setup uses a fixed
  public-registry allowlist; custom registries and direct Go VCS hosts require
  explicit `allowed_hosts`. Live limited-egress conformance depends on the
  operator's OpenSandbox runtime.
- The local sandbox is not a security boundary. Remote sandbox adapters remain
  Preview, and provider selection is process-global.
- Web Search/Fetch require `always_allow` and a Messages endpoint that supports
  the native server tools. Mango does not supply a separate managed Web
  executor for endpoints that lack them.
- MCP supports unauthenticated public Streamable HTTP. Vault-backed
  authentication, private-network connectivity, deprecated-SSE fallback,
  resources, and prompts are not supported.
- The built-in sandbox tools are Mango implementations with a POSIX-like
  command dependency, not a claim of byte-for-byte equivalence to Anthropic's
  hosted tool runtime.
- Context budgeting uses a conservative server-owned estimate and extractive
  compaction. It is not a provider tokenizer or an endpoint/model-specific
  context profile.
- Event streams do not replay history and do not interpret `Last-Event-ID`.
  Clients use the documented open-stream-then-list recovery procedure. Preview
  frames are intentionally ephemeral.

### Operational and security boundaries

- Header checks do not provide authentication, authorization, tenant
  isolation, quotas, or audit controls.
- The repository does not yet publish a supported production deployment,
  Worker Versioning policy, rolling-upgrade proof, backup plan, or production
  observability package.
- Files, Skills execution, Memory Stores, Vaults, Deployments, Deployment Runs,
  Session Threads, the self-hosted Environment Work API, schedules, and
  webhooks remain outside this statement.

## Version policy

The scope and claims of `mango-core-1.0.0` will not be broadened in place.
Factual corrections may clarify this page without expanding the claim. A new
upstream beta target, SDK contract baseline, included resource surface, or
material runtime guarantee requires a new compatibility statement version.
