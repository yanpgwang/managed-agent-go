---
title: Compatibility provenance
slug: /provenance
---

# Compatibility provenance

Mango uses public Claude Platform documentation and the repository's pinned
official SDK only to define observable wire contracts. Mango's implementation,
storage, scheduling, and runtime design are independent and self-hosted.

## File-backed Session messages

- The [Managed Agents event API](https://platform.claude.com/docs/en/api/beta/sessions/events)
  defines `user.message` document sources that reference a previously uploaded
  File by `file_id`.
- The [Files API guide](https://platform.claude.com/docs/en/build-with-claude/files)
  defines upload-once File resources, non-downloadable client uploads, and
  File references in message requests.
- `github.com/anthropics/anthropic-sdk-go` at the version pinned in `go.mod`
  supplies the black-box request and response types used by compatibility
  tests. It is not a runtime dependency on hosted Managed Agents behavior.

Mango's bounded UTF-8 projection, private admission snapshot, S3-compatible
storage, and explicit rejection of multimodal File sources are local design
choices documented in [Files](api/files.md) and the
[compatibility ledger](compatibility.md).
