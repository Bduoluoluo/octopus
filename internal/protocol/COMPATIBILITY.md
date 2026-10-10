# Protocol compatibility policy

The supported runtime protocols remain OpenAI Chat Completions, OpenAI Responses,
and Anthropic Messages. Compatibility checks must not classify harmless provider
extensions or stream lifecycle variations as upstream model failures.

## Relaxed checks

| Area | Behavior |
| --- | --- |
| Chat requests | Preserve same-protocol vendor tools, tool choices, and unknown content fields rather than filtering them out. |
| Chat output | Preserve custom tool calls, citations, vendor finish reasons, late deltas and usage snapshots. |
| Cross-protocol Chat metadata | Keep foreign opaque reasoning as explicitly namespaced metadata, not a native reusable Chat reasoning signature. These extensions are not guaranteed to be understood by every Chat upstream. |
| Responses tools | Keep native namespaces, custom tools, variant parameter schemas and tool choices; let the Responses upstream validate them. |
| Responses request options | Preserve unknown reasoning values and nested provider fields. Raw-only inputs and prompt-based requests stay on Responses channels. |
| Responses timestamps | Preserve original timestamp JSON. Project usable values to integer seconds for logging; unusable metadata does not discard generated output. |
| Responses streams | Permit missing optional item/part metadata and the response.done terminal alias. Final text and tool snapshots may correct earlier deltas without concatenating incompatible replacements. |
| Reasoning signatures | Accept updated Responses signatures and use the final snapshot. Keep signatures separated by reasoning item. |
| Anthropic content | Preserve citation configuration variants and unknown native blocks as raw fields. |
| Anthropic streams | Preserve native event/data/id and tolerate repeated lifecycle notifications, late deltas and provider extensions. |
| Representable cross-protocol metadata | Convert refusals to their destination text/refusal representation and URL citations to destination citation structures. |
| Empty error placeholders | An empty error container alone is not failure. Explicit failure events/statuses and meaningful error details still fail. |
| Completed streams | An explicit non-error completion may have no text. A stream with no frames or no confirmed completion remains distinct. |
| Incorrect MIME headers | Bounded sniffing accepts actual SSE framing despite a wrong Content-Type; HTML, JSON errors and malformed streams are not treated as successful SSE. |
| Native tool argument fragments | Preserve incomplete argument text reported in a completed/truncated native response. Do not fabricate valid JSON arguments or classify model-generated argument text as an API transport error. |

## Boundaries retained

- Authentication, API-key permissions, routing, channel/key selection, retries,
  sticky sessions, parameter overrides and billing policies are unchanged.
- HTTP error statuses, meaningful HTTP-200 error envelopes, error events,
  cancelled/failed response statuses and read/cancellation errors remain failures.
- Malformed outer JSON, invalid URLs, resource limits and genuinely incomplete
  transport streams are not accepted as complete successful calls.
- Destination protocols still reject structures they cannot represent without
  losing payload or semantics, including some native server tools, audio/images,
  encrypted cross-provider reasoning and multiple choices. Removing these checks
  requires a real conversion or explicitly protocol-compatible routing, not
  deletion of the data.
- Already-delivered streaming deltas cannot be retracted. A corrected Responses
  final snapshot updates the normalized log/replay view and native downstream
  snapshot; it does not append a complete replacement to old deltas.

## Regression coverage

The public 18-case conversion matrix remains exercised together with native
round-trips, citation zero offsets, final snapshot metadata, upstream error
preservation, SSE MIME variants, terminal-only completion, WS error placeholders,
replay and routing tests.

```powershell
$env:GOTOOLCHAIN = 'go1.25.0'
go test -mod=readonly ./... -count=1
go test -race -mod=readonly ./internal/protocol/... ./internal/transformer/... -count=1
go build -mod=readonly ./...
```

Protocol source attribution remains in each protocol directory's `SOURCES.md`.
This compatibility behavior is an Octopus adaptation, not a claim that every
provider implements the same API schema.
