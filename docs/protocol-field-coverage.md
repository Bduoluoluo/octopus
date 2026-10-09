# Protocol capability coverage

Status is implementation-stage specific. A wire DTO field alone is not a claim of end-to-end support.

| Capability | P1 wire / IR | Production conversion / stream |
| --- | --- | --- |
| Chat roles, text/arrays, tools, schema, sampling, refusal, audio | Pinned wire DTO present; Octopus IR retained | Pending P2/P3 |
| Responses input/output, native/custom/namespaced tools, opaque reasoning | Pinned wire DTO present; raw positioned IR items added | Pending P2/P3 |
| Anthropic system/content/tool/thinking blocks | Pinned wire DTO present | Pending P2/P3 |
| Document citations object true/false/null | Round-trip tested | Pending P2 |
| Text citations array, assistant history, empty/missing/null | Round-trip tested | Pending P2 |
| Citation document/character/page/block/search/URL positions, zero, unknown fields | Typed IR + round-trip tested | Pending P2/P3 |
| citations_delta | Wire and IR fields present | Pending P3 |
| SSE event/id/data, EOF/Close, reset | Optional interfaces present | Pending P3/P4 |
| Native item positions and independent item/output/content/sequence indices | IR fields present | Pending P3 |
| Attempt clone isolation | Nested message/tool/raw mutation test passes | Pending P2 integration |
| Existing WS/replay/compact and billing rules | Unmodified | Pending integration regression |

The historical Octopus MIT notice and pinned AxonHub LGPL provenance are tracked separately. The migration does not change the root project license.
