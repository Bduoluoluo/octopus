# Anthropic migration slice

Pinned source: `looplj/axonhub@e863c6fe1942deddd0f6e471fa003c430e5314f0`.
Octopus conversion baseline: `98c380bc205b673f3f9c42100ba68397df7a9947`.
This slice builds on the parent's P1 wire DTO import and P2 relocation, rather
than replacing the parent worktree. No upstream executor or second IR is used.

| Source | Local files/functions | Adaptation |
| --- | --- | --- |
| `llm/transformer/anthropic/model.go` | `model.go`, `fields.go`, `citations.go`, `compat.go` | Direct wire types; citations object/array union; raw unknown fields; explicit false/zero/null and nested source preservation. |
| `llm/transformer/anthropic/inbound_convert.go`: `convertToLLMRequest`, `convertImageSourceToLLMImageURLPart`, `convertToAnthropicResponse`, native response-content restoration | `inbound/messages.go`, `native.go`: `ContentPart`, `CaptureContent`, `RestoreContent` | Direct Octopus IR mapping; ordered native block items, tool-result nested image/document/text, system arrays, document sources. Octopus token estimates, metadata and signature compatibility remain. |
| `llm/transformer/anthropic/outbound_convert.go`: request, tool and response conversion | `outbound/messages.go`, `outbound/native.go` | Existing Octopus mappings retained and enhanced with native restoration; no provider/platform selection or cache optimization imported. |
| `llm/transformer/anthropic/outbound_stream.go`: `streamState`, `transformStreamChunk` | `outbound/stream.go`: `TransformStreamFrame`, `EndStream`, `CloseStream` | Per-frame state, block-indexed parallel tools, signatures, citations, raw native events, terminal validation. Old `TransformStream` and event API delegate to this state. |
| `llm/transformer/anthropic/inbound_stream.go`: stream lifecycle, content/tool/citation emission | `inbound/stream.go`: `TransformStreamEvents`, `ResetStream` | Event-to-SSE state machine without upstream Stream/executor; multiple output events per input, per-block indices, shared Octopus aggregation and explicit reset preserving input estimate. |
| `llm/transformer/anthropic/usage.go` and existing Octopus accounting adapter | `usage.go`: `MergeUsage`; `outbound/messages.go`: `convertAnthropicUsage`; `inbound/messages.go`: `convertUsage` | Presence-sensitive cumulative usage snapshots, explicit zero resets; unchanged Octopus cache accounting. Generated message_delta omits unset input/cache zero fields; parsed explicit zeros are retained. |

The upstream `llm/` source is LGPL-3.0 as recorded by the parent's protocol
NOTICE and license files. Historical Octopus transformer provenance remains
separate. Existing Octopus tests were relocated by the parent and retained.

Added local tests:

- `native_roundtrip_test.go`: ordered request/response blocks, native tools,
  nested tool_result multimodality, document sources, citations, system arrays,
  unknown properties, exact large numbers, zero/false/null and empty arrays.
- `outbound/frame_test.go`: parallel tool fragments, terminal/EOF/error state,
  closed lifecycle, SSE event/data conflicts, explicit-zero usage snapshots.
- `inbound/stream_metadata_test.go`: metadata aggregation without output,
  unsupported supplemental payload rejection, legacy/event shared state and
  reset isolation while preserving the parsed input estimate.
- Parent-added `citations_stream_test.go`, `citations_test.go` and
  `upstream_model_test.go` remain in place.

Capability boundaries and parent integration:

- Generic streamed audio, generated images, logprobs and annotations without a
  lossless Anthropic source-location mapping produce an explicit conversion
  error. Native `ContentCitation`/`citations_delta` remains supported.
- Informational Created/SystemFingerprint/ServiceTier/provider metadata is
  retained in the shared aggregate and does not generate an Anthropic frame.
- Unknown same-protocol stream events are represented as `NativeItem` with
  Anthropic format and Position=-1; the Anthropic encoder preserves the frame
  data. Other encoders must reject semantic native events they cannot encode.
- Parent owns common aggregation of `NativeItem`/ContentIndex and ordered
  thinking/signature blocks. This slice does not change shared IR or relay.
- No SSE network reader is introduced: the existing relay supplies complete
  `StreamFrame` event/data boundaries. No new Stream is allocated per frame.
- Original Octopus headers, beta/default rules, key injection, parameter
  overrides and cache breakpoint policy remain local behaviors; no AxonHub
  routing, retry, execution, persistence or automatic cache policy is imported.

Validation at handoff uses Go 1.25.0 and workspace GOCACHE. Anthropic tests and
the full 18-case public conversion matrix pass; Anthropic race, vet and package
build pass. Parent remains responsible for full integration/race/build results.
