# Responses codec migration

Source: looplj/axonhub, pinned commit
`e863c6fe1942deddd0f6e471fa003c430e5314f0`.
Octopus compatibility baseline: `98c380bc205b673f3f9c42100ba68397df7a9947`.
P0/P1 integration commits: `f47b558`, `9746da0`.
Upstream `NOTICE` assigns the `llm/` subtree GNU LGPL version 3.0.
The LGPL supplement is retained at
[`../LICENSE-LGPL-3.0`](../LICENSE-LGPL-3.0), with its incorporated GPL
text at [`../LICENSE-GPL-3.0`](../LICENSE-GPL-3.0) and upstream copyright
notice at [`../NOTICE`](../NOTICE). This file records adaptation,
not an additional license grant.

## Source and adaptation inventory

| Pinned source under llm/transformer/openai/responses | Local code | Adaptation |
| --- | --- | --- |
| model.go | model.go, extensions.go | One shared wire model used by both runtime directions. Octopus exported names remain aliases. Raw unknown fields, input audio, refusal, flexible numeric error codes and existing continuation fields retained. Integer-valued float timestamp parsing retained. |
| stream_event.go | stream_event.go | Event/data boundary carried by Octopus StreamFrame; no upstream stream/executor type. |
| outbound_stream.go | outbound/stream.go | Per-attempt frame state; item-to-call mapping, parallel argument reconciliation, semantically equal JSON done deduplication, provisional/final encrypted-content handling, terminal/EOF validation. Emits Octopus StreamEvent directly. |
| inbound_stream.go | inbound/response.go, inbound/extensions.go | Frame emission adapted to existing Octopus event facade; independent tool indices, deterministic completed output order, final output/usage, native frame preservation, reset preserving truncation/request namespace context. |
| request_extensions.go | inbound/extensions.go, outbound/extensions.go | Ordered native input and tools retained in the sole Octopus IR's serializable protocol extension and existing RawInputItems. Existing replay-controlled input/continuation fields remain authoritative. |
| namespace.go | inbound/extensions.go, inbound/response.go | Namespaced declarations/history mapped to flat semantic function names; conflict detection and output-name restoration. Raw same-protocol tools retain namespace structure. |
| inbound.go, outbound_convert.go | inbound/response.go, outbound/response.go, outbound/output.go | Direct wire/Octopus IR conversion, custom/native tools, refusal, opaque reasoning, annotations, response status/incomplete detail. Existing Octopus cache metadata derivation, URL/header behavior and raw replay sanitization retained. |
| stream_event_test.go | upstream_model_test.go | Numeric status and response.created/response.completed string status fixtures adapted from TestStreamEvent_Unmarshal_preserves_numeric_status and TestOutboundTransformer_TransformStream_accepts_string_status. |
| model_test.go, outbound_stream_test.go, namespace_validation_test.go, additional_tools_roundtrip_test.go | migration_test.go | Focused local regressions derived from pinned behaviors: integral timestamp lexemes, native/custom item order, namespace history/collision, parallel delta/done reconciliation, signatures and EOF. Tests directly use Octopus codecs; no upstream harness/executor imported. |

No AxonHub pipeline, router, executor, provider registration, key rotation,
session storage, database, OAuth, automatic prompt-cache optimization or second
complete request/response IR is included.

## Wire capability coverage

| Capability | Same protocol | Cross protocol |
| --- | --- | --- |
| Text, instructions, system/developer history, refusal | Typed wire and raw input order | Semantic messages / refusal |
| Function calls, results, parallel arguments | IDs and item order, delta/done reconciliation | Dense function indices and arguments |
| Namespace, custom/native tools, unknown items | Original nested definitions/items retained | Functions use namespaced semantic names; unsupported native tool types require Responses, not lossy text conversion |
| Reasoning and encrypted content | Ordered raw items; signature emitted once at final snapshot | Existing Octopus reasoning/signature fields |
| Images/files/input audio | Native input contents retained | Supported existing IR image/file/audio mappings; unsupported content explicitly rejected |
| Output audio / unknown Chat image chunks | Native Responses frames can be retained | Unsupported Chat output audio/image chunks explicitly rejected |
| Annotations | Raw fields and zero offsets retained | Supported URL/file annotations; unrepresentable citation types explicitly rejected |
| Usage | Snapshots, cache read/write, reasoning tokens | Existing Octopus billing semantics; OpenAI cache writes stay in PromptTokensDetails.WriteCachedTokens |
| Completion/error/incomplete | Terminal status/error/detail preserved | Existing finish reasons and ResponseError |
| n > 1 | Responses does not offer multiple choices | Explicit validation error |
| Unknown fields | Protocol-namespaced wire fields/items | No arbitrary copying into another protocol |

## Exported compatibility surface

Both OpenAI facade files retain `ResponseInbound` / `ResponseOutbound`,
`ResponsesRequest`, `ResponsesInput`, `ResponsesItem`,
`ResponsesInputAudio`, `ResponsesReasoningSummary`, `ResponsesAnnotation`,
`ResponsesTool`, `ResponsesToolChoice`, `ResponsesTextOptions`,
`ResponsesTextFormat`, `ResponsesReasoning`, `ResponsesResponse`,
`ResponsesUsage`, `ResponsesError`, `ResponsesStreamEvent`.
Inbound also retains `ResponsesContentItem`, `ResponsesContentPart` and
`FlexibleJSONString`.

`ConvertToResponsesRequest` remains used by relay/relay_forward.go and
relay/ws_passthrough.go; `MarshalResponsesInputItems` remains available to
replay helpers/tests. Existing `DerivedAnthropicCacheMetadata` is retained
for the Chat codec's inherited Octopus cache behavior.

`TransformStream` and `TransformStreamEvent` delegate to the same
`TransformStreamFrame` state. `EndStream` rejects EOF before a semantic
terminal even after bare [DONE]; `CloseStream` prevents further pushes.
No transport, routing, key, retry or sticky implementation belongs here.

## Expected differences and integration notes

- String error codes and provider timestamp variants decode; timestamp metadata
  remains preserved in the native response even when its IR projection is zero.
- Final-only text/tool arguments are emitted; matching done snapshots do not
  repeat streamed deltas. Corrected final snapshots update logs/replay and native
  output without concatenating replacements onto previously delivered deltas.
- SSE contains explicit event lines; old tests' SSE parser now validates
  agreement between event and data type.
- Tool start is emitted once; the former duplicate-start assertion is updated
  while preserving the dense-index regression assertion.
- Bare [DONE] no longer proves completion; EOF must follow a semantic terminal.
- P6 safety audit: foreign/unproven signatures, redacted reasoning, unsupported
  native blocks and tool results fail encoding rather than being reused as
  Responses encrypted_content or silently dropped. Plain thinking text remains
  convertible. Responses-origin raw items/frames retain opaque data. Non-stream,
  legacy stream and event paths reject multiple choices before raw-frame output.
  Streamed Chat text arrays are encoded; unrepresentable audio/image arrays and
  native choice extensions are explicitly rejected.
- The common IR aggregator owns final ordered native/citation/reasoning
  aggregation. Responses attaches output/content indices, item/call IDs and
  sequence numbers; raw frame is OpenAIResponses.Fields["stream_frame"], raw
  native item is ProtocolItem{Format: APIFormatOpenAIResponse, Position: output_index}.

Validation commands and their actual final outcomes are reported by the
integrating task. This source note does not claim completion of tests not run.
Rollback is by reverting the eventual integration commit(s), including facade
aliases and shared IR dependencies; do not reset an unrelated dirty worktree.

## Octopus compatibility follow-up

Native tool parameter schemas, tool choices, reasoning options, raw-only input
variants and prompt-based requests are passed through to Responses upstreams.
Missing optional stream item/part metadata and response.done do not invalidate
an otherwise completed response. Refusal is normal output; content filtering is
represented as incomplete rather than an invented upstream failure. Actual
error events, malformed outer JSON and unconfirmed EOF remain failures.

Regression fixtures are in permissive_fields_test.go, stream_compatibility_test.go,
snapshot_metadata_test.go, and the relay protocol compatibility tests. Shared
behavior and retained boundaries are documented in ../COMPATIBILITY.md.
