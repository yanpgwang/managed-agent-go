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
Session. Docker mounts it read-only beneath `/mnt/session/uploads`; an explicit
path is normalized inside that root. Deleting the source upload does not break
the copy. Detach the Session Resource to delete it.

Runtime add/delete commits desired state in PostgreSQL and is reconciled before
the next sandbox tool. A Session may hold up to 500 active resources and 500 MB
of aggregate File bytes.

## Memory Store attachments

Up to eight Stores may be attached when a Session is created. Each attachment
is `read_only` or `read_write` and is mounted beneath
`/mnt/memory/<store-slug>/`. Memory attachments cannot currently be added or
removed after Session creation.

## Availability

File and Memory mounts currently require a cloud Environment and the Docker
sandbox capability. GitHub repository resources and update-time repository
token rotation are not implemented; unsupported variants return an explicit
`422`.

See [Files](files.md), [Memory](memory.md), and the
[Session Resources conformance ledger](session-resources-conformance.md).
