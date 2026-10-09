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

P0 audit completed. P1-P6 have not been implemented or validated. No capability or migration completion is claimed by this document until the corresponding results are recorded.

`go test -mod=readonly -count=1 ./internal/transformer/... ./internal/relay/...` passed (relay 27.438 seconds). The baseline had no benchmarks in those packages; a fixed public request benchmark was added before modifying production code.

Baseline (Go 1.26.3, windows/amd64, Intel i7-14700F; single run, not a statistical comparison):

| Request round trip | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| Chat | 9483 | 4615 | 44 |
| Responses | 24001 | 12466 | 165 |
| Anthropic | 256196 | 177331 | 2381 |

`python scripts/audit-retired-protocols.py data/data.db` found no retired channels, no groups referencing retired channels and no groups lacking a configured enabled supported channel with an enabled nonempty key. This is a local configuration snapshot, not a deployment-wide health assessment. The script uses SQLite read-only mode, a read transaction and query-only pragma, and never selects secret key values for output. Run it against each deployment before upgrading. MySQL/PostgreSQL deployments require an equivalent read-only query/export; this SQLite tool does not inspect them.

The pinned AxonHub archive was downloaded from the SHA-addressed GitHub codeload endpoint. Its NOTICE assigns `llm/` to LGPL-3.0, subject to per-file overrides. The old Octopus transformer MIT attribution must remain separate from newly adapted LGPL sources.

### Rollback

Before deployment, back up database and configuration. Revert migration commits in reverse stage order or deploy the baseline image. Do not renumber, delete or rewrite persisted channel types. Preserve the user-owned task specification and unrelated changes during rollback.
