---
title: Client integration status
summary: The current frontend-to-API integration, its convergence rules, and composite adoption
---

The web and Electron renderers share the client under `frontend/`. This page is
the post-migration integration record, not a compatibility inventory. Current
server routes, generated contract types, and the receive-only WebSocket are the
only supported boundary; the client carries no alternate route or event
protocol for an earlier API.

One narrowly-scoped, dated exception stands (2026-08-21): the Electron main
process's `/auth/providers` parser in
`frontend/electron/main/desktop-auth-api.cjs` also accepts the previous
response shape (a single `enabled` flag under `providers.google`), because a
packaged desktop build can run ahead of a lagging deployed API during a
rollout. Callers still receive only the current `items[]`-derived shape.
Remove the legacy branch once every deployed API serves the `items[]` shape.

## HTTP and generated contracts

The axios client in `frontend/src/lib/api-client.ts` is the only HTTP
transport. RTK Query uses `fakeBaseQuery` and endpoint-owned `queryFn`s, so RTK
owns cache lifecycles while axios owns credentials, CSRF, refresh, and error
normalization.

- A network failure remains distinct from a server `404`; resource and route
  errors keep the server's `{detail, code, requestId}` body.
- Only idempotent `GET` and `HEAD` requests are retried, once, after a network
  failure or `5xx`. A `4xx` is never retried by the transport.
- Mutations converge from their authoritative response or typed receipt. They
  do not issue a confirming read as part of the success path.
- Server-owned types are generated from `docs/api/spec/openapi.yaml` into
  `frontend/src/shared/api/generated/api.generated.ts`. Integrated domain
  aliases in `shared/api/types.ts` point at those generated schemas and
  operations; UI-only read models remain client-owned.

`make api-types-check` regenerates into a temporary file and fails on drift.
The route-table and error-code checks remain in the Go gate, so a route, error,
or generated type cannot move silently.

## Realtime convergence

The authenticated shell owns one socket:

```text
GET /api/v1/ws?company_id={companyId}
```

Authentication rides the upgrade. The browser sends no initialization,
subscription, acknowledgement, or application ping frame. Each server frame
has exactly six fields:

```json
{
  "eventId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "type": "task.updated",
  "createdAt": "2026-08-19T09:41:02.481113Z",
  "actorId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "actorType": "user",
  "payload": {}
}
```

The transport deduplicates by `eventId` with a bounded recent-id set. Typed
company, user, Company-user, and Company-or-user audiences are server-only
routing facts; direct-conversation and personal data never rely on a
browser-supplied scope.

`RealtimeSyncController`, mounted once in the authenticated shell, applies
version-aware entity projections to every active RTK cache that contains the
entity. A small signal requests only the joined or filtered projection it can
invalidate. Reconnect runs `convergeRealtimeCaches`, catches open sequenced
resources up over REST, and discards non-authoritative stream scratch. Feature
screens do not add focus refetch loops, interval polling, or duplicate sockets
as a second consistency mechanism.

The cloud socket carries no token-delta event. `run.message_created` delivers one full
post-commit transcript item, including its PostgreSQL-assigned `seq`, natural
`itemKey`, structured parts, item digest, and current Run transcript head. It
is broadcast to every eligible connected client; the owning Conversation's
single transcript REST endpoint is the reconnect path. Live partial output stays on the bounded same-machine
Desktop preview lane and never creates a cloud event or recovery obligation.

**`run.message_created` is also how the agent's chat reply arrives.** There is no
`message.created` for agent text — that event is the user path only — and
nothing further follows `run.completed`. A chat panel appends agent text items
from `run.message_created` as they commit; a client that waits for a completion frame to
supply the reply renders an empty turn. See
[realtime.md](realtime.md#there-is-no-messagecreated-for-agent-text).

Electron may also consume the daemon's capability-authenticated loopback NDJSON
preview. That Desktop-only scratch is keyed by `(runId, itemKey, channel)`, may
be coalesced or dropped, never enters RTK Query/Redis/PostgreSQL, and is sealed
by the matching `run.message_created`. Browser clients do not receive it.

Lifecycle snapshots carry `transcriptThroughSeq`. On a terminal event, a
client that already holds that exact prefix performs no transcript request; a
client behind the advertised sequence catches up through the Conversation's
shared `after_seq` cursor and groups rows by `runId`. The Run's exact
`transcriptThroughSeq` remains the terminal proof. This removes the
unconditional terminal round trip without trusting WebSocket delivery as durable.

## Current product surfaces

The integrated client uses the current resource vocabulary and response
shapes:

- direct and task conversations as **one ordered item stream** — the chat
  surface and Conversation transcript are two filtered reads of the same `seq`
  and provider rows carry `runId` — plus
  the run queue, Run lifecycle, and durable transcript catch-up;
- agents bound to registered provider runtimes, plus transient per-runtime
  model discovery for agent setup;
- tasks directly, including dependencies and rich Task projections;
- Company resources, Inbox items and counts, activity, usage, profiles,
  onboarding, and authentication; and
- durable Approvals and Questions with revision-guarded resolution, Inbox
  notification, realtime snapshots, expiry, and a new continuation Run.

There is no human-waiting Run state. The Run that raised an Approval or
Question ends; the durable terminal decision later produces a separate Run
carrying the outcome back to the agent.

Provider output visible through the cloud client is a sequence of immutable
items. Each item has a role and one or more `text`, `reasoning`, `toolCall`,
`toolResult`, or `error` parts. Electron may additionally render daemon-local
`run.preview.delta` frames on the `text` and `reasoning` channels, mapping
reasoning to the UI's thinking treatment; `run.preview.reset` clears that
scratch wholesale. Vendor transport frames and daemon-local paths never cross
the cloud client interface.

## Composite endpoint adoption

The server exposes the settled screen composites and keeps each one as a named,
typed operation:

| Composite | Purpose |
|---|---|
| `GET /companies/{companyId}/bootstrap` | Company shell, root agent/conversation, and counters |
| `GET /companies/{companyId}/tasks/{taskId}/workspace` | Task, conversation window, run summary/queue, and dependency context |
| `GET /companies/{companyId}/conversations/{conversationId}` | Direct conversation window, user state, answering agent, and run state |

The frontend now uses both measured composites without creating a parallel
composite-owned state store:

- An owner session whose active Company snapshot carries `role: "owner"` loads
  the cockpit through exactly one `bootstrap` request. Admin/member sessions,
  and a restored older session with no trusted role field, keep the existing
  member-safe endpoint fan-out because `bootstrap` needs `agents.manage`.
- Opening a Task chat loads `workspace` once and seeds the Task, Conversation,
  initial message window, bounded Run timelines, active-Run resume state,
  queue, and dependencies into their existing client authorities. Direct chat
  keeps its Conversation read. Older message pagination and reconnect catch-up
  remain incremental REST reads rather than reloading the whole workspace.

Those counts describe the composite-owned hydration only. Independently
mounted shell queries still read Company, Inbox
counts, and the full Agent roster. A Task chat may also read the company-wide
pending Approval and Question collections when those RTK caches are cold, and
a deep link to one focused Message intentionally reads its context. None of
those distinct authorities belongs in either composite.

## Client-only and absent surfaces

Generated files and attachments are the durable output surfaces. Prototype
Company-memory and sandbox-artifact read models do not name server operations
and must not be mistaken for synchronized product state.

The Desktop Assets screen follows that rule: it reads bounded,
capability-authenticated projections from the local daemon through Electron's
bridge. Those daemon-local routes are not cloud server operations, so they do
not belong in the OpenAPI document or generated cloud client types. The retired
Memory browser and its Electron bridge have been removed; Agent Company Memory
remains owned by the daemon workspace.

## Verification

Run the contract checks before the client lanes so a frontend failure is not
debugged against a stale generated boundary:

```bash
make api-lint
make api-types-check
make go-gate
make lint
make typecheck
make test
make build
```

`make gate` runs those release-quality lanes together. Browser E2E and Electron
packaging remain explicit checks documented in
[`frontend/README.md`](../../frontend/README.md); they are not hidden inside
the unit/build gate.
