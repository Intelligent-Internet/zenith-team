# `server/pkg`

The shared contract layer: what the cloud server and the machine-side
binaries (`zen`, `boardly-mcp`) must agree on. The layout follows the
reference architecture, which is also why the data layer lives here rather
than in `internal/`.

| Package | What it is |
| --- | --- |
| `protocol` | the wire vocabulary shared by every transport and binary: the flat event registry (`<domain>.<verb>`), the Message envelope and daemon RPC frame payloads, the activity entity/actor vocabulary, the provider list, and wire-side value contracts (phone, text, timestamps). No DTOs, no general-purpose helpers — parsing and pgtype boundary utilities live in `internal/util` |
| `db` | sqlc: `queries/` (per-domain `.sql` files) and `generated/` (package `db`, pgtype end-to-end). `sqlc.yaml` names both directories, so the paths are load-bearing |
| `agent` | the provider-neutral agent contract: `agent.Provider`, its ordered delta/tool-activity/completed-item/session/finished event stream, and `agent.New(provider, cfg)` as the one factory switch. Tool activity is ephemeral watchdog metadata, never transcript content or a broadcast event. Per-provider adapters are sibling files (`claude_provider*.go`, `codex_provider*.go`), not subpackages |
| `runfailure` | the canonical `runs.failure_reason` taxonomy: classification, context-exhaustion detection, resume rules |
| `redact` | log/wire redaction helpers |

Two rules, both held by convention and review (there are no depguard rules):

- **`pkg/**` does not import `internal/**`.** Currently true tree-wide; keep
  it that way — the daemon-side binaries depend on these packages staying
  free of server wiring.
- **Provider adapters stay pure.** `pkg/agent` imports no HTTP stack, no
  PostgreSQL, and no Boardly business domain; it speaks only the provider
  CLIs and its own execution-event contract.

Layout and the full map live in [`CODEMAP.md`](../CODEMAP.md).
