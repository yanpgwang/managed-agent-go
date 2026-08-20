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

## Outcome rubrics

A ready top-level client upload can be reused as an outcome rubric by sending
`{"type":"file","file_id":"file_..."}` in `user.define_outcome`. This is an
internal admission read and does not make the File publicly downloadable.
Mango reads at most the largest valid UTF-8 encoding of 262,144 characters,
checks the stored byte count and SHA-256, rejects empty, invalid UTF-8,
over-limit, deleting, missing, cross-Workspace, and Session-scoped Files, and
durably snapshots the resulting text with the admitted event.

The event returned to clients retains only the File reference. Deleting the
source after admission does not change the working-agent or grader input.

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
already-visible File without another object upload; rewriting the same relative
output path with new bytes atomically replaces its visible File metadata and
object. Removing a path from the output tree hides and cleans up its prior File
at the next idle snapshot, so the visible set matches the current tree and the
500-file limit applies across turns.

An invalid output entry emits a recoverable `session.error` immediately before
the idle event. The agent's answer remains visible and the Session remains
usable, allowing a later turn to remove or replace the invalid entry. An
explicit interrupt skips output publication so cancellation is not delayed by
a large snapshot.

Publishing requires both configured Files storage and a Docker sandbox. It is
not enabled for the CMA `self_hosted` Environment mode, where the client owns
tool execution, nor for the local-process sandbox or current remote adapters.
A text-only Session that never provisioned a sandbox does not create one merely
to check for outputs. A durable Docker sandbox created before the output mount
was introduced fails closed and must be recreated; it is never treated as an
empty output tree.

## Lifecycle and limits

- Metadata becomes visible only after the object write completes.
- Delete hides metadata before deleting bytes; startup reconciliation finishes
  interrupted operations.
- Top-level Files are accepted as UTF-8 outcome rubrics, but not as message
  content.
- Only `/mnt/session/outputs` is exported; arbitrary workspace files remain
  private to the sandbox.
- File metadata and object keys are Workspace-scoped. Startup reconciliation
  currently assumes one Files-enabled API process.

See [Session Resources](session-resources.md) to mount a File in a Session.
