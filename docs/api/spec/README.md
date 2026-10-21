# Zenith Team API contract

This directory is the canonical API documentation. Start with
[openapi.yaml](openapi.yaml) for HTTP operations and generated client types.
The server's served routes and domain code determine current behavior; correct
the contract and its guidance together when they disagree with code.

## Layout

| File | Owns |
| --- | --- |
| [openapi.yaml](openapi.yaml) | Index: metadata, authentication schemes, and references to domain paths |
| [_common.yaml](_common.yaml) | Shared schemas, parameters, and error responses |
| [authentication.yaml](authentication.yaml) | Sessions, OAuth, desktop handoff, and account lifecycle |
| [companies.yaml](companies.yaml) | Companies, members, search, resources, and bootstrap |
| [agents.yaml](agents.yaml) | Agents, the reporting tree, appearance, capabilities |
| [tasks.yaml](tasks.yaml) | Tasks, Company Plan, dependencies, and Task workspace |
| [conversations.yaml](conversations.yaml) | Conversations, messages, transcripts, queues, and attachments |
| [runs.yaml](runs.yaml) | Runs, Runtime reads, model discovery, results, review, and publication |
| [daemon.yaml](daemon.yaml) | Daemon registration, Runtime lifecycle, claim, preparation, and Run reporting |
| [connectors-and-plugins.yaml](connectors-and-plugins.yaml) | Provider discovery, Connector policy, and Plugin operations |
| [approvals.yaml](approvals.yaml) | Approvals, Questions, and human-input resolution |
| [inbox.yaml](inbox.yaml) | Notifications, read/archive state, and unread counts |
| [mailbox.yaml](mailbox.yaml) | Company Email, inbound messages, and Agent Mail drafts and delivery |
| [schedules.yaml](schedules.yaml) | Reviewed Schedule definitions and firing history |
| [workflows.yaml](workflows.yaml) | The read-only Workflow catalog: cards, forms, brief templates and playbook references |
| [activity-and-usage.yaml](activity-and-usage.yaml) | Activity, usage, and diagnostics |
| [health.yaml](health.yaml) | Health, readiness, and operator probes |
| [realtime.yaml](realtime.yaml) | Client WebSocket upgrade and typed event schemas |
| [redocly.yaml](redocly.yaml) | API lint and rendering configuration |

Shared guidance supplements the YAML without duplicating endpoint tables:

- [Conventions](../conventions.md): authentication, wire values, pagination,
  concurrency, mutation receipts, and composite reads.
- [Errors](../errors.md): emitted error codes and recovery policy, checked against
  the server registry by `TestErrorCodesMatchDocumentation`.
- [Realtime](../realtime.md): audiences, event handling, REST recovery, and the
  daemon and Desktop preview boundaries.
- [Workspaces](../workspaces.md): cloud versus daemon-local file and review
  contracts. Loopback routes are documented here, outside the cloud OpenAPI index.

## Maintaining the contract

Edit the owning domain YAML. Keep `openapi.yaml` as an index and put reusable
shapes in `_common.yaml`. Document constraints and examples beside the field
or operation they describe. Add error codes to [errors.md](../errors.md) with the
server registry change.

Feature behavior, design decisions, and wireframe handoffs live in
feature specs. Label future behavior **Planned** there.
Use `x-zenith-team-status: planned` only for a contracted operation that is not
served yet; remove it when the route ships. A proposed change to a served
operation does not turn its current contract into a planned route.

Keep one current contract per operation. Git history retains retired endpoint
guides and migration notes; new dated copies and parallel Markdown response
tables do not belong beside these files.

## Client integration

The web and Electron clients share `frontend/`. RTK Query owns server-state
caches through `fakeBaseQuery` and endpoint `queryFn`s; the axios client owns
HTTP transport. Generate server-owned types from this index, and converge
mutations from authoritative responses and typed receipts. Realtime recovery
follows [the shared recovery rules](../realtime.md#catch-up-after-a-disconnect).

Current client implementation and tooling are documented in
[frontend/README.md](../../../frontend/README.md); feature specs distinguish
shipped behavior from planned improvements.

## Commands and verification

Run from the repository root:

```bash
make api-docs        # Browse the full contract locally
make api-lint        # Validate domain files and references through the index
make api-types-check # Compare generated TypeScript with the committed output
make api-generate    # Refresh committed types after contract documentation changes
make api-bundle      # Optional flattened YAML for external consumers
make api-html        # Optional standalone HTML reference
```

`openapi.bundled.yaml` and `openapi.html` are generated, ignored outputs. Edit
the split source files. The committed generated TypeScript lives in
`frontend/src/shared/api/generated/api.generated.ts`.

The Go handler tests check route parity, request/response shapes, schema
reachability, envelopes, and error-code coverage. Run the relevant existing
checks when changing the contract; `make gate` is the repository merge gate.
