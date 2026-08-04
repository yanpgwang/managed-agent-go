---
title: Skills API conformance
slug: /api/skills-conformance
---

# Skills API conformance

This matrix records the custom Skills resource slice separately from the
frozen [core compatibility statement](../compatibility/core-v1.md). Skills
requests use `anthropic-beta: skills-2025-10-02`; they do not expand the core
`managed-agents-2026-04-01` claim.

## Operation evidence

| Operation | Official Go SDK | Durable/service evidence |
| --- | --- | --- |
| `POST /v1/skills` | Multipart upload and resource decoding | Bundle validation, PostgreSQL creation intent, S3-compatible archive write, and crash cleanup |
| `GET /v1/skills` | Source filter and cursor-page decoding | Ready-only PostgreSQL ordering and filter-bound cursors |
| `GET /v1/skills/{skill_id}` | Resource decoding and not-found handling | Creating rows remain private; empty ready Skills remain addressable |
| `DELETE /v1/skills/{skill_id}` | Deleted response decoding | Deletion is rejected until every Version is removed |
| `POST /v1/skills/{skill_id}/versions` | Multipart upload and Version decoding | Immutable numeric Version allocation and two-phase archive visibility |
| `GET /v1/skills/{skill_id}/versions` | Cursor-page decoding | Parent-bound pagination over ready Versions |
| `GET /v1/skills/{skill_id}/versions/{version}` | Version decoding | Ready-only metadata retrieval |
| `DELETE /v1/skills/{skill_id}/versions/{version}` | Deleted response decoding | Metadata is hidden before archive deletion; restart reconciliation completes interrupted work |
| `GET /v1/skills/{skill_id}/versions/{version}/content` | Binary response download | Byte-complete canonical zip read from S3-compatible storage |

The black-box client is `github.com/anthropics/anthropic-sdk-go` `v1.61.0`.
Service tests run the HTTP lifecycle against PostgreSQL and MinIO.

## Reference evidence

| Boundary | Official Go SDK | Durable/service evidence |
| --- | --- | --- |
| Agent create/update | Custom Skill union request and resolved response | Strict tagged-union validation; omitted or `latest` Versions resolve before an Agent Version is stored; the latest active Agent configuration holds relational pins |
| Session create | Inherited and `agent_with_overrides` Skill lists | Effective references are revalidated and resolved into the immutable Session agent snapshot |
| Session persistence | Resolved Version in retrieve/list responses | PostgreSQL records relational Session-Version pins in the same transaction as the Session projection |
| Version deletion | SDK error decoding | Deletion is rejected while an active Agent or committed Session pins the Version; Agent replacement/archive or physical Session deletion releases the corresponding pins |

## Implemented contract

- Create accepts a zip archive or path-qualified multipart files. Every bundle
  has one top-level directory and a root `SKILL.md`.
- The directory matches the frontmatter `name` after case and underscore
  normalization. `name` and `description` enforce the documented character,
  length, reserved-word, and XML-tag restrictions.
- Explicit `display_title` values are unique among custom Skills. When omitted,
  the title derives from the frontmatter name.
- Uploads remain below 30 MB and at most 1,000 files. Archive validation rejects
  absolute paths, traversal, duplicate paths, links, and non-regular entries.
- PostgreSQL commits an upload or deletion intent before object-store I/O. Only
  completed Versions are public. Startup reconciliation removes interrupted
  uploads and finishes interrupted deletions.
- Skills and Version lists return `data`, `has_more`, and nullable `next_page`.
  Cursors are bound to source filters or the parent Skill ID.
- Agent and Session references accept the documented `custom` union. Omitted
  Versions and the `latest` alias resolve to a concrete ready Version before the
  Agent Version or Session snapshot is persisted. A Session supports at most
  500 effective Skill references.
- Agent/Session pins and Skill Version deletion linearize on the PostgreSQL
  Version row. A deleting Version cannot enter a new Agent configuration or
  Session; the latest active Agent configuration and every committed Session
  retain their archives. Migration backfills concrete, ready references from
  pre-existing projections.
- Skills routes return `422` when S3-compatible storage is not configured or
  its startup reconciliation cannot complete.

## Current limits

- Custom references are validated and pinned, but archives are not yet mounted
  into a sandbox and Skill metadata is not added to model context. This slice
  therefore does not claim Skill execution yet.
- Opaque Skill values stored before tagged references were enforced remain
  readable and round-trip without field loss. They must be replaced on the
  Agent before that configuration can start a new Session.
- Anthropic-managed Skills are not bundled or mirrored. Listing with
  `source=anthropic` returns an empty page, and attaching one returns an
  explicit `422` unsupported error.
- Metadata and archives are not tenant-isolated. Strict mode checks header
  presence but is not production authentication.
- Startup reconciliation currently assumes one Skills-enabled API process.
  Multi-replica operation needs a distributed lease or stale-intent protocol.
- Canonicalization buffers the validated sub-30 MB bundle in API-process memory
  before writing the archive to object storage.

## Normative references

- [Managed Agents Skills](https://platform.claude.com/docs/en/managed-agents/skills)
- [Using Agent Skills with the API](https://platform.claude.com/docs/en/build-with-claude/skills-guide)
- [Agent Skills overview](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview)
- [Skills API reference](https://platform.claude.com/docs/en/api/beta/skills)
