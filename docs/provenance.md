---
title: Design provenance
slug: /provenance
---

# Design provenance

Mango records material external influences so its independent design remains
auditable. Public specifications and SDKs may be deliberately reused or adapted
for terminology, routes, resource shapes, JSON fields, event types, public
client types, workflows, and edge cases. Mango does not rename a sound concept
merely to appear different. Once adopted, the resulting surface is Mango-owned;
the source does not define its target contract or create compatibility,
synchronization, or release-timing obligations. Mango's documentation, OpenAPI
definition, implementation, and tests are authoritative for current behavior.

Mango's implementation, storage, scheduling, and runtime design are independent
and self-hosted. Public surface definitions may be design inputs, but external
implementation code and non-public types must not be copied, and an external
release is never an automatic roadmap.

## HTTP transport

- Claude Managed Agents documentation and public SDK behavior informed early
  use of `x-api-key`, provider version and beta headers, and a provider-named
  worker correlation header.
- Mango uses standard `Authorization: Bearer` authentication and media types,
  retains the generic `request-id` response header, and exposes optional worker
  correlation as the `worker_id` query parameter. It does not expose provider
  rollout headers on its inbound API.
- The Anthropic Messages adapter continues to send the provider headers its
  outbound endpoint requires. Tests that exercise Mango through an Anthropic
  SDK are optional research evidence; raw HTTP and OpenAPI tests define Mango's
  transport contract.

## File-backed Session messages

- The [Managed Agents event API](https://platform.claude.com/docs/en/api/beta/sessions/events)
  defines `user.message` document sources that reference a previously uploaded
  File by `file_id`.
- The [Files API guide](https://platform.claude.com/docs/en/build-with-claude/files)
  defines upload-once File resources, non-downloadable client uploads, and
  File references in message requests.
- `github.com/anthropics/anthropic-sdk-go` at the version pinned in `go.mod`
  supplied request and response examples during early development. It is not a
  runtime dependency, compatibility baseline, or authority over Mango's API.

Mango's bounded UTF-8 projection, private admission snapshot, S3-compatible
storage, and explicit rejection of multimodal File sources are local design
choices documented in [Files](api/files.md) and
[capabilities and limits](capabilities.md).

## File-backed Session Resources

- The [Managed Agents Files guide](https://platform.claude.com/docs/en/managed-agents/files)
  defines independently copied File resources, their read-only presentation
  beneath `/mnt/session/uploads`, optional mount paths, and runtime add/delete.
- `github.com/anthropics/anthropic-sdk-go` at the version pinned in `go.mod`
  supplied Session Resource request and response examples during early
  development. Existing tests using those types may change or be removed with
  Mango's `/v1` design.
- Remote File Resource behavior is implemented against pinned provider Go
  clients. The [OpenSandbox Go SDK](https://github.com/alibaba/OpenSandbox/blob/main/sdks/sandbox/go/README.md)
  and [Daytona filesystem guide](https://www.daytona.io/docs/file-system-operations/)
  define streaming upload/download, metadata and permission operations,
  directory management, and move/delete. The
  [CubeSandbox Go SDK](https://github.com/tencentcloud/CubeSandbox/tree/master/sdk/go)
  supplies the E2B/Cube-compatible whole-value file operations. These provider
  APIs are implementation dependencies rather than definitions of Mango's
  target contract.

Mango's provider-owned marker format and retry algorithm are independent local
design choices documented in [Sandbox backends](sandboxes.md). Remote adapters
intentionally stop at writable sandbox-local copies in the current
implementation. E2B and Cube additionally accept whole-file worker buffering
until their pinned Go data plane exposes streaming operations. These limitations
are documented as Mango behavior rather than inferred from provider APIs.

## Remote Session output export

- The pinned remote Go clients provide the filesystem directory, metadata,
  download, and delete operations used by their adapters. OpenSandbox and
  Daytona expose streaming readers; the E2B/Cube-compatible client currently
  returns whole values.
- Mango reuses those provider operations only as an implementation data plane;
  it does not expose provider file types or routes in the Mango API.

Mango's `/mnt/session/outputs` boundary, unique adapter-owned tar snapshot,
two-pass validation, close-time cleanup, S3 publication, and idle-event ordering
remain Mango-owned behavior. E2B and Cube adopt the same repeatability and
cleanup contract but buffer each archive in worker memory as an explicit
Preview limitation; their SDK similarity alone is not treated as evidence of
support, so they run the same offline and opt-in live conformance suites.

## Custom Skills

- The public [Claude Managed Agents Skills guide](https://platform.claude.com/docs/en/managed-agents/skills)
  describes version-pinned custom Skill directories, a required `SKILL.md`,
  supporting scripts and resources, filesystem paths announced to the Agent,
  and instruction loading when relevant.
- The public [Agent Skills overview](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview)
  describes progressive disclosure and treats executable Skill bundles as part
  of the Agent's trust boundary.
- Mango adopted those useful user-problem and lifecycle concepts: canonical zip
  validation, immutable Version pins, name/description discovery, on-demand
  `SKILL.md` injection, and supporting files in the sandbox. Mango owns its
  `/v1` resource contract, S3 archive lifecycle, Agent-scoped runtime paths,
  private dispatcher, recovery behavior, and provider capability admission.
- Materialization for E2B, CubeSandbox, OpenSandbox, and Daytona reuses the
  same Mango contract through their pinned official filesystem clients. Mango's
  worker-side validation, sibling staging publication, provider-owned marker,
  instruction checksum, write-tool denial, and shared conformance suite are
  local design choices.
- Mango did not adopt Anthropic beta headers, hosted authentication, the
  `anthropic` managed catalog, cloud-only repository scanning, rollout timing,
  or a requirement to mirror hosted/self-hosted feature differences. Repository
  Skills and Environment Worker activation remain separate product decisions.
