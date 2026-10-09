# Three-protocol migration

## P0: audit and freeze

- Octopus baseline and initial HEAD: `98c380bc205b673f3f9c42100ba68397df7a9947`.
- Requested AxonHub source: `e863c6fe1942deddd0f6e471fa003c430e5314f0`.
- Initial worktree: only untracked `octopus_axonhub_protocol_migration_design.md`; this user-owned task specification is not modified or staged.
- Applicable guidance: repository `CLAUDE.md`, including `docs/TERMINOLOGY.md`; no additional AGENTS.md or CLAUDE.md found in the repository or ancestor directories.
- Initial validation: `go test -mod=readonly ./...` passed using Go 1.26.3 windows/amd64 (cached results). This is not yet a Go 1.25 validation.
- Migration stages are separate commits. No production switch until the corresponding capability tests pass.

### Protected behavior

Candidate generation/order, model mapping, keys, sticky, retry rounds, Retry-After, circuit/outlier decisions, replay conditions and persistence, WebSocket connections, timeouts, heartbeat, billing, database IDs and management behavior are preserved. Relay changes are restricted to protocol frames, lifecycle hooks, attempt-state reset and helper type adaptation. Removing retired runtime protocols is a separately documented intentional change.

### Existing protocol boundary

Factories construct a fresh inbound/outbound object. Retained interfaces are `Inbound`, `Outbound`, `OutboundStreamEventTransformer`, `InboundStreamEventTransformer`, and `PassthroughCapable`. Chat must not acquire raw passthrough eligibility. Existing Responses and Anthropic passthrough retain their decisions and controlled model/header/cache rewrites.

`relay_stream.go` currently discards the SSE event name before decoding. Passthrough metric collection currently parses a buffered stream and ignores some parser errors; migration must introduce explicit lifecycle/error handling without replaying already-consumed events.

### Exported symbol callers

| Compatibility symbol | Production callers | Contract |
| --- | --- | --- |
| `inbound.Get`, `outbound.Get` and type constants | relay, handlers, helper, grouphealth, model/op/sitesync | Fresh instance; stable numeric IDs |
| `openai.ConvertToResponsesRequest` | `relay/relay_forward.go`, `relay/ws_passthrough.go` | Request conversion; Octopus owns transport |
| `openai.MarshalResponsesInputItems` | `relay/ws_session.go` | Replay message encoding without history reordering |
| `openai.ResponsesItem`, `openai.ResponsesUsage` | `relay/compact.go` | Existing compact response and usage contract |
| `SetOpenAIResponsesOptions`, `GetOpenAIResponsesOptions` | replay/ws session and protocol code | Removed continuation options must stay removed |
| `SetOpenAIRawInputItems`, raw output items | replay/ws session and protocol code | Preserve opaque items and their positions |
| `GetInternalResponse` | relay metrics/logging/replay | Complete aggregate; never consume stream twice |

### Validation and completion tracking

P0-P5 are committed; P6 contains the final validation, boundary fixes and delivery record. Functional/build gates pass with the specific race and performance limitations listed below. Intermediate commits are not independent deployment recommendations.

P1 adds selected upstream wire DTOs in three protocol packages, local frame/lifecycle interfaces, namespace extensions and a deep request clone. It does not switch production traffic. No upstream llm Request/Response or executor is imported. Upstream usage DTOs exclude cost fields/calculation and provider-specific accounting. Citation wire unions locally extend the pinned source with document configuration, complete position fields, unknown fields, explicit false/null and assistant history support. Targeted protocol and IR tests pass under Go 1.26.3 and Go 1.25.0.

`go test -mod=readonly -count=1 ./internal/transformer/... ./internal/relay/...` passed (relay 27.438 seconds). The baseline had no benchmarks in those packages; a fixed public request benchmark was added before modifying production code.

Baseline (Go 1.26.3, windows/amd64, Intel i7-14700F; single run, not a statistical comparison):

| Request round trip | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Chat | 9483 | 4615 | 44 |
| Responses | 24001 | 12466 | 165 |
| Anthropic | 256196 | 177331 | 2381 |

`python scripts/audit-retired-protocols.py data/data.db` found no retired channels, no groups referencing retired channels and no groups lacking a configured enabled supported channel with an enabled nonempty key. This is a local configuration snapshot, not a deployment-wide health assessment. The script uses SQLite read-only mode, a read transaction and query-only pragma, and never selects secret key values for output. Run it against each deployment before upgrading. MySQL/PostgreSQL deployments require an equivalent read-only query/export; this SQLite tool does not inspect them.

The pinned AxonHub archive was downloaded from the SHA-addressed GitHub codeload endpoint. Its NOTICE assigns `llm/` to LGPL-3.0, subject to per-file overrides. The old Octopus transformer MIT attribution must remain separate from newly adapted LGPL sources.

### P2: direct codecs and compatibility facades

Pinned wire models now feed Octopus's sole IR directly. Responses and Anthropic baseline codec/tests were moved into their protocol directories and selectively enhanced; Chat conversion uses the pinned direct mapping. There is no upstream llm.Request/Response or JSON conversion between two complete IRs. Exported aliases retain the existing relay helpers and types. The actual production references are in `protocol-exported-symbols.json`; per-protocol SOURCES.md records source functions and local adaptations. The manifest includes source SHA256 hashes and distinguishes relocated Octopus files from newly selected upstream logic.

The codec integration includes the minimum frame-state implementation needed by the compatibility facades. P3 separately gates aggregate fidelity, stream lifecycle and adversarial fixtures; P4 alone wires those lifecycle hooks into HTTP/WS relay. This dependency overlap avoids introducing a second temporary stream engine.

`go test -mod=readonly ./internal/protocol/... ./internal/transformer/... -count=1` passes on Go 1.25.0. The 18-case public matrix sends each converted request to a local fake HTTP upstream and verifies instructions, text, tool definitions/history/results; non-stream output is parsed again and actual downstream SSE is also decoded again to verify text, tool IDs/arguments, usage and completion. It does not claim every native feature supports every cross-protocol direction.

Protocol fixes include block-discriminated citations, source item order, function argument string encoding, unknown fields and explicit zero/false/null, parallel tool indices, and errors for unrepresentable cross-protocol content. Octopus's existing Anthropic cache-key derivation, sampling/default behavior and accounting adapters remain; AxonHub's product defaults and automatic cache optimization were not imported.

The preserved Chat upstream tests use testify v1.11.1 plus its three indirect dependencies already present in go.sum; no new runtime dependency or copied upstream module file is introduced. Go remains 1.25.0.

### P3: frame-state and aggregation gate

All legacy stream entrypoints delegate to the frame/event state held by the same adapter. Choice finish is separate from transport completion; trailing Chat usage is consumed. Responses requires a semantic terminal, distinguishes incomplete/failed, and reconciles final snapshots with emitted deltas. Anthropic maintains block-indexed citations, signatures and parallel tool arguments. EOF on unfinished streams errors; state reset preserves parsed request context and cannot authorize a retry after output.

The shared aggregate retains ordered native items, text-block citations and reasoning/signature fragments, raw Responses output, cumulative usage, and status/error metadata. Malformed native sidecars are surfaced rather than ignored. No complete second request/response IR or per-chunk whole Stream exists.

The new 18-case matrix, citations stream fixtures, metadata/reset tests and ordered aggregate contract tests pass under Go 1.25.0 along with all retained protocol tests. Limited fuzz completed: Chat request union 383843 executions, Chat tool fragments 25038, citation union 324850 (10-second budgets, two workers). These are finite runs, not a proof of exhaustive coverage. Full relay lifecycle wiring and SSE contracts are tracked in P4/P6.

### P4: relay frame and lifecycle integration

Relay continues to use its existing mature SSE reader, guarded transform, cache-ratio wrapper, first-token/heartbeat handling and writers. The narrow changes carry event/data/id into the codec, invoke EOF and Close hooks, and clear unsuccessful attempt protocol state only before business output. HTTP and existing WS paths share that boundary; connection/reconnect/replay/sticky policies are unchanged.

Raw passthrough is still raw output with the existing controlled model rewrite. Its sidecar decodes each event once as it arrives, propagates errors, and never feeds the buffered stream to the same parser again. Cache ratio is rewritten before decoding once; model alias rewriting stays after encoding. No Chat passthrough path was added.

`go test -mod=readonly ./internal/relay/... -count=1` passes on Go 1.25.0, including existing WS/replay, model mapping, failover/timeout, heartbeat and metrics tests. New lifecycle tests verify merged/event-only frames, single consumption, partial cancellation, missing terminal, Close errors and attempt reset. Full race detects the pre-existing `TestEarlyHeartbeat_DelayedFirstHeartbeat` recorder race; the same failure was reproduced in the pristine baseline. All protocol/transformer/balancer/stream packages pass race. The existing test was not deleted or weakened.

### P5: runtime retirement and historical data

Factories now register only inbound/outbound 0/1/2. Historical outbound 3/4/5 and inbound 3 remain reserved and are never reused. Native Gemini/Volcengine/Embedding codecs and independent Images endpoints are removed; the retained POSTs, compact, GET Responses WS and models/admin routes are asserted by tests.

Create/enable and manual type changes reject unsupported protocols. Disabled/deleted/exported historical records remain manageable; backup import retains known retired type IDs and their original enabled state. No upgrade code rewrites channels or removes memberships. Projection and sync skip retired runtime buckets while preserving their saved bindings/group references. Model/vendor names are not filtered.

Affected op/sitesync/model/helper/grouphealth/handler/registry packages pass their tests under Go 1.25.0. Retired codec-only tests were removed with those implementations; mixed business tests retain active-protocol assertions and change only retirement expectations. Frontend TypeScript and production build pass using the locked pnpm 11.1.2 project. The machine uses Node 24.16.0 instead of the declared Node 22; the build warns but succeeds. The read-only local SQLite audit reports no retired channels or affected groups.

### P6: final validation and delivery

The final boundary audit additionally rejects native/custom/image-generation items and foreign opaque signatures before an encoder can silently omit them or turn them into another protocol's function/thinking block. Chat supplemental arrays/audio/choice extensions survive the event path. Generic reasoning text remains translatable. Native same-protocol data retains provenance. Tests that formerly expected fabricated Gemini/foreign signatures now assert explicit rejection, with the exact changes documented in each protocol SOURCES.md; they were not deleted to conceal failures.

Validation uses Go 1.25.0, Windows amd64, workspace GOCACHE and GOTELEMETRY=off. Source code was frozen for final checks. No remote paid model calls or deployment were performed.

| Check | Result / evidence |
| --- | --- |
| Full Go tests | `go test -mod=readonly ./... -count=1` passes; local log `.tmp/protocol-migration/all-tests-final.log` |
| Final finite protocol fuzz | Chat request union 556806, tool fragments 4706, citation union 461275 executions; all pass, 10-second budget/two workers; logs `chat-request-fuzz-final.log`, `chat-tools-fuzz-final.log`, `citations-fuzz-final.log` |
| Go 1.25 vet/build | `go vet -mod=readonly ./...` and `go build -mod=readonly ./...` pass |
| Full required race | Command executed, overall FAIL only at unchanged `TestEarlyHeartbeat_DelayedFirstHeartbeat`; all other package results pass. Final log `race-release-candidate.log`; baseline failure `race-baseline.log` |
| 18 public conversions | Pass, including local HTTP request inspection and downstream non-stream/SSE reparsing |
| citations union/history/zero positions/delta/multiple blocks | Pass, including wrong-shape rejection, unknown fields and aggregate round trip |
| native tools/order/reasoning/signatures/usage/EOF/reset | Pass in protocol and shared IR fixtures |
| baseline vs migrated relay contracts | Same existing assertions pass on pristine baseline and migrated tree: candidate/key preference, retry/failover, sticky, exact replay, HTTP/WS continuation, compact, alias mapping, suffix, cache ratio and metrics |
| Core policy diff audit | relay.go, balancer/, metrics.go, ws_session.go, ws_client.go and responses_replay_store.go unchanged from baseline |
| SSE contract and fuzz | Pass; segmented/coalesced read, CRLF/multiline/comments/event-only, invalid JSON propagation, limit and cancellation; 4757 executions in a finite 10-second run |
| Frontend | pnpm TypeScript check and production build pass (Node24 environment warning; Node22 declared) |
| Data compatibility | Read-only local audit empty; retired type import/state/binding/group fixtures pass; no live DB writes |
| License/package checks | Pinned LGPL and incorporated GPL text, historical MIT notice, SHA/source manifest included in archives and Docker COPY; bash syntax check passes |
| Final performance snapshot | Same Go1.25 compiler/fixtures, three alternating runs: Chat/Responses request conversion time is 5.97×/6.14× baseline; Responses 4096-delta retained heap is 2.989→12.847 MiB. Local conversion microbenchmarks, not service QPS/RSS; full method and source hashes in performance report |

Changed files are listed in `protocol-modified-files.json`; field-level boundaries in `protocol-field-coverage.md`; sources and hashes in `protocol-upstream-manifest.json`; production compatibility references in `protocol-exported-symbols.json`. `protocol-performance.md` reports time, allocations, first-frame latency and sampled/retained heap with reproducible fixtures and limitations.

Expected differences from baseline: retired endpoints are unavailable; text arrays/unknown fields/citations are retained; raw item order and tool argument strings are corrected; partial streams and explicit errors cannot masquerade as completed success; native cross-protocol data without an equivalent representation now errors; invalid foreign signature shims are removed. These are protocol correctness or explicit retirement changes, not new candidate selection/retry policies.

Remaining limitations: full race is not green because the unchanged heartbeat test concurrently reads httptest.ResponseRecorder while its heartbeat goroutine writes (baseline reproduction saved); no live upstream end-to-end or Docker daemon build was performed. Finite fuzz/fixture tests do not prove arbitrary undocumented extensions translatable. Performance increases are measured and reported, not claimed away; this migration does not cap process RSS or change existing full-stream log retention. No production release/push is part of this task.

### Deployment rollback procedure

Before deployment, back up database and configuration. Revert migration commits in reverse stage order or deploy the baseline image. Do not renumber, delete or rewrite persisted channel types. Preserve the user-owned task specification and unrelated changes during rollback.

P0 `f47b558`, P1 `9746da0`, P2 `a381683`, P3 `3bb3039`, P4 `cb905ba`, P5 `fec9b36`; P6 is the delivery commit containing this final report. After preserving any later work, revert P6 then P5 through P0 in that order. On a clean deployment checkout, `git revert --no-commit 98c380bc205b673f3f9c42100ba68397df7a9947..HEAD` followed by review and a rollback commit reverts this contiguous migration series; do not use that range after unrelated commits without selecting only the seven migration commits. Alternatively redeploy the known baseline artifact with the saved data/config. No schema renumbering or destructive migration needs reversing.
