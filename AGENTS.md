# Mango repository instructions

## Project mission

Mango is an open-source, clean-room implementation of the public Claude
Managed Agents (CMA) contract. The goal is to provide a durable, self-hosted CMA
runtime in Go whose observable API behavior is as compatible as practical with
the officially documented service.

Keep that goal explicit when making product or architecture decisions. Mango
is not an Anthropic product, must keep its own branding, and must not claim
compatibility beyond behavior that is implemented and tested. Reproduce public
contracts and semantics from official sources; keep the internal implementation
original.

## CMA reference material

Use the official CMA documentation when designing or reviewing behavior:

1. Start with the local index at
   `docs/_upstream/claude-managed-agents/snapshot/INDEX.md`.
2. Use `docs/provenance.md` for the exact sources behind existing decisions and
   `docs/compatibility.md` for the current implemented/unsupported boundary.
3. Refresh the local snapshot with `./scripts/sync-cma-docs.sh` when current
   hosted behavior matters. If the snapshot and hosted official documentation
   differ, the hosted documentation is authoritative.
4. Treat official documentation and official SDK behavior as public-contract
   evidence. Do not infer or copy Anthropic's private implementation.

For any change to the CMA-facing wire or lifecycle, identify the relevant guide
and API reference first. Record material new sources in `docs/provenance.md`,
update `docs/compatibility.md`, and add focused conformance tests.

## Contract-first design method

Design Mango from the documented CMA product boundary inward. Do not start with
a preferred database, workflow, sandbox, or service topology and then fit the
public API around it.

Before designing or implementing a CMA capability:

1. Review the relevant official guide, every affected API endpoint, and public
   official SDK types or examples. Consult adjacent guides when the capability
   crosses resource, sandbox, event, or lifecycle boundaries.
2. Place the capability on the current CMA scope map. Keep these dimensions
   separate:
   - the complete official CMA surface;
   - Mango's implemented, limited, and unsupported coverage;
   - the current delivery priority; and
   - non-CMA operational work required to run Mango safely in production.
   Do not describe an official CMA surface as outside CMA merely because Mango
   has deferred it.
3. Extract observable behavior before choosing infrastructure: resource shapes
   and relationships, lifecycle and deletion rules, state transitions, event
   ordering, consistency and concurrency behavior, idempotency, actor
   attribution, sandbox/filesystem effects, limits, errors, pagination,
   authentication boundaries, and recovery behavior.
4. Translate those observations into implementation invariants and ownership
   boundaries. Explicitly label each important statement as one of:
   - **documented contract**: stated by official public material;
   - **design inference**: required or strongly implied by observable behavior;
   - **local choice**: Mango's original implementation decision.
5. Choose the simplest clean-room design that satisfies the invariants and
   composes with the existing runtime. Anthropic's private service topology is
   neither known nor a target; observable compatibility is the target.
6. Define conformance evidence before implementation. Cover the public wire and
   the runtime semantics that force the design, including concurrency,
   restart/recovery, and sandbox behavior where relevant.

For a cross-cutting capability, record a compact evidence-to-design table in
the relevant architecture document or PR before substantial code is written:

| Official observable behavior | Required invariant | Mango design | Evidence |
| --- | --- | --- | --- |
| Guide or API behavior | What must remain true | Original local mechanism | Test or verification |

Maintain a whole-product scope map from the snapshot index and compatibility
matrix, refreshing it when the upstream snapshot changes materially. Individual
small PRs need only audit the touched capability and adjacent contracts; they
should not repeatedly block on a full-catalog review.

## Engineering expectations

- Inspect the current execution path before changing it, especially
  `internal/temporal`, `internal/httpapi`, `internal/pg`, and
  `cmd/managed-agent/orchestrate.go`. Documentation and roadmaps can lag code.
- Match documented resource shapes, defaults, errors, optimistic concurrency,
  pagination, event ordering, SSE reconnection, permission waits, and session
  lifecycle semantics. Do not silently invent CMA wire fields.
- Follow durable-execution best practices: PostgreSQL remains authoritative for
  accepted public state and events; Temporal owns recoverable in-flight
  orchestration; NATS is an ephemeral delivery optimization. Make side effects
  idempotent where possible and never blindly retry an ambiguous external side
  effect.
- Preserve explicit execution evidence and state transitions. Tool calls use a
  durable journal and blocking client actions must survive worker/API restarts.
- Treat credentials as deployment- or worker-managed configuration. Never
  expose secrets in events, logs, test fixtures, or CMA resource payloads.
- Treat the local sandbox as a development guardrail, not a security boundary.
  Prefer isolated sandbox backends for untrusted workloads and apply least
  privilege to networking, filesystem access, and tools.
- Prefer small, reviewable changes with offline deterministic tests. Add live or
  black-box tests only behind explicit opt-in configuration.
- Preserve unrelated work in a dirty worktree.

## Verification

Run the narrowest relevant tests during development, then use the repository
checks appropriate to the change:

```bash
gofmt -w <changed-go-files>
go test ./path/to/changed/package
make verify
make docs-check
```

Do not weaken tests, compatibility notes, security boundaries, or durable
failure semantics merely to make an implementation pass.
