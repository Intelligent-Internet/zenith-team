---
title: API Overview
summary: What the Zenith Team API is, how the contract is enforced, and where to find each part
---

Zenith Team exposes one HTTP API and two WebSocket planes. This page orients; the
rules live in [conventions.md](conventions.md) and are not repeated per
endpoint.

`spec/openapi.yaml` is the machine-readable source of truth. The TypeScript client
types are generated from it, and tests diff it against the server. When prose
and the spec disagree, fix the prose; when either disagrees with served code,
**the code wins** and both documents must be corrected. These docs label
future behavior Planned instead of presenting it as already served.

```bash
make api-docs     # browse the contract locally
make api-lint     # validate it (also runs in CI)
make api-bundle   # flatten to one file for consumers that want one
make api-generate    # update the committed TypeScript contract
make api-types-check # verify generation is reproducible and current
```

The accepted desktop screens and their implemented contracts are mapped in
[Desktop design support](design-support.md), including provider-dependent limits.

[runs.md](runs.md) and [activity-and-usage.md](activity-and-usage.md) describe
UUID Run IDs, source revisions, Agent Run history and the Company Activity log.

## How the spec is laid out

At 8,000 lines a single spec file stops being reviewable — a diff touching
three resources is unreadable, and two people editing different domains
conflict. So the spec is split the way the prose is, one file per domain with
matching names:

```
docs/api/
  *.md                  the prose — one file per resource, plus the rules
  spec/
    openapi.yaml        the index: metadata, security schemes, one $ref per path
    redocly.yaml        lint configuration
    _common.yaml        reusable parameters, standard error responses,
                        and every schema more than one domain shares
    companies.yaml      ← companies.md          agents.yaml   ← agents.md
    conversations.yaml  ← conversations.md      runs.yaml     ← runs.md
    tasks.yaml                        inbox.yaml
    authentication.yaml                         approvals.yaml
    activity-and-usage.yaml                     daemon.yaml
    schedules.yaml       ← schedules.md
    mailbox.yaml         ← mailbox.md
    connectors-and-plugins.yaml ← connectors-and-plugins.md
    health.yaml         the probes outside /api/v1
    realtime.yaml       the WebSocket upgrade
```

Everything machine-readable lives in `spec/`, including the generated
`openapi.html` and `openapi.bundled.yaml`, so `docs/api/` itself is the prose
you read and `docs/api/spec/` is what tooling consumes.

**Edit the domain files.** `spec/openapi.yaml` is an index — its `paths` entries are
`$ref`s and carry no operation bodies. A domain file holds that domain's path
items and the schemas only it uses; anything a second domain needs moves to
`_common.yaml`, which is what keeps ownership honest.

Tooling follows the `$ref`s natively: `make api-lint` validates every domain
file transitively, `make api-docs` renders the whole thing, and
`openapi-typescript` generates from the index. `make api-bundle` produces a
single flattened `spec/openapi.bundled.yaml` for anything that cannot — it is
generated, never edited, and never committed.

The complete generated contract lives at
`frontend/src/shared/api/generated/api.generated.ts`. It includes every user,
operator, WebSocket-upgrade, and daemon operation; handwritten client bridges
choose which operations they expose. Both contract tools are exact development
dependencies in the frontend lockfile, so local and CI output is identical.

## How the contract stays true

There is deliberately **no Swagger annotation layer**. The spec is written
first and the code is checked against it, not generated from it. Four gates
enforce that:

| Gate | Fails when |
|---|---|
| `TestOpenAPISpecMatchesRouteTable` (in `make go-gate`) | A served route has no contract entry, or a contract entry has no route |
| The error-code diff, in the same test | The spec names a code the server cannot emit, or the server emits a code the spec does not name |
| `make api-lint` | The spec is malformed |
| `make api-types-check` | The committed TypeScript contract differs from a clean regeneration |

An operation contracted **before** it is served carries
`x-zenith-team-status: planned`. The gate allows it to have no route and reports it
as awaiting implementation, so the backlog stays visible instead of silent. The
marker is removed in the commit that serves the route; an operation that stays
`planned` indefinitely is drift wearing a nicer name.

## Base URL

Everything is under `/api/v1`. Local development serves
`http://localhost:8788`. Four paths sit outside the prefix: `GET /healthz`
(liveness), `GET /readyz` (readiness), `GET /metrics` (Prometheus), and
`GET /health/realtime`.

## The shape of the API in one screen

- **Three credentials**, one per request: session cookie (web), bearer token
  (desktop), daemon bearer (`/api/v1/daemon/*`).
- **Company-scoped**: every product entity belongs to a company; cross-company
  access is `404`, never `403`.
- **camelCase** on everything Zenith Team serializes.
- **Selective pagination** — bounded entity collections return all matching
  rows; high-volume feeds use `{items, nextCursor}` seek pages keyed by
  `(createdAt, id)`; conversation streams advance by `seq`, and every sequenced
  read is a filtered view of one conversation-wide counter, so its numbers skip.
- **One error envelope** — `{detail, code, requestId}`. Branch on `code`.
- **`revision` / `expectedRevision`** for optimistic concurrency.
- **Mutations return authoritative state or a typed receipt**, so a confirming
  `GET` is not part of the success path.
- **Entity events reuse the owning public projection**; routed payloads add
  explicit routing context and signals remain small typed hints.

Each of these is specified in [conventions.md](conventions.md).

## Round trips are part of the contract

Screens have a stated **call budget**, and named composite endpoints exist to
meet it. A composite is documented with the consumers it serves and its
measured before/after request count; there is no generic `?expand=` grammar.

Two composites read on the same paint must not both carry the same entity — the
duplication couples one entity to two cache slots, which is what
[conventions.md](conventions.md#composites) forbids.

The budgets are recorded per resource, in each file's call budget section. How
the frontend client consumes the API is in
[conventions.md](conventions.md#client-integration).

## Where things are

| File | Covers |
|---|---|
| [conventions.md](conventions.md) | The rules every endpoint follows — read this first |
| [errors.md](errors.md) | The full error-code vocabulary and what to do about each |
| [realtime.md](realtime.md) | Both WebSocket planes, the event catalogue, and reconnect |
| [authentication.md](authentication.md) | Signup, login, refresh, OAuth, desktop handoff, account |
| [companies.md](companies.md) | Companies, members, profile, search, resources |
| [agents.md](agents.md) | Agents, the reporting tree, appearance, starter agents |
| [tasks.md](tasks.md) | Company plan, tasks, dependencies |
| [projects.md](projects.md) | Projects, per-person grants, access report |
| [groups.md](groups.md) | Groups of members that receive Project grants together |
| [conversations.md](conversations.md) | Task/direct privacy, messages, transcript recovery, attachments, run queue |
| [runs.md](runs.md) | Runs, transcript heads, runtimes, input snapshot boundaries |
| [workspaces.md](workspaces.md) | Server-side Task workspace versus local files, Resource references, execution environments, and the planned managed-only boundary |
| [approvals.md](approvals.md) | Human-in-the-loop approvals and mid-run questions |
| [inbox.md](inbox.md) | Inbox items and counts |
| [activity-and-usage.md](activity-and-usage.md) | Activity feed, usage reports |
| [schedules.md](schedules.md) | One-shot and recurring scheduled work |
| [mailbox.md](mailbox.md) | Company Email and Agent Mail drafts |
| [wiki.md](wiki.md) | The Company Wiki and Agent access to it |
| [connectors-and-plugins.md](connectors-and-plugins.md) | Company policy over provider Connectors and Plugins |
| [design-support.md](design-support.md) | Desktop screens mapped to their API contracts |
| [daemon.md](daemon.md) | The daemon plane — not for client use |
