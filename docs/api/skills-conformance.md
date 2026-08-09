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

The black-box client is `github.com/anthropics/anthropic-sdk-go` `v1.62.0`.
Service tests run the HTTP lifecycle against PostgreSQL and MinIO.

## Reference evidence

| Boundary | Official Go SDK | Durable/service evidence |
| --- | --- | --- |
| Agent create/update | Custom Skill union request and resolved response | Strict tagged-union validation; omitted or `latest` Versions resolve before an Agent Version is stored; the latest active Agent configuration holds relational pins |
| Session create | Inherited and `agent_with_overrides` Skill lists | Effective references are revalidated and resolved into the immutable Session agent snapshot |
| Session persistence | Resolved Version in retrieve/list responses | PostgreSQL records relational Session-Version pins in the same transaction as the Session projection |
| Version deletion | SDK error decoding | Deletion is rejected while an active Agent or committed Session pins the Version; Agent replacement/archive or physical Session deletion releases the corresponding pins |
| Runtime discovery | N/A | `PrepareTurn` projects bounded name, description, and `SKILL.md` path metadata and adds the private `Skill` dispatcher without eagerly injecting bundle contents |
| Runtime activation | N/A | A `Skill` call reads the selected immutable pin, returns `Launching skill: <name>`, and injects the complete `SKILL.md` with its base directory into the provider conversation |
| Runtime durability | N/A | The injected block is journaled with the tool result, retained in the provider transcript, recovered after worker restart, and not duplicated by an identical later activation |
| Docker materialization | N/A | The worker verifies archive size and SHA-256, revalidates every zip entry, atomically publishes a read-only tree, repairs damaged staging, and reattaches it after worker restart |

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
  Session; the latest active Agent configuration and every resolved Agent scope
  in a committed Session retain their archives. Migration backfills concrete,
  ready references from pre-existing primary projections.
- Docker-backed cloud Sessions expose primary/self custom Skills at
  `/workspace/skills/<name>/`. External roster Agents use stable isolated
  namespaces below `/workspace/skills/.agents/`, so two Agent configurations
  may safely reuse a runtime name with different Versions. The uploaded
  top-level directory is stripped and re-rooted under the validated frontmatter
  name, so case and underscore normalization cannot make discovery disagree
  with the runtime path.
- Skill contents are reconciled lazily before the first sandbox tool call. The
  model initially receives only JSON-encoded name, description, and `SKILL.md`
  path metadata. Selecting the private `Skill` dispatcher causes the runtime,
  rather than a model-issued `read`, to inject the complete main instruction
  file. Supporting files and scripts remain available through normal tools.
- Admission rejects duplicate runtime names or more than 500 MB of aggregate
  expanded bundle content within one Agent scope. Discovery always retains
  every current-Agent Skill name and uses
  one percent of the configured context window for descriptions; once that
  budget is exhausted, later entries remain invocable as name-only records.
  Executable bits are preserved while the complete Docker bind mount remains
  read-only.
- Skills routes return `422` when S3-compatible storage is not configured or
  its startup reconciliation cannot complete.

## Current limits

- Runtime execution currently requires a cloud Session and the Docker sandbox
  provider. Local, self-hosted, and current remote-provider Sessions reject
  custom Skill execution explicitly.
- A Docker container created before Skill mounts were introduced cannot gain a
  bind mount in place. Reconciliation fails closed and requires that Session's
  sandbox to be recreated; it never reports an unavailable path as mounted.
- Request-time compaction follows the documented Claude Code budget: the most
  recent invocation of each activated Skill keeps up to 5,000 estimated tokens,
  with 25,000 estimated tokens shared across Skills. A truncated reattachment
  may be invoked again to restore the complete body. Exact differential parity
  with every hosted compaction path is not part of this matrix yet.
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
- [Claude Code Skill content lifecycle](https://code.claude.com/docs/en/slash-commands#skill-content-lifecycle)
