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
- Node.js 20 or newer for the documentation site;
- Docker only for optional Docker sandbox tests.

Run the core checks:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Run the documentation checks:

```bash
cd website
npm ci
npm run typecheck
npm run build
```

Default tests must stay offline and deterministic. Tests that require a Docker
daemon or real model endpoint must be opt-in and skip cleanly when unavailable.

## Compatibility changes

When changing the public HTTP surface:

1. cite an official source in `docs/provenance.md`;
2. add or update raw HTTP golden tests for exact JSON and status behavior;
3. add an official SDK black-box test when the SDK exposes the capability;
4. update `docs/compatibility.md` when the supported integration surface or a
   user-visible limitation changes;
5. update the API docs and embedded `internal/httpapi/openapi.yaml`.

Do not copy upstream implementation code or internal types. Official public
documentation and public SDK behavior may establish the wire contract; internal
design must remain this project's own.

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

By participating, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).
