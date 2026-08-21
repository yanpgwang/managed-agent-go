# Repository instructions

Follow `CONTRIBUTING.md` for testing, durability, security, documentation, and
pull-request requirements.

## Product boundary

- Mango is an independent, self-hosted runtime for durable AI agents. Mango
  owns its public API, lifecycle semantics, roadmap, and release policy.
- Mango must never proxy or delegate Sessions, Files, sandbox execution,
  scheduling, persistence, or other runtime behavior to a hosted agent service.
- The current model adapter calls a Messages-shaped `/v1/messages` endpoint.
  That adapter is replaceable infrastructure, not a requirement that Mango's
  public API or future model integrations remain tied to Anthropic.
- External API documentation and SDKs may be used as clean-room design
  references. They do not define Mango's target contract and are not runtime
  dependencies. Development, CI, and production must not require hosted agent
  credentials.

## Development API policy

- Mango currently has no customers and no supported stable release. Until the
  maintainers explicitly change that status, backward compatibility does not
  exist as a product requirement.
- `/v1` is the single development API namespace. Change its routes, fields,
  schemas, and behavior in place when that improves Mango. Do not create `/v2`,
  dual behavior, deprecation windows, legacy shims, or translation layers for
  compatibility with an earlier commit or an external SDK.
- Existing tests, fixtures, database rows, and vendor-shaped fields are evidence
  of the current implementation, not compatibility obligations. Update or
  remove them with the design they cover. Development databases may be rebuilt;
  do not retain code solely to read data written by an earlier checkout.
- Keep an existing behavior only because it remains the right Mango design, not
  because changing it would be breaking. Only an explicit maintainer decision
  establishing a supported release or real customer migration can change this
  rule.

## Product-driven development

- Mango's documented HTTP API and observable runtime behavior define the
  product contract. GitHub Issues define active engineering work and may form a
  Mango-specific roadmap.
- `docs/product.md` defines product direction. `docs/capabilities.md` records
  Mango's current capabilities and limitations; it is not a delta ledger
  against another service.
- Before substantial API, persistence, or runtime work, verify current behavior
  in source code, migrations, OpenAPI definitions, and executable HTTP,
  persistence, workflow, and service tests. Use Mango's implementation and
  documentation as the authority for current behavior.
- Select one user-visible, end-to-end problem. State its acceptance criteria
  and non-goals before implementation. A feature needs a Mango user or operator
  rationale; similarity to an external product is not sufficient.
- Implement the smallest safe slice that solves the selected problem. Expand
  internal architecture only when required for observable correctness,
  durability, recovery, security, or operability.
- Mango is pre-release. Public API, storage, and workflow changes may be
  breaking when they materially improve the product. Update code, migrations,
  OpenAPI, documentation, and tests together on the existing `/v1` surface.
- External contracts may inspire resource models, workflows, or edge cases.
  Record useful provenance, but adapt the design to Mango's self-hosted trust
  boundary and reject constraints that do not serve Mango users.
- Do not add research-preview or vendor-specific surfaces unless a Mango issue
  explicitly selects them for an independent product reason.
- Stop when the acceptance criteria and required tests pass. Record adjacent
  work as separate Issues instead of expanding the current change.
- A completed user-visible change must update the affected API documentation,
  `internal/httpapi/openapi.yaml`, and the capability summary when applicable.
