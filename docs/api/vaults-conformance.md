---
title: Vault conformance
slug: /api/vaults-conformance
---

# Vault conformance

This matrix records the encrypted Vault, Credential, and MCP runtime slice.
These routes use `anthropic-beta: managed-agents-2026-04-01` and are available
only when `MANAGED_AGENT_VAULT_KEYRING_FILE` is configured.

## Operation matrix

| Surface | Operations | Evidence |
| --- | --- | --- |
| Vaults | create, get, update, list, archive, delete | Official Go SDK lifecycle and auto-pagination; PostgreSQL lifecycle, cascade, cursor, and archive tests |
| Credentials | create, get, update, list, archive, delete | Official Go SDK static-bearer and OAuth lifecycle, nullable-field, and auto-pagination tests; strict tagged-union parsing; PostgreSQL lifecycle and encrypted response-redaction tests |
| Session and MCP runtime | ordered `vault_ids`; canonical URL matching; static bearer injection; expired OAuth refresh and encrypted token rotation | PostgreSQL Session snapshot, live re-resolution, and durable refresh tests; MCP authorization, 401/403 classification, and redirect-replay tests |
| MCP OAuth validation | `mcp_oauth_validate` | Official Go SDK call; live `initialize` and `tools/list` probes; refresh outcome classification; bounded, scrubbed HTTP diagnostics |

## Security contract

- Static bearer tokens, OAuth access/refresh tokens, and OAuth client secrets
  are write-only. Public response types cannot represent them.
- PostgreSQL stores a versioned AES-256-GCM envelope, never a plaintext secret.
  Authenticated data binds the ciphertext to its Vault ID, Credential ID, and
  complete public authentication configuration.
- The API verifies that binding before returning an active Credential. Changing
  a stored MCP URL, expiry, or refresh endpoint without resealing the payload
  makes the Credential unavailable.
- Vault and Credential archive are idempotent. The same transaction marks the
  resource archived and sets every encrypted payload column to `NULL`.
- An archived Credential releases its active per-Vault MCP URL key, allowing a
  replacement Credential for the same canonical HTTPS endpoint.
- A Vault retains at most 20 Credentials, including archived records; deleting
  one releases quota while archiving alone does not erase its audit record.
- Reads and mutations use the primary PostgreSQL connection. Credential updates
  execute beneath a row lock, so metadata and secret rotation cannot overwrite
  one another through an application-level read/modify/write race.
- Keyrings support one active encryption key and retained decrypt-only keys.
  Invalid configured keyrings fail startup; plaintext fallback does not exist.
- API and worker processes load the same operator-mounted keyring. Credentials
  are decrypted only for the matching request and are never mounted into a
  Session sandbox or added to model context.
- Authenticated redirects may remain on the exact scheme, host, and effective
  port. Cross-origin redirects are rejected before replaying an MCP body.
- OAuth token endpoints use the same public-network egress boundary and never
  replay form or Basic-auth secrets through redirects. Captured validation
  bodies are bounded and redact token-, secret-, password-, and authorization-
  shaped JSON fields plus every known credential value.

## Current boundary

Session authentication covers static bearer and OAuth credentials:

- Session create accepts ordered `vault_ids`; the first Vault with an exact
  normalized MCP URL match wins.
- Credentials are re-resolved for each MCP request, so rotation, archive, and
  deletion affect a running Session without a restart.
- An expired OAuth access token with a configured refresh grant is exchanged
  outside the PostgreSQL transaction. The encrypted access token, optional
  rotated refresh token, and new expiry are then committed beneath a row lock;
  a concurrently replaced refresh grant is never overwritten.
- An unmatched endpoint is attempted without authentication. Remote 401/403
  responses emit `mcp_authentication_failed_error` without terminating the
  Session.
- `mcp_oauth_validate` live-probes the current bearer, refreshes only after
  expiry or an explicit authentication rejection, and reports `valid`,
  `invalid`, or `unknown`. HTTP 4xx refresh rejection is invalid; rate limits,
  5xx responses, protocol failures, and network failures are unknown.
- `environment_variable` requests return `422` until a sandbox provider exposes
  a SecretEgress capability that can substitute a placeholder only at controlled
  outbound hosts and locations. Real secret values will not be placed in a
  sandbox environment or model context.

Webhook delivery is not implemented, so the upstream
`vault_credential.refresh_failed` notification remains outside this slice.

## Upstream references

- [Vaults API](https://platform.claude.com/docs/en/api/beta/vaults)
- [Credentials API (Go)](https://platform.claude.com/docs/en/api/go/beta/vaults/credentials)
