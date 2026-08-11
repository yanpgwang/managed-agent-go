---
title: Session Resources conformance
slug: /api/session-resources-conformance
---

# Session Resources conformance

This matrix records the File-backed and Memory-backed Session Resources slices
separately from the historical
[core compatibility snapshot](../compatibility/core-2026-08-03.md). The routes
use `anthropic-beta: managed-agents-2026-04-01`.

## Operation evidence

| Operation | Official Go SDK | Durable/service evidence |
| --- | --- | --- |
| `POST /v1/sessions/{session_id}/resources` | File input and File Resource response | Independent object copy, Session lock, mount collision check, and atomic metadata publication |
| `GET /v1/sessions/{session_id}/resources` | File and Memory Store union decoding, cursor page shape | PostgreSQL keyset order and cursor scope binding |
| `GET /v1/sessions/{session_id}/resources/{resource_id}` | File Resource union decoding | Active rows only; cross-Session IDs do not resolve |
| `POST /v1/sessions/{session_id}/resources/{resource_id}` | Request reaches the route | Explicit `422`: token rotation is defined only for GitHub repository resources |
| `DELETE /v1/sessions/{session_id}/resources/{resource_id}` | Deleted response decoding | Desired-state tombstone, idempotent unmount, object cleanup, and crash retry |

File and Memory Store inputs are accepted in `POST /v1/sessions`. Session
publication, initial events, File-copy visibility, and resource rows share one
PostgreSQL transaction after any object copies are prepared.

The black-box client is `github.com/anthropics/anthropic-sdk-go` `v1.62.0`.
Service tests use real PostgreSQL. Sandbox tests use a real Docker container and
verify reattachment after a provider-client restart.

## Implemented contract

- The `file` and `memory_store` variants are accepted at Session creation.
  Memory Stores cannot be added or removed after creation. GitHub repository
  resources return an explicit unsupported error.
- Every File attachment creates a new downloadable File with
  `scope.type = session`.
  Its object bytes are independent: deleting the source upload does not break
  the Session Resource. The scoped copy cannot be deleted through
  `DELETE /v1/files/{file_id}`; detach the owning Session Resource instead.
- A Session accepts at most 500 active resources and 500 MB of aggregate File
  bytes. The count, byte budget, and mount-path uniqueness check run under the
  Session database lock; create-time requests are rejected before any copy when
  their sources already exceed the byte budget.
- An omitted path becomes `/mnt/session/uploads/<source_file_id>`. A supplied
  absolute path is normalized beneath `/mnt/session/uploads`; parent traversal
  is rejected. The normalized path is limited to 1024 UTF-8 bytes and each
  component to 255 bytes so every admitted path is materializable.
- File bytes stream from object storage into a provider-owned staging file.
  Size and SHA-256 are checked before an atomic rename; a failed replacement
  leaves the prior file visible.
- Docker bind-mounts the provider directory read-only at
  `/mnt/session/uploads`. Container root cannot modify the mounted copy.
- PostgreSQL stores desired resources and deletion tombstones. Provider-owned
  integrity markers record applied mounts. Every sandbox acquisition repairs
  missing active mounts and removes deleted mounts before a built-in or MCP
  tool executes.
- The model system context lists active read-only mount paths and their
  session-scoped File IDs without injecting File contents.
- Runtime add and delete take effect before the next sandbox tool execution.
  They do not interrupt a command that was already running when the mutation
  committed.
- A path may be reused immediately after detach. Deletion tombstones reconcile
  before active mounts, and provider markers include the resource identity so
  a late retry cannot remove or resurrect a newer attachment at the same path.

## Availability and current limits

- The API and worker must share the same PostgreSQL, object-store, sandbox
  provider, and task-queue configuration.
- File Resources are admitted only when Files storage is configured and
  `MANAGED_AGENT_SANDBOX=docker`. The local-process provider cannot safely
  expose an isolated absolute path, and the current remote adapters do not
  advertise an equivalent read-only mount primitive; those deployments return
  `422` for create-time and runtime admission. When Files storage remains
  configured, list, get, and delete stay available so resources created before
  a provider configuration change can still be inspected and detached.
- Docker Session Resources currently require the worker to run where its
  Docker daemon can bind the provider staging directory. The directory can be
  placed with `MANAGED_AGENT_SANDBOX_RESOURCE_DIR`; all workers that can attach
  a Session must use the same dedicated, host-visible location and Docker
  daemon/context. Allow up to 500 MB of staging capacity per concurrently live
  resource-bearing sandbox. The bundled Compose
  stack keeps the safer local-process development default and therefore does
  not enable Session Resources through its running API.
- Provider startup audits staging generations older than 24 hours. A generation
  is removed only after a complete Docker inventory proves that no managed
  container mounts it; any inventory error makes the audit a no-op.
- File delivery to self-hosted Environments is not implemented.
- Resource deletion hides the API row immediately. A tombstone remains until a
  worker has removed any applied mount; deleting the Session also removes it.
  A new active resource may reuse the path while that cleanup is pending.
- A Docker container created before File Resource mounts were introduced keeps
  ordinary tool execution. Adding a File Resource to that legacy container
  terminates the Session with a public error. Detaching remains available and
  is a no-op at the provider, but the terminated Session must be recreated. New
  containers always include the mount.
- Session Workflow runs that predate this capability retain the legacy
  five-minute tool Activity timeout until Continue-As-New. Recreate such a
  Session before attaching a large resource when materialization may exceed
  five minutes. New runs use a 30-minute budget.
- Resource copies are synchronous: the API downloads, spools, and uploads each
  source before publishing the Session. The 500 MB aggregate budget bounds one
  request, but operators must size API temporary storage and upstream request
  timeouts accordingly.
- File-sourced messages, File outcome rubrics, arbitrary sandbox-output export,
  and GitHub repositories remain outside this slice. Memory Store behavior is
  detailed in the [Memory conformance matrix](memory-conformance.md).
- Files metadata and bytes remain single-tenant, and Files startup
  reconciliation still assumes one Files-enabled API process.

## Normative references

- [Session Resources API](https://platform.claude.com/docs/en/api/beta/sessions/resources)
- [Managed Agents Files](https://platform.claude.com/docs/en/managed-agents/files)
- [Files API](https://platform.claude.com/docs/en/api/beta/files)
- [Managed Agents Memory](https://platform.claude.com/docs/en/managed-agents/memory)
