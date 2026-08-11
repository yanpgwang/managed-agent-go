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

Set `MANAGED_AGENT_FILE_S3_BUCKET` and the corresponding endpoint, region, and
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
independent, downloadable Session-scoped copies.

## Lifecycle and limits

- Metadata becomes visible only after the object write completes.
- Delete hides metadata before deleting bytes; startup reconciliation finishes
  interrupted operations.
- Top-level Files are not yet accepted as message content or outcome rubrics.
- Arbitrary sandbox-output export is not implemented.
- Files storage remains single-tenant, and startup reconciliation currently
  assumes one Files-enabled API process.

See [Session Resources](session-resources.md) to mount a File in a Session and
the [Files conformance ledger](files-conformance.md) for test evidence.
