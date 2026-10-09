# Chat codec migration

Pinned source: looplj/axonhub@e863c6fe1942deddd0f6e471fa003c430e5314f0.
Octopus baseline: 98c380bc205b673f3f9c42100ba68397df7a9947.

| Source under llm/transformer/openai | Local | Adaptation |
| --- | --- | --- |
| model.go | model.go, fields.go | Chat wire unions only; namespaced unknown fields and null preserved; text arrays retain their shape, including a single empty text part. |
| inbound_convert.go | inbound_convert.go | Direct Octopus IR; schemas decoded through existing schema representation; annotations, tool restrictions, file/audio/image content. |
| outbound_convert.go | outbound_convert.go | Direct Octopus IR; no session-derived defaults or upstream model policy; unsupported native content rejected by codec before mapping. |
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
validation. Non-function tools cannot be silently filtered in production.

New local tests cover unknown fields, explicit false/zero/null, editable
model/content priority, parallel tool fragments, finish followed by usage,
EOF, error envelopes, closed states, and JSON/tool-fragment fuzzing.
The library is LGPL-3.0 as specified by the pinned NOTICE; historical Octopus
portions retain their separate attribution.

No raw passthrough is added for Chat. Unknown semantic native events from
other protocols produce an error. Document citations have no lossless Chat
location representation and are rejected; URL citations with known positions
can map to annotations. Empty text and refusal remain valid protocol content.
