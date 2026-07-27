---
title: Compatibility ledger
slug: /compatibility
---

# Compatibility ledger

How this implementation compares to the Claude Managed Agents contract. One row
per capability or endpoint, grounded only in this repository's behavior.

- **Status** is `exact` only when tests assert the complete relevant behavior.
  A passing implementation test or successful SDK decode alone is not enough.
  Use `partial` when a flow works but fields, edge cases, or evidence remain
  incomplete, and `unsupported` when it is not implemented.
- **Test** names the proving test; `—` means no test yet.

Go SDK black-box tests live in `internal/httpapi/sdk_test.go`; raw-HTTP golden
tests in `internal/httpapi/sdk_golden_test.go`.

## Agents

| Capability | Status | Test | Notes |
|---|---|---|---|
| Create agent (`POST /v1/agents`), `version` starts at 1 | partial | `TestSDK_AgentLifecycle` | SDK request/decoding, initial version, and an opaque `multiagent` object are asserted; resolved-roster semantics and a complete response key set are not. |
| Get agent (`GET /v1/agents/{id}`) | partial | `TestSDK_AgentLifecycle` | Latest version is returned; response-field completeness is not proven. |
| Update agent (`POST /v1/agents/{id}`), `version` optimistic concurrency | partial | `TestSDK_AgentLifecycle` | Matching/stale-version paths are exercised; required-field, missing-version, and full update semantics remain incomplete. |
| Update field semantics (patch vs full-replace, clear-with-null) | partial | `TestAgents_ClearSystemWithNull`, `TestAgents_UpdateModelNullRejected`, `TestAgents_MultiagentNullAndInvalidShapes` | `system`/`description` and opaque `multiagent` clear-with-null plus metadata patch are proven; per-field full-replacement of `tools`/`mcp_servers`/`skills` is not yet asserted. |
| List agents (`GET /v1/agents`) | partial | `TestSDK_AgentLifecycle` | Latest-per-id returned with a `next_page` field; `limit`/`page`/`order`/`created_at`/`include_archived` filters not implemented. |
| List agent versions (`GET /v1/agents/{id}/versions`) | partial | `TestAgents_CreateGetVersionArchive` | Convenience route; exact wire not confirmed. |
| Archive agent (`POST /v1/agents/{id}/archive`) | partial | `TestSDK_AgentLifecycle`, `TestAgentService_ArchiveIdempotent` | Archive is idempotent, creates no new version/history entry, and archived agents reject updates; exact error wire and all list/read interactions remain incomplete. |
| Effort/speed model config, tools/mcp/skills/multiagent bodies | partial | `TestSDK_AgentLifecycle`, `TestAgents_MultiagentObjectPersistsAndReplaces`, `TestAgents_MultiagentNullAndInvalidShapes` | Opaque `multiagent` objects persist, replace atomically, and clear with explicit null. Basic tools/MCP/skills storage exists, but resolved rosters, reference validation, orchestration, and complete per-field validation are absent. |

## Sessions

| Capability | Status | Test | Notes |
|---|---|---|---|
| Create session, `agent` string (latest) | partial | `TestSDK_SessionLifecycleAndSnapshot` | SDK decoding, selected snapshot values, and presence of required top-level collection/stats/usage/deployment keys plus nested `multiagent` are asserted. Their full semantics are not implemented. |
| Create session, pinned `{type:"agent",id,version}` | partial | `TestSDK_SessionPinnedVersion` | Version/system pinning is exercised; the full response and invalid-reference cases are incomplete. |
| Create session, `agent_with_overrides` | partial | `TestCreateSession_RejectsInvalidAgentReferences` | Model/system/tools/MCP/skills replacement and invalid/null forms are validated with raw HTTP; successful override behavior is not yet SDK-proven. |
| Resolved agent snapshot is immutable | partial | `TestSDK_SessionLifecycleAndSnapshot` | Stability of `system` and `version` after one agent update is tested; other fields and archival are not. |
| Get session (`GET /v1/sessions/{id}`) | partial | `TestSDK_SessionLifecycleAndSnapshot` | Selected fields decode; the full required response shape is incomplete. |
| List sessions (`GET /v1/sessions`) | partial | `TestSDK_SessionListBidirectionalPaginationAndStatusesFilter`, `TestListSessions_BidirectionalStablePagination`, `TestListSessions_FiltersAndArchivedDefault` | Bidirectional keyset cursors are bound to order and normalized filters; agent/version/status/time/archive filters are exercised. Deployment/memory matching, mutation-concurrency guarantees, and complete semantics remain incomplete. |
| Session statuses (`idle`/`running`/`rescheduling`/`terminated`) | partial | `TestSessionService_InitialEventsRunToIdle`, `TestSessionService_SendEventDrivesRun`, `TestSessionService_RuntimeFailureClosesDurableRun`, `TestSessionService_RuntimeReceivesImmutableAgentSnapshot`, `TestRunStore_ClaimAndCompleteClosesRunAtomically`, `TestWorkshop_*` | Admission and completion commit the mutable projection with their status events and durable run state. An unrecoverable runtime error terminates the session (`session.error` + `session.status_terminated`); with no retry machinery we do not project `rescheduling`. The fake proves the full drive-to-idle path; the real agent core drives multi-turn model/tool runs, while durable incremental output, interrupt cancellation, and multi-node behavior are incomplete. |
| Update session (title) | partial | `TestSDK_SessionTitleUpdateEmitsChangedFieldsEvent`, `TestSessionService_UpdateTitleEmitsOnlyOnChange`, `TestUpdateSession_OmittedTitlePreservesValue` | Title and `session.updated` commit together; omitted/same title is a no-op. Metadata and `agent.tools`/`mcp_servers` session-local updates are absent. |
| Archive session | partial | — | Handler and running-session guard exist, but the archive endpoint is not SDK-proven. |
| Delete session | partial | `TestStreamEvents_DeleteTerminalAndEOF`, `TestSessionService_DeletePublishesTerminalEventAndClosesStream` | Removes the session/events, publishes a terminal `session.deleted` event to current subscribers, then closes the stream. Slow-subscriber, concurrent-delete, and multi-node behavior remain incomplete. |
| `metadata`, `vault_ids`, `resources`, `initial_events` inputs | partial | — | `metadata`/`title`/`initial_events` are accepted. Non-empty `vault_ids`/`resources` are explicitly unsupported; corresponding empty/default response fields are emitted. |

## Session events

| Capability | Status | Test | Notes |
|---|---|---|---|
| Event is a flat top-level tagged union (`{id,type,...,processed_at}`) | partial | `TestGolden_EventIsFlatTaggedUnion` | The raw golden covers one `user.message`; the full event union and exact per-variant keys are not covered. |
| `user.message` with `content[]` blocks | partial | `TestSDK_EventSendAndList`, `TestGolden_EventIsFlatTaggedUnion` | One text block round-trips; other block variants and validation are incomplete. |
| Send events (`POST .../events`) echoes submitted events | partial | `TestSDK_EventSendAndList`, `TestSessionService_InitialEventBatchProcessesEveryEventInOrder`, `TestSessionService_SendEventBatchProcessesEveryEventInOrder`, `TestRunStore_QueuesRunsPerSessionInAdmissionOrder`, `TestRunStore_AdmitBatchCreatesOneRunPerTrigger`, `TestRunStore_CompletionBeforeNextClaimObservesOutput`, `TestSessionService_SecondUserEventObservesFirstAgentOutput`, `TestSessionService_BatchedTriplePerRunCausalProjection`, `TestRunStore_ModelHistorySurvivesReopenInCausalOrder` | A batch is admitted atomically in input order; each processable trigger gets its own durable queued run (never grouped), and runs drain one at a time. Model-facing history is reconstructed from run causality (each prior run's trigger IDs then its persisted output IDs, then the current trigger), so a later trigger's projection observes the earlier run's committed agent output as a separate turn — proven for A/B (`TestSessionService_SecondUserEventObservesFirstAgentOutput`) and A/B/C (`TestSessionService_BatchedTriplePerRunCausalProjection`), and durable across a file-backed reopen (`TestRunStore_ModelHistorySurvivesReopenInCausalOrder`). All event variants, interrupt cancellation, and external-side-effect idempotency remain incomplete. |
| Reject unknown / server-only event types and validate client variants | partial | `TestGolden_RejectsServerOnlyEventType`, `TestSendEvents_ValidatesVariantShape` | Type gating, empty-batch rejection, and validation for the currently modeled client variants are exercised; the full client-event union and every field constraint are not. |
| List events (`GET .../events`): `types` filter, `limit`, `page`, `next_page` | partial | `TestSDK_EventListPaginationAndTypesFilter`, `TestListEvents_CursorIsBoundToSessionAndFilters` | Cursors are bound to the session and normalized filters. Bounds and `processed_at` ordering/keyset semantics remain incomplete. |
| List events: `order`, `created_at[gt|gte|lt|lte]` filters | partial | `TestQuery_CreatedAtNamedFilterUsesProcessedAt`, `TestQuery_ProcessedAtFiltersExactAndFractionalWithinSecond`, `TestListEvents_RejectsInvalidQueryValues` | The public `created_at` query name compares a fixed-width `processed_at` key. Results and cursors are still ordered by internal sequence rather than a `processed_at` keyset, including its null/tie semantics. |
| Send/list/stream share one event JSON shape | partial | `TestGolden_EventIsFlatTaggedUnion`, `TestWorkshop_ReconnectNoGapNoDup`, `TestSDK_EventStream` | One mapper renders event JSON and SSE emits an `event:` discriminator plus `data:` JSON that the Go SDK decodes. The full event union is incomplete. |
| SSE stream and open-then-list reconnect | partial | `TestSession_FullLifecycleWithSSE`, `TestWorkshop_ReconnectNoGapNoDup`, `TestSDK_EventStream` | The SDK stream decoder is exercised, but current reconciliation tests start with empty history and do not perform a real disconnect/reconnect. |
| SSE deletion terminal and EOF | partial | `TestStreamEvents_DeleteTerminalAndEOF`, `TestSessionService_DeletePublishesTerminalEventAndClosesStream` | An active stream receives `session.deleted` with an ID and `processed_at`, then EOF. Backpressure, reconnect-after-delete, and distributed delivery are not proven. |
| Streaming preview of `agent.message` (`event_start`/`event_delta`, opt-in via `event_deltas[]`) | partial | `TestPreviewFrame_WireJSON_Start`, `TestPreviewFrame_WireJSON_Delta`, `TestHub_PreviewOnlyToOptedIn`, `TestAgentCore_EmitsPreviewThenPersistedMessage`, `TestSessionService_PreviewStreamsToOptedInOnlyNeverPersisted`, `TestAnthropic_CreateMessageStream_DecodesSSE` | While an `agent.message` is generated, the stream can emit an `event_start` frame followed by incremental `event_delta` frames carrying a `content_delta` shape, then the authoritative persisted `agent.message` event. Preview frames are delivered **only** to subscribers that opted in via the `event_deltas[]` stream parameter, and are **stream-only, never persisted** — a `list events` call never returns them. The real Messages-API client decodes the upstream SSE (`content_block_delta`/`text_delta`) to drive previews (`TestAnthropic_CreateMessageStream_DecodesSSE`). `agent.thinking` preview and `span.*` events are deferred. The upstream/inbound stream uses `content_block_delta`; our outbound preview uses `content_delta` — they are different wires. The exact outbound SSE `event:` line format for preview frames is `unknown` against the official contract. |
| `processed_at` on-receipt vs queued | partial | `TestAppend_ProcessedOnReceipt`, `TestRunStore_ClaimAndCompleteClosesRunAtomically`, `TestSessionService_SendEventDrivesRun` | Server/on-receipt events are stamped immediately; queued trigger timestamps commit atomically with runtime output and run completion. Exact timing for every variant remains incomplete. |
| Interrupt / tool confirmation / custom-tool result semantics | partial | `TestWorkshop_CustomToolHandoff`, `TestSessionService_CustomToolParksAndResumes`, `TestAgentCore_CustomToolParksWithRequiresAction`, `TestAgentCore_AlwaysAskBuiltinParks` | Custom-tool and `always_ask` calls now park the run: the app emits `session.status_idle` with `stop_reason.type == "requires_action"` and `event_ids` naming the committed `agent.custom_tool_use` / `agent.tool_use{evaluated_permission:"ask"}` event, and a `user.custom_tool_result` referencing that id resumes a fresh run to `end_turn`. Correlation uses the committed event id (pre-assigned by the runtime sink). Still unproven against the official wire: exact `requires_action` payload shape, and the `always_ask` **resume** path (`user.tool_confirmation` → projected `tool_result` + built-in execution) is not yet wired. |

## Tools and sandbox

| Capability | Status | Test | Notes |
|---|---|---|---|
| Parse agent tool declarations (built-in toolset / custom / MCP) | partial | `TestParseTools_BuiltinCustomMCP`, `TestParseTools_RejectsUnknownType`, `TestParseTools_Empty` | `agent_toolset_20260401` with `default_config` and per-tool `configs` (`enabled`, `permission_policy`), `custom` tools, and `mcp_toolset` server references parse into `domain.ToolSet`; unknown types are rejected. Full config-field and validation coverage is incomplete. |
| Built-in tool loop (model `tool_use` → sandbox exec → `tool_result` → repeat) | partial | `TestAgentCore_ExecutesBuiltinToolLoop`, `TestFake_CallsOfferedToolThenEnds` | `AgentCore` loops model↔tool up to 20 turns (`maxToolTurns`), executing enabled built-ins with `always_allow` policy and feeding results back until `end_turn`. `bash`/`read`/`write`/`edit`/`glob`/`grep` execute; `web_fetch`/`web_search` are declared to the model but return `is_error` "not implemented". |
| Built-in tool input schemas sent to the model | partial | `TestAnthropic_SerializesToolsAndToolBlocks` | The per-tool JSON schema we hand the model is an **internal** design choice following Anthropic's public bash/text-editor parameter conventions; it is not part of the Managed Agents public wire and is not asserted against an official contract. |
| `agent.tool_use` / `agent.tool_result` events | partial | `TestAgentCore_ExecutesBuiltinToolLoop`, `TestProjectMessages_ToolUseAndResultPairing`, `TestProjectMessages_KeepsPairedToolUseUnderFilter` | Built-in calls commit `agent.tool_use` and `agent.tool_result` events (correlated by committed event id) and project back into paired Messages-API `tool_use`/`tool_result` blocks. Exact per-variant wire keys are not asserted against the official contract. |
| Custom-tool handoff (`agent.custom_tool_use` → park → `user.custom_tool_result` → resume) | partial | `TestWorkshop_CustomToolHandoff`, `TestSessionService_CustomToolParksAndResumes`, `TestAgentCore_CustomToolParksWithRequiresAction`, `TestFake_CustomToolResultEndsIdle`, `TestProjectMessages_CustomToolResultPairing` | A custom tool call parks the run at `session.status_idle` with `stop_reason.type == "requires_action"` and `event_ids` naming the committed `agent.custom_tool_use`; a `user.custom_tool_result` referencing that id resumes a fresh run to `end_turn`. End-to-end proven; the exact `requires_action` payload shape is unconfirmed against the official wire. |
| Built-in tool run end-to-end via the app layer | partial | `TestSessionService_BuiltinToolRunEndToEnd` | A session-driven run provisions a sandbox, executes a built-in tool, and reaches `end_turn` through the durable run path. |
| `always_ask` permission policy on built-in tools | partial | `TestAgentCore_AlwaysAskBuiltinParks` | An `always_ask` built-in call parks the run (`agent.tool_use{evaluated_permission:"ask"}` + `requires_action`); the **resume** path (`user.tool_confirmation` → projected `tool_result` + built-in execution) is not wired. `ProjectMessages` drops the dangling `tool_use` so the parked call never poisons a real request (`TestProjectMessages_DropsDanglingToolUse`, `TestProjectMessages_DropsDanglingCustomToolUse`). |
| Session-scoped sandbox lifecycle (`internal/sandbox` `SessionManager`) | partial | `TestSessionManager_ReusesSandboxPerSession`, `TestSessionManager_IsolatesSessions`, `TestSessionManager_ReleaseDestroysExactlyOnce`, `TestSessionService_SandboxPersistsAcrossRuns`, `TestSessionService_SandboxIsolatedBetweenSessions`, `TestSessionService_SandboxProvisionedOncePerSession`, `TestSessionService_IdleDoesNotDestroySandbox`, `TestSessionService_DeleteReleasesSandboxExactlyOnce`, `TestSessionService_DeleteTeardownSurvivesRequestCancellation` | A sandbox is **scoped to the session**, not the run. The first run needing tools provisions one logical sandbox; later runs in the same session reuse it, so tool-produced file state **persists across turns**. Different sessions get distinct sandboxes and stay isolated. Entering idle does not destroy the sandbox; deleting the session releases it exactly once. Delete runs that teardown *after* the durable delete has committed, on a context detached from the request context (`context.WithoutCancel`) and bounded by its own timeout, so a client disconnect cannot cancel an in-flight `Destroy` yet a stuck provider cannot hang forever. Ownership lives in a session-scoped manager that wraps the provider inside the `sandbox` package; `AgentRuntime` receives a resolved sandbox and is unaware of the lifecycle. The manager is in-memory: a process restart does not restore an idle session's sandbox, and there is no durable checkpoint, quota, or eviction yet. |
| Local sandbox (`Provider`/`Sandbox`, restricted local process) | partial | `TestLocal_ExecEcho`, `TestLocal_FileRoundTripAndConfinement`, `TestLocal_Timeout` | `internal/sandbox` provides a two-layer interface and a local-process default that confines paths to a work dir, clears the environment, applies a timeout, and caps output. **Dev-grade guardrail, not a security boundary — do not run untrusted code.** Sandboxes are session-scoped (see the row above): provisioned on first tool use and reused across the session's runs. |
| Docker sandbox (real-isolation `Provider`, opt-in) | partial | `TestDocker_*` (skipped without a daemon), `TestResolveSandboxProvider_DefaultsToLocal` | The same `Provider`/`Sandbox` interface has a Docker-backed implementation (shells out to the `docker` CLI, no extra module dependency). It gives **real isolation**: each sandbox is a container with its own Linux namespaces/cgroups, a separate filesystem, and `--network none` by default. Selected at startup via `MANAGED_AGENT_SANDBOX=docker` (default is local); image via `MANAGED_AGENT_SANDBOX_IMAGE` (defaults `alpine:latest`). gVisor (`--runtime=runsc`) can layer under the same interface later with no interface change. Not audited for hostile multi-tenant use (shared host kernel). Docker tests are gated on a running daemon and skip in default offline CI. |
| MCP toolsets | unsupported | `TestParseTools_BuiltinCustomMCP` | Parsed into `domain.ToolSet` but never resolved or executed. |

## Transport

| Capability | Status | Test | Notes |
|---|---|---|---|
| Error envelope with top-level `type`, nested `error`, and `request_id` | partial | `TestGolden_ErrorEnvelopeShape`, `TestRequestIDMiddleware_AllResponses`, `TestSDK_AgentLifecycle` (409) | The common shape and matching response-header/body request ID are exercised; exact per-status error types remain incomplete. |
| `anthropic-beta` required in strict mode | partial | `TestBetaMiddleware_Strict`, all SDK tests (strict server) | Exact token parsing is implemented; tests cover missing and exact-good values but not multi-token or invalid-superset cases. |
| `anthropic-version: 2023-06-01` required in strict mode | partial | `TestVersionAndContentTypeMiddleware_Strict`, all SDK tests | Missing and exact-good values are exercised; route-by-route behavior is not separately asserted. |
| `x-api-key` / `Authorization` auth in strict mode | partial | `TestAuthMiddleware_Strict`, all SDK tests | Strict mode is **header validation, not authentication**: the server checks only that a header is present/valid, not that any credential is genuine or authorized. The test directly covers `x-api-key`, not the Authorization form. |
| 32 MiB body limit → 413 `request_too_large` | partial | `TestGolden_BodyLimitRejects`, `TestGolden_ChunkedBodyLimitRejects` | Known-length and consuming chunked JSON paths are tested; endpoints that do not consume an unknown-length body are not. |
| JSON request/response content type | partial | `TestVersionAndContentTypeMiddleware_Strict` | Strict request parsing accepts `application/json` parameters; JSON response content type is set but not separately asserted across every route. |
| HTTP status codes per error kind | partial | `TestWriteError_MapsKinds` | 400/404/409/413/422 mapped; the `conflict_error` type string is our choice and unconfirmed. |
| `limit` query bound on List Sessions / List Events | partial | `TestListSessions_LimitBoundary`, `TestListEvents_LimitBoundary` | Both list endpoints accept `limit` up to a shared maximum of 1000; a value above 1000 returns a `validation_error` rather than being silently clamped. Defaults (100) and cursor semantics are unchanged. |

## Runtime

| Capability | Status | Test | Notes |
|---|---|---|---|
| Self-hosted agent core (Messages-API-backed loop) | partial | `TestAgentCore_*`, `TestSessionService_MultiTurnProjectsPriorHistory` | The default runtime. Projects the session event log into `messages[]` each turn (`domain.ProjectMessages`), merging adjacent same-role events so the request satisfies the Messages API strict-alternation constraint (`TestProjectMessages_MergesConsecutiveUsers`, `TestProjectMessages_MergesConsecutiveAssistantsAndAlternates`), then calls the stateless Messages API. It runs a multi-step built-in tool loop (`bash`/`read`/`write`/`edit`/`glob`/`grep`; `web_fetch`/`web_search` are declared to the model but return `is_error` "not implemented") over a sandbox, hands off custom/`always_ask` tools by parking, and streams an ephemeral `agent.message` preview. Durable incremental output, interrupt cancellation, thinking/spans, and compaction are deferred. |
| Real Messages API client (`/v1/messages`) | partial | `TestAnthropic_*` | Env-configured (`MANAGED_AGENT_MODEL_*`), Anthropic-shape, `x-api-key` or bearer auth. Serializes `tools[]` and round-trips `tool_use`/`tool_result` content blocks (`TestAnthropic_SerializesToolsAndToolBlocks`, `TestAnthropic_ParsesToolUseResponse`). `CreateMessageStream` opens the real SSE stream (`"stream": true`) and decodes it incrementally: text is streamed per `content_block_delta`/`text_delta` (`TestAnthropic_CreateMessageStream_DecodesSSE`), and tool_use blocks are assembled from `content_block_start` + accumulated `input_json_delta` (parsed at `content_block_stop`; partial_json is not surfaced incrementally). On a non-2xx response the upstream error body (JSON `{"error":{"message":...}}` or plain text) is sanitized, length-bounded, and folded into the returned error so operators see the cause; headers and the API key are never included (`TestAnthropic_BearerAuthAndErrorStatus`, `TestAnthropic_NonJSONErrorBodyIsSurfaced`). Verified against `httptest`; real-service calls are opt-in. |
| Offline fake model client | partial | `TestFake_EchoesLastUserTextAndRecordsRequest` | Deterministic echo `model.Client`; the default agent core uses it with no env, keeping the suite offline. |
| Fake control-plane runtime | partial | `TestSessionService_*`, `TestWorkshop_*` | An in-process `AgentRuntime` double (`agentruntime.Fake`), no longer the default. Drives sessions to `agent.message` + terminal status for control-plane tests without a model layer. |

## Known gaps

- `domain.ProjectMessages` merges adjacent same-role events into one message
  (concatenating their content blocks in order) so the projected conversation
  always alternates roles and forms a legal Messages-API request. Under the
  causal Run history model, multiple `user.message` events queued before
  `drainRuns` claims them are **not** collapsed into one projected user message:
  each processable trigger runs as its own durable run and projects as a
  separate turn (`TestSessionService_SecondUserEventObservesFirstAgentOutput`,
  `TestSessionService_BatchedTriplePerRunCausalProjection`), and the causal
  ordering survives a restart (`TestRunStore_ModelHistorySurvivesReopenInCausalOrder`).
  Merging still fires only when a turn genuinely emits no assistant content —
  e.g. a model turn that produces no `agent.message`, leaving two user turns
  adjacent (`TestProjectMessages_MergesConsecutiveUsers`,
  `TestProjectMessages_MergesConsecutiveAssistantsAndAlternates`). Context
  compaction when a session exceeds the projection limit is still deferred.
- Server shutdown does not cancel in-flight model calls. `drainRuns` runs on
  `context.Background()`, so a SIGTERM does not propagate cancellation to an
  in-progress Messages-API request; cancellation propagation is not implemented.

- `session.error` carries `{error:{type,message}}` but omits the documented
  `retry_status` field. The `retry_status` enum values are unconfirmed from the
  docs, so this stays `partial`; the field's absence is a known gap.
- On an unrecoverable runtime error the session projects to `terminated`, not
  `rescheduling`: there is no attempt/lease/retry mechanism, so `rescheduling`
  (which promises an automatic retry) would be dishonest. No `stop_reason` is
  emitted on the failure path, since the documented `stop_reason.type` union is
  only `end_turn | requires_action`. Transient-vs-unrecoverable classification
  and true rescheduling are not implemented.
- The 409 error `type` string is `conflict_error`; the exact type returned for
  an agent version conflict is unconfirmed, so the status (409) is matched but
  the type string is not.
- Agent and session responses include the SDK keys exercised by the lifecycle
  tests, but several values are empty/default placeholders. The SDK decoder is
  lenient, so field presence and values still require explicit assertions.
- The SSE handler's `event:` + `data:` frames decode, but a true
  disconnect/reconnect boundary has not been tested.
- Agent listing is single-page. Session listing has bidirectional cursors and
  core filters, but deployment/memory matching and mutation-concurrency
  guarantees are incomplete.
- Event time filters compare `processed_at`, but ordering/pagination is still
  based on internal sequence; null, tie, and concurrent-write semantics are not
  established.
- Opaque `multiagent` input is stored with tested replace/null-clear behavior;
  roster resolution, reference validation, and multiagent execution are absent.
- There is no first-class durable pending-action gate. A single custom-tool
  park and its `user.custom_tool_result` resume are implemented and tested
  (`TestSessionService_CustomToolParksAndResumes`), but if other trigger runs
  were already queued before a run parks with `requires_action`, their gating
  and correlation against the parked action are not yet fully modeled; this case
  is not claimed to work.
- The durable queue is single-process and restart recovery is at-least-once. A
  crash after an external side effect but before completion commit may replay
  it; no lease, fencing, or idempotency-key protocol exists yet.
- `422` is used for unsupported operations (e.g. sessions against `self_hosted`
  environments). This is an internal choice.
- The tool loop targets the self-hosted agent core only. Sessions against a
  `self_hosted` *environment* are rejected outright, so the
  self-hosted-environment `user.tool_result` worker path (a client executing the
  tool and returning results) is not implemented.
