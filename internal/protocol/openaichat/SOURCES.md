# Chat codec migration

Pinned source: looplj/axonhub@e863c6fe1942deddd0f6e471fa003c430e5314f0.
Octopus baseline: 98c380bc205b673f3f9c42100ba68397df7a9947.

| Source under llm/transformer/openai | Local | Adaptation |
| --- | --- | --- |
| model.go | model.go, fields.go | Chat wire unions only; namespaced unknown fields and null preserved; text arrays retain their shape, including a single empty text part. |
| inbound_convert.go | inbound_convert.go | Direct Octopus IR; schemas decoded through existing schema representation; annotations, tool restrictions, file/audio/image content. |
| outbound_convert.go | outbound_convert.go | Direct Octopus IR; no session-derived defaults or upstream model policy; same-Chat vendor tools are retained, impossible foreign native operations are rejected before mapping. |
| usage.go | usage.go, usage_convert.go | Token DTO and mappings only, no upstream cost calculation; optional detail objects retain absence/zero distinction. |
| outbound.go, inbound_stream.go | stream.go | Extracted per-frame error/finish observation and choice state; no whole Stream or executor. EOF completes only finished choices; it does not synthesize success for unfinished output merely because usage arrived. |
| model_test.go | upstream_model_test.go | Chat content/stop/tool-choice/request/response round trips. Single-text-array expectation intentionally strengthened to preserve source shape. |
| google.go | extensions.go | Standard Chat extra_content signature DTO only; no Gemini runtime or key/provider policy. |

outbound/chat.go and its relocated tests originate from Octopus's baseline.
They retain URL/auth/header, reasoning-model max_tokens conversion,
Anthropic stop stripping and existing derived Anthropic cache-key behavior.
Only request clone, wire conversion and frame delegation changed. The old
transformer package exports aliases; no executor was moved into this package.

The historical whitelist builder remains for exported DTO compatibility and
baseline tests; production encoding uses the pinned wire model and explicit
validation. Same-Chat non-function tools and vendor tool choices are preserved
instead of being rejected or filtered. Foreign tools without a Chat mapping
still fail explicitly rather than disappearing from the request.

New local tests cover unknown fields, explicit false/zero/null, editable
model/content priority, parallel tool fragments, finish followed by usage,
EOF, error envelopes, closed states, and JSON/tool-fragment fuzzing.
The library is LGPL-3.0 as specified by the pinned NOTICE; historical Octopus
portions retain their separate attribution.

No raw passthrough is added for Chat. Native operations from other protocols
without a Chat mapping still produce an error. Optional foreign reasoning and
citations do not block usable text: original reasoning items retain their
protocol and position in the `reasoning_items` extension; foreign signatures
are metadata, not recycled as native Chat reasoning signatures. Document
citations retain their complete location fields in the `citations` extension
on text parts; URL citations with known positions can map to annotations.
Unmapped stream metadata is retained under `protocol_events`. These are
compatibility extensions, not a claim that Chat providers understand the
source protocol's signed state or native operations.

Late data after a choice finish or terminal marker is accepted, including
usage trailers, and vendor finish reasons are preserved. Duplicate terminal
markers remain idempotent. EOF without a finish or terminal marker, malformed
JSON, context cancellation, closed streams, and meaningful upstream errors
remain failures. Empty `error: {}`, `error: []`, and `error: null` placeholders
alone are not failures; explicit error events or failed statuses still are.
Empty text and refusal remain valid protocol content.
