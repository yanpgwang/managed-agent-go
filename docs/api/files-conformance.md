---
title: Files API conformance
slug: /api/files-conformance
---

# Files API conformance

This matrix records the implemented Files slice separately from the frozen
[core compatibility statement](../compatibility/core-v1.md). Files requests
use `anthropic-beta: files-api-2025-04-14`; they do not expand the core
`managed-agents-2026-04-01` claim.

## Operation evidence

| Operation | Raw HTTP | Official Go SDK | Durable/service evidence |
| --- | --- | --- | --- |
| `POST /v1/files` | Multipart shape, header, filename, and size validation | Upload and response decoding | PostgreSQL intent, bounded stream-to-disk, S3-compatible write, and crash cleanup |
| `GET /v1/files` | Filters and page envelope | Forward and backward auto-paging | PostgreSQL ordering, cursor boundaries, and scope filtering |
| `GET /v1/files/{file_id}` | Metadata and not-found shape | Metadata retrieval | Only committed `ready` rows are visible |
| `GET /v1/files/{file_id}/content` | Binary content and non-downloadable rejection | Byte-exact output-file download | S3-compatible read for an internally seeded downloadable output |
| `DELETE /v1/files/{file_id}` | Deleted response and not-found shape | Delete lifecycle | Metadata is hidden before object deletion; incomplete deletion is reconciled on restart |

The black-box client is `github.com/anthropics/anthropic-sdk-go` `v1.61.0`.
Service tests run the same HTTP lifecycle against real PostgreSQL and MinIO.

## Implemented contract

- Upload accepts exactly one multipart part named `file`.
- File size is limited to 500 MB. Uploads are streamed through a bounded local
  spool file before the object-store write; file bytes are not buffered in Go
  memory.
- Filenames contain 1–255 characters and reject control characters, path
  separators, and the documented unsafe filename characters.
- List defaults to 20 items, accepts up to 1,000, supports `after_id`,
  `before_id`, and `scope_id`, and returns `data`, `has_more`, `first_id`, and
  `last_id`.
- Client-uploaded files have `scope: null` and `downloadable: false`, matching
  the upstream Files contract. The content endpoint rejects them.
- The metadata row is committed as an intent before blob I/O. A file becomes
  visible only after its object-store write completes. Delete hides metadata
  before deleting bytes. Startup reconciliation removes interrupted upload and
  delete intents.
- Files routes return `422` when `MANAGED_AGENT_FILE_S3_BUCKET` is not set or
  the configured object store cannot initialize. An object-store failure does
  not prevent the Managed Agents core routes from starting.

## Current limits

- The public runtime does not yet produce downloadable Agent output files.
  File-backed Session Resources do create downloadable session-scoped copies,
  but arbitrary sandbox output export remains open. A scoped copy is deleted by
  detaching its Session Resource, not through the top-level Files delete route.
- Files can be mounted through the conditional Docker-backed Session Resources
  slice. They cannot yet be referenced by message content or used as outcome
  rubrics. See the [Session Resources conformance matrix](session-resources-conformance.md).
- Files metadata and bytes are not tenant-isolated. Strict mode checks header
  presence but is not production authentication.
- Startup reconciliation currently assumes one Files-enabled API process.
  Multi-replica operation needs a distributed lease or stale-intent protocol.
- Operators must provide private S3-compatible storage and enough temporary
  disk for each concurrent upload. The bundled MinIO service is development
  and CI infrastructure, not a production recommendation.

## Normative references

- [Files API reference](https://platform.claude.com/docs/en/api/beta/files)
- [Files guide](https://platform.claude.com/docs/en/build-with-claude/files)
- [Claude API errors](https://platform.claude.com/docs/en/api/errors)
