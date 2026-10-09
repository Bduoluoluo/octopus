# Protocol field capability coverage

Only OpenAI Chat Completions, OpenAI Responses and Anthropic Messages run.
The tables distinguish public conversions from native same-protocol fidelity.
Fixture success is not a claim that every feature translates between vendors.

| Fields / capability | Production behavior | Main evidence |
| --- | --- | --- |
| Text, instructions, system/developer/user/assistant/tool history, function schema, tool calls/results | All 3×3 directions, streaming and non-streaming; outgoing request and downstream wire parsed again | `protocol/matrix_test.go`, 18 cases |
| Chat content string/array, explicit empty string, file/audio/image/video payload, role/refusal | Pinned wire -> sole IR -> wire; same-protocol fields preserved; unsupported target modalities rejected | `openaichat/codec_test.go`, relocated chat tests |
| Chat n, sampling/stop, response_format JSON schema, allowed_tools subset/mode | n retained for Chat; n != 1 rejected by Responses/Anthropic; schema unknown keywords retained as raw schema | Chat and Responses migration tests |
| Chat unknown response/choice/message/content/tool fields | Namespaced extension fields, authoritative model/content edits win; not copied blindly to another protocol | `TestChatStreamSupplementalFieldsAndArrayContent`, `TestChatFieldPreservationAndAuthoritativeEdits` |
| Responses ordered message/function_call/function_call_output/reasoning/item_reference | Raw ordered input stays authoritative through replay helpers; function arguments remain strings | `TestPinnedRequestNativeOrderAndReplayOverrides`, replay suite |
| Responses custom, namespace, native tools and unknown items | Same protocol preserves native definitions/items; namespace functions map to collision-checked semantic names; unrepresentable cross-protocol data errors | `openairesponses/migration_test.go`, cross-capability tests |
| Responses opaque reasoning encrypted_content | Preserved with item ID/order; final snapshots deduplicate; cannot be reused as another provider's signature | reasoning/signature, aggregate and cross-boundary tests |
| Anthropic system string/array, nested tool_result text/image/document | Same protocol preserves raw ordered blocks and source fields; common semantic tool result mapping | `anthropic/native_roundtrip_test.go` |
| Anthropic native server_tool_use/results, redacted thinking/signatures | Ordered native extensions and per-block stream state; unknown blocks retained same protocol, explicit unsupported errors cross protocol | native round trips, stream aggregate contract |
| document.citations config | Object with enabled true/false/null; distinct from text citation arrays | `anthropic/citations_test.go` |
| text.citations including assistant history | Array, missing/null/empty preserved; wrong shapes rejected | union and history fixtures |
| char/page/content_block/search_result/web citation positions | Pointer positions preserve zero; document/search indices, titles, cited_text, encrypted_index and unknown fields retained | union, native and multi-block stream fixtures |
| citations_delta | Parsed -> ContentCitation -> original block aggregate -> Anthropic SSE; no sidecar-error bypass | `citations_stream_test.go`, ordered aggregate tests |
| Citation cross conversion | Supported URL/location annotations map where target can represent them; document/block citations without equivalent target location error rather than textual invention | cross-capability and Responses citation tests |
| id/call_id/item_id/output_index/content_index/sequence | Distinct fields; dense tool indices separate from native output indices | parallel and output-index tests |
| Stream role/text/refusal/tool parameters/thinking/signature | Per-attempt state and per-block ordering; multiple semantic events per frame; final snapshots not appended twice | per-protocol frame tests, aggregate contracts |
| Usage missing, zero, cumulative snapshots, cache read/create/TTL and reasoning | Existing Octopus accounting adapters retained; snapshots replace/merge by presence, not sum whole totals repeatedly; nil usage remains missing | usage, cache-ratio, metrics tests |
| Chat finish_reason then usage then DONE | Choice finish is not transport completion; trailing usage consumed; unfinished EOF errors | Chat lifecycle fixtures |
| Responses completed/failed/incomplete and bare DONE | Semantic terminal required; error/incomplete preserved; bare DONE does not prove success | Responses terminal/EOF fixtures |
| Anthropic message_stop/error, split signatures | Lifecycle terminal and errors are distinct; signatures accumulate at original block | Anthropic frame/aggregate fixtures |
| SSE boundaries and EOF/Close/cancel/reset | Existing go-sse parser; event/data/id frames, multi-event output; unsuccessful attempt reset before business output only | relay lifecycle + SSE contract/fuzz tests |
| Same-protocol passthrough | Existing eligibility only (Responses/Anthropic); raw downstream bytes retain controlled model/cache rewrites; sidecar once | retained passthrough, cache-ratio and lifecycle tests |
| Model aliases, ParamOverride, PromptSuffix, headers/beta, accounting | Existing ordering and business code retained; URL/auth builders remain Octopus's | relay tests, headers/suffix/cache tests |
| Responses compact, GET WebSocket and replay | Existing endpoints and session/connection policies retained; exported helpers remain aliases/delegates | compact/WS/replay tests; symbol inventory |
| Retired IDs/history/import | Stable numbers; cannot create/enable; original records/bindings readable and retained | registries, op/sitesync/handlers retirement fixtures |

Protocol source and runtime locations are in
`protocol-upstream-manifest.json` and per-protocol `SOURCES.md`.
Exported compatibility references are in `protocol-exported-symbols.json`.

Unknown fields are preserved at the structural scopes represented by each
wire extension (top-level, message/block/item/tool and supported nested fields).
This does not make arbitrary unknown semantics translatable. Same-protocol
unknown stream frames remain native; other encoders reject semantic frames
they cannot represent. No provider-native feature changes candidate order.

The historical MIT attribution and pinned LGPL source are separate. No second
complete AxonHub IR, pipeline/executor, key/session/database manager, provider
platform or upstream cost calculator is included. The root license is unchanged.
