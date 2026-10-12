---
title: Agent Run history contract
summary: Consumer contract for UUID Runs, source revisions and Company Activity
---

Status: implemented on `feat/run-tracking-history`, 2026-09-13, based on
`main` `5dd64ebd0`. The served [OpenAPI index](spec/openapi.yaml) references
the owning domain schemas; the frontend types are generated from that index.
The UUID migration requires the coordinated [cutover procedure](run-history-cutover.md).

## Reads and actions

All paths below are relative to `/api/v1`. Cookie or bearer authentication
and active Company membership are required; cancellation also requires CSRF
for cookie callers.

| Operation | Consumer purpose | Response |
|---|---|---|
| `GET /companies/{companyId}/runs?agent_id={agentId}` | Agent's paged execution history; reuse the existing list route | `RunPage` |
| `GET /companies/{companyId}/runs/{runId}` | Exact Run detail and authoritative lifecycle recovery | `Run` |
| `POST /companies/{companyId}/runs/{runId}/cancel` | Idempotent cancellation by UUID, with no `expectedRevision` | `Run` |
| `GET /companies/{companyId}/activity` | Company action log, optionally filtered by entity or actor | `ActivityPage` |
| `GET /companies/{companyId}/conversations/{conversationId}/transcript` | Existing shared provider-item stream, with UUID `runId` labels | `TranscriptPage` |

No new Agent-specific Run endpoint is needed. `agent_id` on the Run list
means **executed by this Agent**. Company Activity
`actor_type=agent&actor_id={agentId}` means **acted by this Agent**; it cannot supply complete Run history
because lifecycle transitions can have a System actor.

## Agent history filters and pagination

```http
GET /api/v1/companies/{companyId}/runs?agent_id={agentId}&run_type=task&limit=50
GET /api/v1/companies/{companyId}/runs?agent_id={agentId}&run_type=task&limit=50&cursor={nextCursor}
GET /api/v1/companies/{companyId}/runs?agent_id={agentId}&task_id={taskId}&revision=12
```

| Filter | Meaning / validation |
|---|---|
| `agent_id` | UUID of the executing Agent; Agent history supplies this on every page. |
| `run_type` | `task`, `direct_chat`, or `schedule`; omit for all visible types. |
| `revision` | Exact frozen source revision, 1..9007199254740991. Requires `task_id` or `conversation_id`; revision numbers across unrelated entities are not comparable. |
| `task_id` | Original Task source identity, including retained frozen identity after a live Task pointer is cleared. |
| `conversation_id` | Owning Conversation. Task Run revision still means Task revision even when this filter is used. |
| `runtime_id` | Runtime recorded on the Run, independent of the Agent's current runtime binding. |
| `status` | One comma-separated status list; repeated query keys are rejected. |
| `active` | `true` selects nonterminal Runs; mutually exclusive with `status`. |
| `from`, `to` | Inclusive/exclusive bounds on `createdAt`. When both exist, `from < to`. |
| `limit`, `cursor` | 1..100, default 50; opaque UUID-aware cursor. |

Filters combine with AND. `task_id` with a non-Task `run_type` is invalid.
Unknown type, invalid UUID query value, repeated/unknown query key, an unscoped
revision or incompatible filters return `422` using the existing error
envelope. A well-formed inaccessible filter target produces an empty list
within an accessible Company; a missing/inaccessible Company returns `404`.
Malformed or old numeric cursors return `422 invalid_cursor` after cutover.

Responses contain `{items, nextCursor}` ordered by `(createdAt DESC, id DESC)`.
The UUID breaks timestamp ties; its lexical order has no temporal meaning.
Server overfetch determines `nextCursor`; a full page alone does not prove
another page. Reset pagination when Company, Agent or any filter changes.
Refresh the head for newly created Runs; a page cursor is not a realtime
replication checkpoint. Never page or deduplicate by source revision.

## Run projection

The `Run` retains the current execution fields and adds:

| Field | Consumer meaning |
|---|---|
| `id`, `parentRunId` | UUID strings; parent is nullable. Never use `Number`, `parseInt`, arithmetic or number-keyed maps for these identities. |
| `runType` | Explicit origin, including Schedule; never infer from nullable `taskId`. |
| `revision` | Frozen source Task/Conversation revision; explicit null means old input cannot be proven. No Run version is added. |
| `source` | Safe `{id, type, key, title}` frozen at input creation, or null for unknown legacy source identity. Supplies source labels without a per-row Task fetch. |

`taskId` is a live optional pointer and can become null after deletion while
`runType` remains `task`. The frozen `source` still names the original Task.
Direct and Schedule Runs name their Conversation as source. Source labels
may be null for historical unknowns; never invent them from the Agent's
current Task or current settings. A retained source reference is not proof
that a live target still exists; handle missing/inaccessible navigation.

Retries get new UUIDs and inherit the source revision. Do not group different
Runs into one row just because their revision matches. Render explicit
attempt/parent relationships when useful. A shortened UUID is only a display
label; links and cache identities retain the full UUID.

`updatedAt` is a mutation clock that may move for lease/progress writes. It
does not provide exact execution duration or a new lifecycle version. Use
`createdAt` for history order and the server's status/outcome fields for state.
Do not use the current Agent runtime to relabel older executions.

## Company Activity

Activity tracks actions across Company domains. Filter by the changed subject
or verified actor; a Run is one possible entity, not the root of this log.

```http
GET /api/v1/companies/{companyId}/activity
GET /api/v1/companies/{companyId}/activity?entity_type=task&entity_id={taskId}
GET /api/v1/companies/{companyId}/activity?entity_type=task&entity_id={taskId}&before_revision=8&limit=1
GET /api/v1/companies/{companyId}/activity?entity_type=run&entity_id={runId}
GET /api/v1/companies/{companyId}/activity?actor_type=agent&actor_id={agentId}
```

The Company feed and calendar share the broader visibility rules. Task panels
use explicit entity filters. There is no `/runs/{runId}/activity` route or
Activity `runId` field. A Run-subject query returns its own actions, not changes it caused
to Tasks or other subjects. The Agent actor filter is not execution history;
use the Run list for that.

An entry returns `{id, action, actor, entity, revision, details, createdAt}`.
The actor and entity labels are frozen for captured state. `action` keeps the
domain name, such as `task.updated` or `run.completed`; the consumer maps that
to a label. No `changeType`, `recordType`, `previousRevision` or `revisionSource`
is needed. Subject identity already names the owner of a non-null revision.

| Case | Activity `revision` | `details` |
|---|---|---|
| Task/Conversation change | Resulting subject revision | `{schemaVersion: 1, before, after, context}` |
| Run lifecycle action | Null | Same snapshot envelope; Run snapshots retain their frozen input revision |
| `*.history_baselined` action | Observed Task/Conversation revision; null for Run | Known starting state, with `before: null`; not a creation event |
| Legacy/uncovered event | Null | `{schemaVersion: 0, payload: originalRedactedPayload}` |

Creation has `before: null`, deletion has `after: null`, and a cleared field
has null inside a snapshot. Exact previous state/revision is stored in
`details.before`; querying earlier Activity gives the previous retained revision
and may skip a gap. Never assume it equals current revision minus one.
The controlled format migration wraps all legacy details unchanged as the
version-0 payload. Arbitrary old keys never prove that an event is a snapshot;
no extra format-provenance column is needed.

Filters `entity_type`, `entity_id`, `actor_type`, `actor_id`, `action`, `from`,
`to`, `revision`, `before_revision`, `limit` and `cursor` combine with AND.
`entity_id` requires `entity_type`; `actor_id` requires `actor_type`.
Revision filters require an exact Task/Conversation subject and cannot be
combined with each other. Repeated/unknown keys, invalid enums/ranges or
incompatible filters return 422. A well-formed inaccessible subject gives an
empty Company page; a missing/inaccessible Company gives 404.

Company and ordinary entity pages order by `(createdAt DESC, id DESC)`.
A revision-filtered page contains only revisioned snapshots, ordered by
`revision DESC`; `before_revision=8&limit=1` retrieves the previous retained
revision below 8 using the entity revision index. Cursors bind filters and
ordering mode; reset them when either changes.

`ActivityPage` contains `{items, nextCursor, historyCoverage, historyStartsAt}`.
The current implementation returns `historyCoverage: "partial"` and
`historyStartsAt: null` for every page. Legacy and uncovered event families
prevent a completeness claim; a per-query capture/retention ledger is not
implemented. Page length never establishes coverage. Full allowlists and capture rules are in
[Activity TECH](../specs/activity/TECH.md).

## Privacy and realtime consumption

Filter authorization before pagination, page-existence checks or counts.
Task Runs are visible to the readers of their task chat's Project (ADR 0017);
Direct and Schedule Runs are creator-private.
Company admin role or access to an Agent does not expose another creator's
private Runs. Apply retained scope to Activity even after entity deletion;
exclude rows whose scope cannot prove access. The list only promises
retained Runs, not recovery of deleted Run rows from Activity evidence.

Existing `run.*` lifecycle event names keep the `RunLifecyclePayload`
shape shared with REST, without raw `error`. No new Agent-history event stream
is needed. Update known cached Runs by full UUID, constrained by Company and
filter membership. For a filtered page, a status change can remove a row;
recover the page to fill the gap instead of treating its cached count as a
total. A newly enqueued Run may change the head page and its cursor.

Source revision never rejects a newer lifecycle state: running and completed
can both carry revision 12. Retain terminal guards; reconcile ambiguous
nonterminal ordering/reconnects through the authorized Run read. Recover the
head page when membership/order is uncertain. Shared transcript `seq` and
`transcriptThroughSeq` retain their separate correctness rules. Read the
Conversation stream once, then group by UUID; no per-Run transcript polling.

## Consumer implementation and verification

The Agent panel uses RTK Query infinite pages of 20 Runs, an explicit type
filter and a “Load older runs” action. It displays frozen source labels,
source revision, attempt and status. Activity panels expand version-1
before/after snapshots; version-0 evidence is not presented as a reconstructed
change. Cache keys and realtime correlations use UUID strings throughout.

Generate types with `make api-generate`; validate the served schema and type
freshness with `make api-lint api-types-check`. `make gate` also runs Go
build/vet, the PostgreSQL integration suite with the race detector, existing
frontend/Electron tests, type checking and the production build.
