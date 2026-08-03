# Security policy

`managed-agent-go` is pre-release software and has not received a security
audit. It should not be exposed as a production multi-tenant service without an
independent review.

## Supported versions

Until the first stable release, security fixes target the latest commit on
`main`. Older commits and development database schemas are not supported.

## Reporting a vulnerability

Please use the repository's private GitHub Security Advisory reporting flow:

`https://github.com/yanpgwang/managed-agent-go/security/advisories/new`

Include affected versions, reproduction steps, impact, and any suggested
mitigation. Do not include credentials or sensitive production data. Please do
not open a public issue for an unpatched vulnerability.

If private reporting is unavailable, open a public issue requesting a private
maintainer contact without disclosing vulnerability details.

## Current security boundaries

- The default local sandbox is a development guardrail, not a security
  boundary. It must not execute untrusted code.
- The Docker provider gives container isolation and disables networking by
  default, but containers share the host kernel and the provider has not been
  audited for hostile multi-tenant workloads.
- Authentication validates a presented `x-api-key` against the keys configured
  in `MANAGED_AGENT_API_KEYS`. Keys are held only as SHA-256 digests and are
  compared in constant time; an unknown key is rejected exactly like a missing
  one. There is still no authorization, no tenancy, and no per-key scoping: any
  accepted key can reach every resource.
- If `MANAGED_AGENT_API_KEYS` is unset, authentication is **disabled** and every
  request is served unauthenticated. `serve` logs a warning at startup, binds
  loopback by default, and refuses to start with `-strict` unless keys are
  configured. Set keys before binding any non-loopback address.
- `authorization: Bearer <key>` is accepted only when
  `MANAGED_AGENT_AUTH_ALLOW_AUTHORIZATION_HEADER=true`. It is a non-upstream
  convenience extension; `x-api-key` is the documented header.
- `GET /healthz` and `GET /readyz` stay unauthenticated so an orchestrator can
  probe the process. They expose no session data.
- PostgreSQL journals tool attempts, but an external side effect can still be
  ambiguous if execution succeeds and its durable result is lost. Exactly-once
  behavior requires idempotency from the external system.
- Model credentials and API keys are read from environment variables. Operators
  are responsible for secret storage, rotation, logging policy, and endpoint
  trust. Mango never logs a key or its digest; only the non-secret key id
  appears in logs.

See the [architecture](docs/architecture.md) and
[roadmap](docs/roadmap.md) for planned hardening.
