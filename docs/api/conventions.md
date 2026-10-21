---
title: Conventions
summary: The rules every Zenith Team endpoint and every realtime event follows
---

This is the contract every resource file conforms to. A rule stated here is not
repeated per endpoint; a resource file only records where it *deviates*, and a
deviation needs a reason written next to it.

`spec/openapi.yaml` is the machine-readable source of truth and the TypeScript
client is generated from it. Where this prose and the spec disagree, the spec
is right and this file is a bug.

The spec is split one file per domain under `spec/`, matching these prose file
names, with `spec/openapi.yaml` as the index — see
[overview.md](overview.md#how-the-spec-is-laid-out).

## Domain-owned wire projections

The domain that owns a noun owns its public response type and mapper. Run
views live in `internal/execution`, task views in `internal/task`, and so on;
there is no horizontal `internal/api` package. Handler-only request bodies may
remain private to `internal/handler`, but a reusable response projection does
not.

Client-plane realtime payloads fall into four categories:

| Category | Shape | Client behavior |
|---|---|---|
| Entity projection | The same public entity projection used by REST | Replace or upsert the exact cache entry when its revision is newer |
| Removal | The removed id and terminal revision where one exists | Remove the cache entry |
| Routed entity | Routing ids plus an embedded public entity | Route first; apply the embedded projection to its cache entry |
| Signal/control | A small typed payload such as progress or daemon roster invalidation | Apply the named behavior or perform its scoped read |

Do not force a signal or routed payload to equal an unrelated GET response.
Where an event embeds an entity, that embedded value uses the owning domain's
projection rather than a second event-only copy.

Disposable provider deltas are not client-plane events. Desktop may consume
them from the daemon's capability-authenticated loopback NDJSON stream; web and
other server clients receive only the completed `run.message_created`. Local preview is
rendering scratch, not a cache or recovery source.

Timestamps serialize as UTC RFC 3339 with nanosecond precision. Domain mappers
normalize `time.Time` values to UTC or use the wire-time formatting helpers in
`pkg/protocol`; there is no custom `protocol.Time` type.

## Base URL and versioning

Everything is under `/api/v1`. Local development serves
`http://localhost:8788`. Four paths sit outside the prefix: `GET /healthz`,
`GET /readyz`, `GET /metrics`, `GET /health/realtime`.

There is no second API version and no `Accept` negotiation. The contract
evolves additively: new fields are added with the absence rule below, and a
field being retired is marked `deprecated: true` in the spec with the condition
for its removal named in the description.

## Authentication

One credential per request.

| Credential | Presented as | Used by |
|---|---|---|
| Session cookie | `zenith_team_access` | The web app |
| Bearer token | `Authorization: Bearer <accessToken>` | The desktop app and the machine-side daemon |

The web and desktop transports are the same credential in different containers.
`POST /auth/login` returns a discriminated union — **branch on
`session.transport`**, because the token fields are absent in the cookie case.

There is no separate daemon credential. The daemon plane (`/api/v1/daemon/*`)
accepts the standard bearer token, from the `Authorization` header only —
never a cookie — authenticated by the same service as every other route. The
spec names that header-only rule `daemonToken`.

CSRF applies **only when an auth cookie is present**; bearer callers —
desktop and daemon alike — never encounter it. Unsafe methods require
`X-CSRF-Token` matching the `zenith_team_csrf` cookie; `/auth/refresh` and
`/auth/logout` validate against `zenith_team_refresh_csrf`.

### Runs

A Run of an Agent calls the API with its member's session plus the Run's
proof, `X-Zenith-Team-Run-Token`. The proof is not a second credential: it
narrows what the session may do. Every route says whether it accepts a Run:

| Route says | With a Run proof |
|---|---|
| `person` (and every route that says nothing) | `403 human_actor_required`, before the session is checked |
| `run` | The handler resolves the proof: it must be a running Run whose member is the caller (`401 run_token_invalid`), and the route reads or writes only what that Run may (`403 task_outside_run_scope`) |
| `any` | Admitted without reading the proof. Only Company metadata reads: the Company list, one Company, the Agent list, one Agent |

Deciding (approve, reject, cancel, answer, withdraw), Task edit and start,
the dependency graph, Project create and every access change are `person`.
A request without the proof is never touched by this rule.

## Company scoping

Every product entity belongs to a company. Company-scoped routes carry
`:companyId` and sit behind one of two gates: **member** (any active member) or
**owner** (the company owner).

**Cross-company access returns `404`, never `403`.** A caller who is not a
member cannot distinguish "does not exist" from "exists but is not yours". The
daemon plane follows the same rule.

## Requests

Bodies are `application/json`. The decoder is strict: unknown fields are
rejected, the root must be an object, and the body is size-capped. A non-JSON
content type on a JSON endpoint is `415`.

**Unknown, repeated, or empty-valued query parameters are `422`.** Adding a
parameter a client sends but the server does not know is a breaking change for
that client, not a no-op. This applies to every endpoint that accepts a query
string, without exception.

**A multi-valued filter is one comma-separated value in a single parameter.**
That is the mechanism, everywhere — `status=queued,running`, not
`status=queued&status=running`. Repeating the parameter is the `422` above,
not an alternative encoding, so there is exactly one way to send a set.
Parameter names themselves stay snake_case, per the response-casing rule
below.

## Responses

Success bodies are JSON objects. **Every field Zenith Team serializes is
camelCase.** Two things are not: `pkg/db/generated/**` (sqlc models, which
mirror snake_case columns and never cross the wire) and `pkg/agent/**` (vendor
CLI formats whose field names belong to those vendors). URL query parameters
are also outside the rule and stay snake_case.

### A response is the thing itself

**A response about one entity returns that entity at the top level.** There is
no wrapper key named after the resource — no `{"company": {…}}`, no
`{"user": {…}}`, no `{"run": {…}}`.

A wrapper costs every caller an unwrapping step and invites duplicated derived
state. `POST /companies` therefore returns the company itself; onboarding and
statistics remain separately addressable resources.

Three shapes, and only three:

| Response is about | Shape |
|---|---|
| One entity | The entity, flat |
| One entity plus derived data the caller needs | The entity flat, with the extra as a **sibling key** |
| A whole screen — several co-equal things | One named key per thing |

So marking an inbox item read returns the item's own fields with `counts`
beside them, and starting a task's execution returns the updated task flat
with the `runId` it enqueued as a sibling. A screen composite like
`GET …/tasks/{taskId}/workspace` names each part, because no one of them is
"the" resource.

No-result shapes are endpoint-specific. Mutations with no receipt use `204`;
the daemon's batch claim is a read whose stable success shape is
`{"items": []}` when there is nothing to claim, so callers do not branch on an
HTTP status before decoding the claim envelope.

### Created is a status code

Whether a write created something is signalled one way: **`201` for a
creation, `200` for a replay or an already-existing result**. There are no
`{entity, created}` wrappers and no `created` flags in bodies — a second
mechanism would let the two disagree, and every caller already reads the
status. The one place a body still distinguishes outcomes is a genuine
multi-branch result (like a message send's `disposition`), where the branches
carry different things, not a boolean about one thing.

### What earns a field

A response field is **deleted** when it is derivable from its siblings,
duplicates another field, is constant, or is dead by design — nothing writes
it, or nothing could ever read it. A field **stays**, even currently unread,
when it is the wire's only carrier of a fact (`hasPassword`, `decidedBy`,
`lastSeenAt`, a run's `error`): unique facts are contract, convenience copies
are cost. When a screen needs a join the client can make from collections it
already caches, the row carries the id and the client joins.

### Counting runs

One run-counting vocabulary, everywhere a run counter is served:

| Counter | Counts |
|---|---|
| `queuedCount` | `queued` + `deferred` — not yet claimed by a machine |
| `activeCount` | `dispatched` + `waiting_local_directory` + `running` |

The two are disjoint and terminal runs are in neither. They are the only names
for counting a collection of runs as a whole.

One counter sits outside that pair, on purpose: **`activeRunCount`** on the
Agent projection. It uses the same three statuses as `activeCount` but counts a
narrower set — only the agent's own task-lane runs, because it is the numerator
of the agent's `maxConcurrentRuns` capacity, and direct-conversation runs are
exempt from that cap. It is deliberately not called `activeCount`: a client that
read it as "this agent's active runs" would be wrong every time the founder is
chatting with a busy agent. No further run counter name is part of the
contract; a new counting question gets one of these two names or an argument
for why it counts something neither describes.

### Absent ≠ null ≠ empty

Entity responses and lifecycle events are complete projections: replace their
documented fields rather than interpreting omission as a field-level patch.
For every other typed receipt, signal, or nested optional value, the schema
defines absence explicitly.

| Value | Meaning |
|---|---|
| Field **absent** | The schema says this value is optional and not present; it never means a generic `changes` patch |
| Field is **`null`** | Explicitly no value |
| Array is **`[]`** | Authoritatively empty |

**Spec-nullable means Go serializes the `null`.** A field the spec declares
nullable is never `omitempty` in its Go projection — absence and `null` are
different statements, and a projection that collapses them lies to the
schema. The response contract test enforces this in both directions: a field
the spec requires must appear, and a spec-nullable field must not be
omittable. A field using `omitempty` must say what absence means in its own
schema or resource page.

### Pagination

Pagination is selective. Bounded entity collections — companies, members,
agents, tasks, runtimes, resources, and direct
conversations — return the complete matching set in one response. They do not
accept `limit` or `cursor`. Their established collection wrapper remains part
of each endpoint's contract, and an empty array is authoritative.

Only high-volume chronological feeds use seek pagination: activity, Inbox,
runs, approvals, and questions. Those endpoints return:

```json
{ "items": [ … ], "nextCursor": null }
```

`nextCursor` is `null` on the last page. Unless an endpoint says otherwise,
`limit` defaults to 50 and is bounded 1..100.

A cursor is **opaque**: an unpadded base64url encoding of the versioned,
fixed-width 25-byte record carrying the immutable `(createdAt, id)` sort tuple
(one version byte, eight timestamp bytes, and the 16-byte UUID).

Clients round-trip it unchanged and never construct one. A malformed or
unsupported cursor is `422 invalid_cursor`; it is never folded into
`validation_failed`.

The UUID is a required tie-breaker: a timestamp alone can skip or duplicate
rows created at the same instant. Oldest-first feeds seek with
`(created_at, id) > (cursor_created_at, cursor_id)`. Newest-first feeds seek
with `<`; using `>` there would fetch newer rows again instead of the next
older page. Offset pagination and mutable `updated_at` sort keys are not part
of this API.

Search is a separate bounded top-results operation. It accepts `limit` as a
cap and has no cursor or continuation contract.

### Sequenced resources

A conversation is one ordered stream, numbered by a monotonic `seq`, and its
sequenced reads page by position rather than by a cursor — because a client
needs to ask "what did I miss" by position and then prove it caught up.

| Parameter | Selects |
|---|---|
| `after_seq` | rows with `seq > after_seq` — the reconnect path |
| `through_seq` | bounds the window above, so a page is reproducible |
| `limit` | how many, newest-bounded |

The read returns `throughSeq` — the highest `seq` in the conversation — so a
client compares it against what it holds and knows whether one page was enough.
`earliestAvailableSeq` says how far back the stream still goes, and
`resetRequired` says the requested window is no longer reachable, so the client
should re-read rather than merge.

`seq` is allocated as `max(seq) + 1` over committed rows, under the
conversation row lock every write path already takes — a user send, a queue
promotion, a transcript append. **Only the holder of that lock may advance the
sequence**, which is what makes it gap-free over the stream: a rolled-back write
leaves no hole, because nothing was committed to count. An allocator that hands
out numbers before commit (a counter column, a sequence) would break that on the
first aborted transaction.

**A sequenced read is a filtered view of that one stream, so its own numbers
skip.** The chat surface returns the two message types and leaves provider
detail between them unreturned; a Conversation transcript returns provider
items across all Runs and leaves user messages out. Both are ascending and
never repeat, neither is dense, and a skipped number is not a missing row. Page
with the last returned row's `seq` and judge completeness from the window's
bounds, never by incrementing.

Conversation transcripts take `after_seq` exactly the same way and are numbered
by the same counter, so a transcript row and a chat message can always be
ordered against each other. Every returned row carries `runId`; clients use one
Conversation cursor and group rows rather than scanning the same pages once per
Run. The daemon still submits one immutable completed item at a time over
`runs.transcript.append` with no `seq`; the server allocates it under the
Conversation lock and returns it in the receipt. `(run_id, item_key)` is the
natural replay identity: equal normalized content returns the stored item and
different content conflicts. This is not a generic idempotency-key convention,
and `seq` is not a global event sequence — it is local to one Conversation.

### Concurrency

Mutable resources carry `revision` (an `int64`). Mutations that must not
clobber a concurrent edit take **`expectedRevision`** in the request body and
answer `409` on mismatch. The field has exactly that name everywhere.

A few conflict responses deliberately extend the error envelope with the
current server state so a client can resolve without a refetch: `PATCH
/me/profile` adds `currentUser`, `PATCH /companies/{companyId}/profile` adds
`currentCompany`, and `PATCH /companies/{companyId}/tasks/{taskId}` adds
`runLive` to `task_handoff_confirmation_required` alone. The full list is in
[errors.md](errors.md#conflicts-that-carry-state).

### Retry fences

Three fences make a retried request safe, and each has a fixed home:

- **`requestKey`** — a caller-generated UUID in the request body, scoped to
  the actor and operation. Used where the client mints a new entity and a
  replay must not mint a second one: message send, task message send, and
  attachment initiate. Exact replay returns the stored result; the same key
  with a different payload is that resource's `409`.
- **Natural unique keys** — where the entity already has an identity, that
  identity is the fence and no token is added: tasks by
  `(companyId, key)`, direct conversations by their creator and agent, and
  run transcript items by `(runId, itemKey)`.
- **`expectedRevision`** — optimistic concurrency for updates to an existing
  entity (above). It does not deduplicate creates; it stops a retried or
  concurrent update from clobbering a newer revision.

The one header-borne fence is the agent plane's `Idempotency-Key`, required
wherever a request carries the live-run proof `X-Zenith-Team-Run-Token`: the
approval-request branch of company-plan apply, and Question ask. It is a
header because those request bodies are the domain artifact itself, which the
server compares on replay; a token inside the body would perturb the very
payload the replay check compares. Client-plane operations do not use the
header form.

### Mutation receipts

A mutation returns its authoritative post-write entity or a documented typed
receipt when the operation creates or coordinates more than one entity. The
caller does not issue a confirming GET after a successful response.

Realtime is post-commit. An entity event carries the safe current projection;
a routed event may add routing ids around that projection. Signals name only
the behavior they accelerate. Change facts used by server listeners remain
process-local. No generic `mode`, `snapshot`, `refetch`, or `changes` wrapper
is part of the wire envelope.

### Actions and sub-resources

- **Actions are `POST /resource/{id}/verb`** — `/archive`, `/restore`,
  `/cancel` — never a magic field on a `PATCH`.
- A sub-resource mutation returns the documented updated entity, collection,
  or typed receipt; its event category is documented separately.

### Values

| Kind | Rule |
|---|---|
| Timestamps | RFC 3339, nanosecond precision, always UTC |
| Calendar days | Date-only `YYYY-MM-DD`. Never an instant — a due date has no time zone |
| Big integers | Serialized as **strings** when they can exceed 2^53 (token counts, usage totals), because JavaScript consumes them |
| Money | Integer minor units, never floats. A zero is a real answer and stays distinguishable from absent |
| Enums | lower `snake_case` string values, validated against the database CHECK constraint that backs them. An invalid value is `422` and the message lists the allowed set |
| Ids | UUID strings. Version-agnostic — never assume UUIDv7 ordering |

## Errors

One envelope, everywhere:

```json
{
  "detail": "human-readable explanation",
  "code": "snake_case_code",
  "requestId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c"
}
```

`requestId` echoes `X-Request-ID` and is `null` only if the middleware chain
was bypassed. It appears in the server log line for the same request.

**Branch on `code`, never on `detail`.** `detail` is prose and may change or be
localized; `code` is the contract. The full vocabulary, the status each maps
to, and what a client should do about it are in [errors.md](errors.md).

The 400-versus-422 split: **malformed** input is `400`, **semantically
invalid** input is `422`.

**A malformed path id is `404`, never `400` or `422`.** An id that cannot
name a row is indistinguishable, to the caller, from one that names nothing —
and answering differently would disclose which failures are structural. This
is the same reasoning as the cross-company `404`.

## The daemon plane

The daemon plane (`/api/v1/daemon/*`) follows every rule in this document —
it is the same API with a different caller, not a second dialect. In
particular: list responses use the same `{"items": […]}` wrapper, timestamps
are the same RFC 3339 UTC values, and receipts are honest — an endpoint
answers `{"status": "ok"}` only when the write actually happened, and a
failure surfaces as its error, never as a swallowed success. Authorization
denial is `404`, exactly as on the user plane.

## Realtime

Both WebSocket planes, the event catalogue, Desktop-local preview separation,
and reconnect behaviour are in [realtime.md](realtime.md). Entity events reuse
the owning public projection; routed payloads add explicit routing context and
signals remain named typed hints.

## Round trips

The contract states a **call budget per screen** and provides named composite
endpoints to meet it. Composites are hand-written and separately testable;
there is deliberately no generic `?expand=` or `?fields=` grammar, because a
generic expansion grammar is a combinatorial surface that has to be secured and
paginated per branch.

Each composite documents its consumers in a `## Use Cases` section, and its
measured before/after request count. A composite without a measured win does
not belong in the contract.

### Composites

Four rules, and the first is the one that is easy to get wrong.

**A composite must not carry an entity another composite carries on the same
paint.** The cost is not the duplicated bytes, it is the coupling: one entity
in two responses means a single entity event has to patch two cache slots or
leave one stale, which defeats the rule this whole document hangs from. When
two screens genuinely need the same entity, the wider-scoped composite carries
it and the narrower one carries the id.

**An aggregate carries scalars and ids; entities live on their own endpoints.**
A counter, a total or a bucketed series has no cache slot of its own and cannot
go stale against an event, so it is safe to compose freely. An entity does have
one, so it belongs in exactly one place.

**Every capped collection inside a composite reports its own cap**, either as a
`caps` object naming each limit or a `truncated…` flag per dimension. A client
that cannot tell "there are three" from "you were given three of nine" will
render the wrong thing and never know. This is why composites embed bounded
slices rather than whole collections: an unbounded embed trades an N+1 for one
enormous response.

**A composite is scoped to what is read together, not to a resource.** The
session composite is read once when a company is opened; a screen composite is
read when that screen opens. Splitting one paint across two composites, or
folding two paints into one, both cost more than they save.

## Client integration

The web and Electron renderers share one client under `frontend/`. It speaks
only the current API: it keeps no old route, event or payload shape, with the
one exception at the end of this section.

- **One HTTP transport.** The axios client in `frontend/src/lib/api-client.ts`
  makes every `/api/v1` call. It owns credentials, CSRF, session refresh and
  error handling. RTK Query uses `fakeBaseQuery` with a `queryFn` per
  endpoint, so RTK owns cache lifetimes and axios owns the wire.
- **Retries.** Only idempotent `GET` and `HEAD` requests are retried, once,
  after a network failure or a `5xx`. A `401` refreshes the session and
  replays the request once; no other `4xx` is retried. A network failure has
  no status, so it never looks like a server `404`, and a server error keeps
  its `{detail, code, requestId}` body.
- **Mutations converge from their response.** The client applies the
  entity or typed receipt a mutation returns. It sends no confirming read.
- **Generated types.** Server-owned types are generated from
  `docs/api/spec/openapi.yaml` into
  `frontend/src/shared/api/generated/api.generated.ts` by `make api-generate`.
  `make api-types-check` regenerates into a temporary file and fails on drift.
- **One socket.** The authenticated shell opens one realtime socket,
  `GET /api/v1/ws?company_id={companyId}`, and drops repeated frames by
  `eventId`. After a reconnect it catches up over REST, as
  [realtime.md](realtime.md#catch-up-after-a-disconnect) describes.
- **Agent replies arrive as `run.message_created`.** There is no
  `message.created` for agent text; see
  [realtime.md](realtime.md#there-is-no-messagecreated-for-agent-text).
- **Desktop preview stays local.** Electron may render the daemon's loopback
  preview ([realtime.md](realtime.md#local-desktop-preview)). That scratch
  never enters the RTK Query cache and never reaches the server, so it is
  never in Redis or PostgreSQL. The matching `run.message_created` replaces
  it.

The one exception is in the Electron main process. Its `/auth/providers`
parser, in `frontend/electron/main/desktop-auth-api.cjs`, also accepts an
older response shape (a single `enabled` flag under `providers.google`), so a
newer desktop app can still talk to an older server. Callers always receive
the current `items[]` shape.
