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
| Session and MCP runtime | ordered `vault_ids`; canonical URL matching; static bearer and current OAuth access-token injection | PostgreSQL Session snapshot and live re-resolution tests; MCP authorization, 401/403 classification, and redirect-replay tests |
| MCP OAuth validation | not implemented | Deferred with live MCP probing and refresh recovery |

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

## Current boundary

Session authentication currently covers static bearer credentials and
non-expired OAuth access tokens:

- Session create accepts ordered `vault_ids`; the first Vault with an exact
  normalized MCP URL match wins.
- Credentials are re-resolved for each MCP request, so rotation, archive, and
  deletion affect a running Session without a restart.
- An unmatched endpoint is attempted without authentication. Remote 401/403
  responses emit `mcp_authentication_failed_error` without terminating the
  Session.
- OAuth validation and automatic refresh are not implemented.
- `environment_variable` requests return `422` until a sandbox provider exposes
  a SecretEgress capability that can substitute a placeholder only at controlled
  outbound hosts and locations. Real secret values will not be placed in a
  sandbox environment or model context.

OAuth refresh follows as a separate stateful workflow because a remote token
exchange cannot be made exactly-once with a local database transaction.

## Upstream references

- [Vaults API](https://platform.claude.com/docs/en/api/beta/vaults)
- [Credentials API (Go)](https://platform.claude.com/docs/en/api/go/beta/vaults/credentials)
