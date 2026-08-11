---
title: Memory conformance
slug: /api/memory-conformance
---

# Memory conformance

This matrix records the Memory API and Memory-backed Session Resource slice
separately from the historical
[M1 core compatibility snapshot](../compatibility/core-v1.md).
Memory resource routes use `anthropic-beta: agent-memory-2026-07-22`; Session
creation continues to use `managed-agents-2026-04-01`.

## Operation evidence

| Resource | Operations | Evidence |
| --- | --- | --- |
| Memory Stores | create, get, update, list, archive, delete | Official Go SDK request/response decoding; PostgreSQL lifecycle, filter, cursor, archive, and attachment-FK tests |
| Memories | create, get, update, list, delete | Official Go SDK full lifecycle; UTF-8/path/size bounds, depth-one prefix projection, SHA-256 preconditions, idempotent desired-state update, and conflict tests |
| Memory Versions | get, list, redact | Official Go SDK full/basic views; immutable actor-attributed history, filter, redaction, and current-head protection tests |
| Session attachment | create-time `memory_store` resource | Official Go SDK union decoding; real PostgreSQL snapshot and deletion lifecycle; real Docker read/write and read-only mount tests |
| Runtime persistence | ordinary file tools | Real Docker plus PostgreSQL round trip; atomic multi-file update/create/delete; concurrent stale-write rollback; final deletion-time writeback |

The black-box client is `github.com/anthropics/anthropic-sdk-go` `v1.62.0`.
Opt-in integration tests use real PostgreSQL and Docker.

## Implemented contract

- PostgreSQL is the canonical Store. No object store is required for Memory
  contents.
- A Store contains at most 2,000 current Memories. Each Memory is one valid
  UTF-8 document of at most 102,400 bytes at a canonical NFC-normalized
  absolute path of at most 1,024 bytes.
- Every create, content/path update, and delete appends an immutable Version.
  Versions record `api_actor`, `session_actor`, or `user_actor` attribution.
  Redaction removes the historical path and content while retaining lineage,
  operation, timestamp, and redaction actor. A current head cannot be redacted.
- Content mutations accept SHA-256 optimistic preconditions. A stale request
  that already describes the stored desired state is an idempotent success;
  otherwise it returns `409 memory_precondition_failed_error`.
- Store archive is one-way and makes contents read-only. Deleting a Store
  removes its Memories and Versions, but is rejected while any Session remains
  attached.
- Memory lists support `basic` and `full` views, path prefixes, depth zero or
  one, and opaque forward cursors. Full pages are limited to 20; basic pages to
  100. Store and Version lists use stable PostgreSQL keyset pagination.
- A cloud Session may attach up to eight active, non-archived Stores at
  creation. Runtime add/remove is rejected. The resource snapshots name,
  description, instructions, access, and mount path so later Store renames do
  not move a live Session.
- Mount paths are derived from the lowercased Store name with non-alphanumeric
  runs collapsed to hyphens: `/mnt/memory/<store-slug>`.
- The system context exposes Store metadata and instructions, not contents.
  Contents stay on disk and are accessed through the standard `read`, `write`,
  `edit`, `glob`, `grep`, and `bash` tool surface. There is no Memory-specific
  recall tool.

## Runtime consistency

Docker bind-mounts every Store separately. `read_only` is enforced by Docker's
mount flag, including against container root. `read_write` changes are scanned
after each sandbox tool and committed in one PostgreSQL transaction. The
provider stores the last published Memory IDs and SHA-256 values outside the
agent-visible mount.

Before the next tool, the worker first retries any local changes left by an
interrupted Activity, then refreshes remote heads. A concurrent mutation to a
locally changed baseline returns an explicit precondition error; unchanged
paths accept newer remote heads. Before Session deletion destroys the sandbox,
the cleanup Activity performs one final writeback, closing the crash window
between tool execution and the ordinary post-tool hook.

## Availability and limits

- Memory API operations are available anywhere PostgreSQL is configured.
- Memory-backed Session Resources currently require a cloud Environment and
  `MANAGED_AGENT_SANDBOX=docker`. Local, self-hosted, E2B, CubeSandbox,
  OpenSandbox, and Daytona adapters do not yet advertise the mount capability.
- API-key actor IDs are stable credential hashes in the current single-tenant
  deployment. Production workspace identity, authorization, and tenant
  isolation remain platform hardening work.
- Version rows are immutable and expose the documented timestamps, but an
  automatic 30-day retention reaper is not yet implemented.
- The implementation follows documented behavior and official SDK types. It
  does not claim byte-for-byte parity with Anthropic's private storage or
  conflict-resolution internals.

## Normative references

- [Managed Agents Memory](https://platform.claude.com/docs/en/managed-agents/memory)
- [Memory Stores API](https://platform.claude.com/docs/en/api/beta/memory-stores)
- [Session Resources API](https://platform.claude.com/docs/en/api/beta/sessions/resources)
