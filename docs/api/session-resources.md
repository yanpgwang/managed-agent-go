---
title: Session Resources
slug: /api/session-resources
---

# Session Resources

Session Resources attach File copies or Memory Stores to a Session without
injecting their contents into model context.

```text
POST   /v1/sessions/{session_id}/resources
GET    /v1/sessions/{session_id}/resources
GET    /v1/sessions/{session_id}/resources/{resource_id}
POST   /v1/sessions/{session_id}/resources/{resource_id}
DELETE /v1/sessions/{session_id}/resources/{resource_id}
```

## File attachments

A File attachment creates an independent downloadable copy scoped to the
Session. An explicit path is normalized beneath `/mnt/session/uploads`.
Docker exposes that path read-only. OpenSandbox and Daytona materialize a
writable sandbox-local copy as a current backend limitation; changing it does
not change the S3-backed source or downloadable Session File. Write a modified
deliverable beneath `/mnt/session/outputs` when output publication is available.
Deleting the source upload does not break the copy. Detach the Session Resource
to delete it from the sandbox.

Runtime add/delete commits desired state in PostgreSQL and is reconciled before
the next sandbox tool. A Session may hold up to 500 active resources and 500 MB
of aggregate File bytes.

## Memory Store attachments

Up to eight Stores may be attached when a Session is created. Each attachment
is `read_only` or `read_write` and is mounted beneath
`/mnt/memory/<store-slug>/`. Memory attachments cannot currently be added or
removed after Session creation.

## Availability

File mounts currently require a cloud Environment backed by Docker,
OpenSandbox, or Daytona. E2B and Cube remain fail-closed because their pinned
Go data-plane client buffers uploads and does not satisfy Mango's 500 MB
streaming path. Memory mounts require Docker. GitHub repository resources and
update-time repository token rotation are not implemented; unsupported
variants return an explicit `422`.

See [Files](files.md) and [Memory](memory.md).
