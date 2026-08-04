# Contributing to managed-agent-go

Thanks for helping improve the project. This repository is a clean-room,
self-hosted runtime with a Claude-compatible integration surface, so changes
should preserve a clear line between observed public behavior and original
internal design.

## Before opening a change

For substantial API, persistence, or runtime changes, open an issue describing:

- the user-visible problem;
- the official source for any compatibility claim;
- the durability, retry, and security implications;
- a small independently testable delivery slice.

Small bug fixes and documentation improvements can go directly to a pull
request.

## Development setup

Requirements:

- Go 1.26 or newer;
- [golangci-lint](https://golangci-lint.run/docs/welcome/install/local/)
  2.12.x for local lint checks;
- Node.js 20.17 through 24 for the documentation site (Node.js 22 LTS is used
  in CI);
- Docker with Compose for service-conformance and Docker sandbox tests.

Run the core checks:

```bash
make verify
```

`make lint` checks changes relative to `origin/main`, matching the incremental
CI rollout. Set `LINT_BASE` when your comparison branch differs.

Run the documentation checks:

```bash
make docs-check
```

The documentation lives in `docs/` and uses Mintlify with the Sequoia theme.
Preview it locally with `npm --prefix docs run dev`. Publishing is handled by
the Mintlify GitHub App with `docs` configured as the content root; the retired
GitHub Pages workflow is intentionally absent.

Run reachable Go vulnerability scanning and fail on high-severity production
dependency advisories for the documentation toolchain:

```bash
make security
```

Validate the deployment configuration and container entrypoint:

```bash
make local-config
make image-smoke
```

Run the same PostgreSQL, Temporal, NATS, MinIO, and Docker conformance suite as
CI:

```bash
docker compose -f deployments/local/compose.yaml up -d --wait postgres temporal nats minio
make test-service
```

Default tests must stay offline and deterministic. Service tests must use
isolated database schemas and clean up their workflows, File objects, and
sandboxes. A real model endpoint is a separate, explicitly enabled test tier
because it uses a credentialed network call and may incur cost:

```bash
make test-model-live
make test-platform-live
```

The live targets require the `MANAGED_AGENT_MODEL_*` variables documented in
the deployment guide. They are intentionally not run in public CI and must
never print or persist API keys.

## Compatibility changes

When changing the public HTTP surface:

1. cite the relevant official source in the change or compatibility table;
2. add or update raw HTTP golden tests for exact JSON and status behavior;
3. add an official SDK black-box test when the SDK exposes the capability;
4. update `docs/compatibility.mdx` when the supported integration surface or a
   user-visible limitation changes;
5. update the embedded `internal/httpapi/openapi.yaml` route inventory.

Do not copy upstream implementation code or internal types. Official public
documentation and public SDK behavior may establish the wire contract; internal
design must remain this project's own.

## Sandbox backend changes

Open an issue before adding a substantial sandbox backend. Describe the target
use case, trust boundary, host dependencies, network defaults, resource
controls, session persistence, and restart behavior.

Backend changes should preserve the provider contract and session-scoped
ownership described in the [sandbox backend guide](docs/sandboxes.mdx). Keep
external runtimes optional, keep default tests offline, add shared lifecycle
and tool-contract coverage, and label experimental integrations honestly.
Command execution alone is not evidence that a backend is production-ready or
safe for hostile multi-tenant workloads.

## Architecture expectations

- Keep wire DTOs in `internal/httpapi` and persistence/execution facts out of
  public responses.
- Preserve the event log as the authoritative public history.
- Do not perform model, sandbox, or other external work inside SQL transactions
  or application locks.
- Treat crash recovery and side-effect idempotency as part of a feature, not a
  later operational detail.
- Add interfaces at infrastructure/trust boundaries, not around every domain
  type.

## Pull requests

Keep each pull request focused. Include:

- a concise problem and solution statement;
- tests that fail without the change;
- compatibility and migration impact;
- security considerations for tools, sandboxes, credentials, or external calls;
- documentation updates for user-visible behavior.

Use `gofmt` for Go code. Generated and dependency artifacts should not be
committed except for lockfiles required for reproducible builds.

Keep deployment assets within the support boundaries documented in
[`deployments/README.md`](deployments/README.md). The local Compose stack may
build the current checkout; future production bundles must consume versioned
release images and document their upgrade lifecycle.

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).
