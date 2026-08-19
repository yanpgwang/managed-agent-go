---
title: Files
slug: /api/files
---

# Files

The Files API stores immutable client uploads in configured S3-compatible
storage. PostgreSQL owns metadata and crash-recoverable upload/delete intents;
the object store owns bytes.

```text
POST   /v1/files
GET    /v1/files
GET    /v1/files/{file_id}
GET    /v1/files/{file_id}/content
DELETE /v1/files/{file_id}
```

Set `MANGO_FILE_S3_BUCKET` and the corresponding endpoint, region, and
credential variables before using these routes. In strict mode they require
`anthropic-beta: files-api-2025-04-14`.

## Upload and list

Upload one multipart part named `file`. The maximum file size is 500 MB.
Uploads stream through bounded temporary storage rather than being buffered in
Go memory.

Lists use the upstream `after_id` or `before_id` direction, an optional
`scope_id`, and a `data`/`has_more` envelope. The two direction parameters
cannot be combined.

Client uploads have `scope: null` and `downloadable: false`; their content
endpoint is intentionally unavailable. File-backed Session Resources create
independent, downloadable Session-scoped copies. Mango-managed Docker Sessions
also publish agent deliverables written beneath `/mnt/session/outputs` as
downloadable Files with `scope.id` equal to the Session ID.

## Session outputs

The output directory is writable inside a Docker sandbox. At every primary
Session idle boundary, the worker recursively streams its regular files into
the configured object store before committing `session.status_idle`. A client
that observes the idle event can therefore immediately list and download the
deliverables with `GET /v1/files?scope_id={session_id}`.

Each output is subject to the 500 MB per-file limit. One Session may publish at
most 500 files from the output tree. Directories are traversed but are not
Files; symbolic links, hard links, devices, path traversal, and other
non-regular archive entries are rejected. An unchanged retry preserves the
already-visible File; rewriting the same relative output path with new bytes
atomically replaces its visible File metadata and object.

Publishing requires both configured Files storage and a Docker sandbox. It is
not enabled for the CMA `self_hosted` Environment mode, where the client owns
tool execution, nor for the local-process sandbox or current remote adapters.
A text-only Session that never provisioned a sandbox does not create one merely
to check for outputs.

## Lifecycle and limits

- Metadata becomes visible only after the object write completes.
- Delete hides metadata before deleting bytes; startup reconciliation finishes
  interrupted operations.
- Top-level Files are not yet accepted as message content or outcome rubrics.
- Only `/mnt/session/outputs` is exported; arbitrary workspace files remain
  private to the sandbox.
- File metadata and object keys are Workspace-scoped. Startup reconciliation
  currently assumes one Files-enabled API process.

See [Session Resources](session-resources.md) to mount a File in a Session.
