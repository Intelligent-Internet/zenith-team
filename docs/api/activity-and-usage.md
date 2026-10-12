---
title: Activity and usage
summary: Company activity, usage reports, personal activity insight, and huddle AI budgets
---

Five read surfaces over what a company has already done — the feed, its
calendar, two usage reports and one personal insight — plus configurable
Company huddle AI budgets.

Two shape rules run through the whole file and are worth stating once.

**Responses here are flat.** A window is `periodStartAt` / `periodEndAt`, not
`period: {startAt, endAt}`; a cost caveat is `costCoverageState` beside the
cost, not `costCoverage: {state}`. Nesting is kept only for a reference to
another entity, an array of entities, and the two or three shapes a client
genuinely hands around whole — the five token fields, chiefly, which appear
identically on a total, on a series point and on a model row.

**Money is integer minor units.** Provider cost is `providerCostUsdTicks`, a
decimal string of **1e-10 USD ticks**. That is the unit the daemon reports —
`costUsdTicks` in [daemon.md](daemon.md) — and the stored fixed-point dollar
figure is always a whole number of them, so the round trip is exact and totals
sum with integer arithmetic.

## Configure a huddle AI budget

`GET /api/v1/companies/{companyId}/huddle-budget` requires `usage.read`.
`PUT` on the same resource requires `company.profile.update` and a human
actor. The service rechecks the current membership inside its transaction.

The allowance uses UTC calendar months. One USD is 10^10 ticks. A new Company
starts with a $2 monthly limit (`"20000000000"`) at revision `1`. A Company
with no budget row returns a zero limit and revision `0`; no AI spending is
authorized. Change the limit to $10 with:

```json
{"monthlyLimitUsdTicks":"100000000000","expectedRevision":1}
```

The response includes `monthlyLimitUsdTicks`, `committedUsdTicks`, `spentUsdTicks`, `reservedUsdTicks`,
`remainingUsdTicks`, `revision`, `periodStart`, `periodEnd`, and `warning`.
Money fields are decimal strings. `warning` becomes true at 80% committed for
a nonzero limit. A stale revision returns `409 huddle_budget_conflict`.
Setting the limit to `"0"` prevents new reservations; decreasing a limit does
not erase existing commitments.

`spentUsdTicks` is usage cost recorded by Zenith Team, not a provider invoice.
`reservedUsdTicks` is the additional allowance held beyond recorded usage;
their sum equals `committedUsdTicks`. Missing final receipts retain holds and
must not be represented as confirmed zero-cost sessions.

Reservations pin a Huddle to its original budget period. A retry returns the
same reservation and deadline, including across a month boundary. Partial
coverage shortens the authorized deadline. Uncertain final usage retains the
reservation; confirmed final cumulative usage releases unused funds. Recorded
cost above a reservation remains a commitment rather than being discarded.
These accounting functions are transaction participants for the voice
controller; configuring a budget alone does not dispatch a voice agent.

## List the activity feed

`GET /api/v1/companies/{companyId}/activity` reads permitted recorded actions
across Company domains. Task, Conversation and Run changes capture safe
before/after state in the owning mutation transaction. Other domains retain
redacted event evidence through the Bus sink. An action an Agent takes in a
Run names the Agent, the person the Run works for and the Run (`onBehalfOf`,
`runId`, below). There is no per-Run Activity route; select a Run's own
lifecycle actions by subject.

| Query parameter | Meaning |
|---|---|
| `entity_type` | Subject type, a lowercase domain name that may join words with `_` (`project_grant`); may be supplied alone. |
| `entity_id` | Exact subject ID; requires `entity_type`. |
| `actor_type`, `actor_id` | Verified initiator. An ID requires `user` or `agent`; System has no ID. |
| `action` | Domain action such as `task.updated` or `run.completed`. |
| `revision`, `before_revision` | Exact or earlier retained revision; mutually exclusive, requiring an exact Task/Conversation subject. |
| `from`, `to` | Inclusive/exclusive RFC 3339 bounds on recording time. |
| `limit`, `cursor` | Page size 1..100 (default 50), opaque continuation cursor. |

Filters combine with AND. Normal timelines order by `(createdAt DESC, id DESC)`;
revision-filtered timelines order by revision descending. Cursors bind Company,
reader, filters and ordering mode. Old cursors are rejected. Unknown/repeated
query keys, malformed values and incompatible filters return 422; inaccessible
subjects give an empty page inside an accessible Company. Project access
([ADR 0017](../adr/0017-project-access-scopes.md)) decides who reads a row, and
the calendar counts the same rows:

- A row addressed to one person is listed for them, and when it names a
  Project only while they read that Project. The person who made a grant
  change and later lost the Project keeps that row only if their role holds
  `projects.manage`.
- A Company-wide row that names a Project is listed only for that Project's
  readers.
- A `project_grant` row (who gave whom which Project permissions, including a
  Join) is listed for the Project's readers and for every member whose role
  holds `projects.manage`, so an owner sees an admin let themselves in. A
  Group change writes them too: `project_grant.group_changed` (a Group's set
  on the Project before and after), and `project_grant.group_member_added` and
  `project_grant.group_member_removed`: one row per Project and change,
  `{projectId, groupId, userIds, count}`, naming every person whose stored
  access there moved (sorted), and written only when someone's moved. Every
  `project_grant` row also carries `projectTitle`, and a Group's row
  `groupName`, as they were when the change was made, so the row still reads
  after the Group is deleted or the Project is renamed or archived. Rows
  written before 2026-10-08 carry neither; read the live names for them.
- A `group` row (`group.created`, `group.updated`, `group.deleted`,
  `group.member_added`, `group.member_removed`) names no Project and is
  listed for every active member, as the Team page is ([groups.md](groups.md)).
- A Company-wide row about a Task, a Task chat, a Run, a message, a file, an
  Approval or a Question that names no Project is listed for nobody. Only
  history recorded before Project access existed can lack a Project.

An entry is `{id, action, actor, onBehalfOf, runId, entity, revision, details,
createdAt}`. Actor is `{id, kind, displayName}`; entity is `{id, type, key,
title}`. Descriptive labels are frozen when captured and may be null for
legacy evidence. Activity revision is the resulting Task/Conversation
revision; Run actions have null Activity revision, with the frozen source
revision inside their snapshots.

An action an Agent takes in a Run has three identities: `actor` is the Agent,
`onBehalfOf` is `{id, displayName}` for the person the Run works for (its
Authorizer), and `runId` is the Run; clients read the row as "Eli for Mia".
The person's name is frozen when the row is written, like the actor's, and is
also kept in `details.context` as `onBehalfOfDisplayName`. A person's own
action, a system action, an Agent row that names no Run, and every row
written before this change carry `null` in both. A Task a Run creates also
names both creators: the person stays the Task's creator, and the Agent is
recorded beside them.

Version-1 details are `{schemaVersion: 1, before, after, context}`. Create uses
null before; delete uses null after. `*.history_baselined` records an observed
starting state, not an invented creation. Version-0 details preserve
`{schemaVersion: 0, payload, context?}` for legacy/uncovered evidence. They do
not promise reconstructed previous state. The UI expands recorded snapshots.

No entry names a member's machine: a runtime is its registrant's alone
(ADR 0014). A Run snapshot's `runtimeId` reads `null`, an Agent receipt's
`binding` reads `null`, and daemon wakeups (`runtime.work_available`,
`runtime.pending_work`) are never activity — rows an older server kept are
skipped too.

The page is `{items, nextCursor, historyCoverage, historyStartsAt}`. Current
coverage is always `partial`, with a null starting timestamp: mixed legacy and
uncovered families prevent a completeness claim. Do not infer coverage from
page length or the earliest retained revision.

Active Company membership is checked before reads, paging and counts. A row
that names a Project reads for that Project's readers only, and only for its
addressee among them when it is addressed to one person; a `project_grant` row
also reads for the `projects.manage` holders. A Company-wide row about Task
work that names no Project reads for nobody. Direct/Schedule history remains
creator-private, including after subject deletion and for Company admins.
Unknown scope is excluded. `activity.created` reaches exactly the members whose
feed shows the row ([ADR 0017](../adr/0017-project-access-scopes.md)).

For examples, source revision semantics and Agent execution history, see the
[consumer contract](run-tracking-history.md#company-activity),
[Activity TECH](../specs/activity/TECH.md) and served
[OpenAPI](spec/activity-and-usage.yaml). The Agent actor filter records actions
by that Agent; use the Run list's `agent_id` for executions by that Agent.

## Read the activity calendar

```
GET /api/v1/companies/{companyId}/activity/calendar?month=2026-08&time_zone=UTC
```

One month of per-day activity counts for the company, bucketed in the caller's
time zone. Any active member may read it.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company whose calendar to read |
| `month` | query | **yes** | Calendar month, `YYYY-MM` |
| `time_zone` | query | **yes** | IANA zone name, e.g. `Europe/Berlin`. `Local` is rejected |

**Never build this calendar by paging the feed.** Counting days client-side
means an unbounded backward pagination loop — 5 to 20 sequential requests for a
busy month — followed by one `Intl.DateTimeFormat` call per row on the browser
main thread, and it still gets the boundaries wrong on a DST change. The server
first resolves the local month to a half-open UTC range. That raw
`created_at` range uses the company activity index; SQL then groups only the
bounded rows by local date. The grouping expression itself is not the indexed
part of the query.

`days` is **dense**: every day of the requested month is present, including
days with no activity, so a client renders the grid without generating the key
set or branching on a missing key. Keys are `YYYY-MM-DD` calendar days in
`time_zone`, never instants. Counts are plain JSON numbers — a per-day count
cannot approach 2^53, unlike the token counters further down this file.

This response is not a page and deliberately does not use the `{items,
nextCursor}` envelope: a month is a fixed, complete, directly-indexable bucket
set with nothing to paginate, and forcing it into a list would make a client
rebuild the map it wants. `month` and `timeZone` are echoed so a cached
response is self-describing.

> **Added.** `startAt`, `endAt` and `totalCount`. The first two are the exact
> instants the local month begins and ends — the boundaries this endpoint just
> resolved in SQL. Handing them back means the drill-down from a calendar cell
> into `GET .../activity?from=…&to=…` costs no client-side zone arithmetic at
> all, which matters because that arithmetic is precisely what this endpoint
> exists to remove and precisely what goes wrong across a DST change. Without
> them a client computes the month's UTC bounds a second time, by a different
> method, and the two can disagree. `totalCount` is the number the month header
> renders and the denominator a heat scale divides by; it saves nothing
> expensive, but it makes the response answer its own headline.

Call budget: the home calendar goes from **5–20 sequential requests to 1**.

```json
{
  "month": "2026-08",
  "timeZone": "UTC",
  "startAt": "2026-08-01T00:00:00Z",
  "endAt": "2026-09-01T00:00:00Z",
  "totalCount": 400,
  "days": {
    "2026-08-01": 12,
    "2026-08-02": 0,
    "2026-08-03": 41,
    "2026-08-04": 37,
    "2026-08-05": 28,
    "2026-08-06": 33,
    "2026-08-07": 19,
    "2026-08-08": 4,
    "2026-08-09": 0,
    "2026-08-10": 52,
    "2026-08-11": 44,
    "2026-08-12": 61,
    "2026-08-13": 38,
    "2026-08-14": 22,
    "2026-08-15": 6,
    "2026-08-16": 3,
    "2026-08-17": 0,
    "2026-08-18": 0,
    "2026-08-19": 0,
    "2026-08-20": 0,
    "2026-08-21": 0,
    "2026-08-22": 0,
    "2026-08-23": 0,
    "2026-08-24": 0,
    "2026-08-25": 0,
    "2026-08-26": 0,
    "2026-08-27": 0,
    "2026-08-28": 0,
    "2026-08-29": 0,
    "2026-08-30": 0,
    "2026-08-31": 0
  }
}
```

`startAt` and `endAt` are a half-open range in the same sense the feed uses, so
`from=startAt&to=endAt` on `GET .../activity` reads exactly the entries this
calendar counted. In a zone with an offset they are not UTC midnight:
`month=2026-08&time_zone=Europe/Berlin` answers
`startAt: "2026-07-31T22:00:00Z"`.

`time_zone` is **required**. It never defaults to UTC, because that would
silently return different month boundaries to a caller in Berlin than the ones
they requested.

Errors:

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 422 | `validation_failed` | `month` missing or not `YYYY-MM`; `time_zone` missing, unparseable, or `Local`; any unknown, repeated or empty-valued query parameter |
| 500 | `internal_error` | The aggregate read failed |

## Get my activity insight

```
GET /api/v1/companies/{companyId}/me/activity-insight
```

Lifetime numbers about **the calling user's own** work in this company — not
the company's, and not scoped to any period. Any active member may read it; the
membership must be active and the caller's cached role must still match the
stored one.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company to report on |

This endpoint takes no query parameters, so any parameter sent to it is an
unknown one and is `422 validation_failed` — by the standard rule in
[conventions.md](conventions.md), not by a rule of its own. The check runs
behind the auth and membership gates: an unauthenticated request with a query
is `401`, a non-member's is `404`.

The response carries `Cache-Control: private, no-store`; the reason is with the
usage reports below, and it is the same reason.

```json
{
  "mostUsedProvider": "claude",
  "mostUsedModel": "claude-sonnet-4-5",
  "successfulTasks": "37",
  "totalSessions": "112",
  "completedRuns": "289",
  "failedRuns": "31",
  "totalRuns": "326",
  "lastActiveAt": "2026-08-16T08:41:19.220417Z"
}
```

- `mostUsedProvider` and `mostUsedModel` are `null` together when no run of the
  caller's has a recorded model. When present, both are non-empty.
- `successfulTasks` counts distinct tasks with at least one completed run the
  caller initiated.
- `totalSessions` counts distinct conversations in which the caller started a
  run.
- `completedRuns`, `failedRuns` and `totalRuns` count the caller's runs by
  outcome. `totalRuns` includes runs still in flight, so the three do not sum.
- `lastActiveAt` is when the caller's most recent run started, `null` if none
  ever did.

Every counter here is a decimal string for the reason given under the company
report.

> **Added.** `completedRuns`, `failedRuns`, `totalRuns` and `lastActiveAt`. The
> tile previously showed two success figures with nothing to divide them by, so
> "how often does my work actually land" was unanswerable from this response
> and a client that wanted a rate had to invent a denominator from a different
> endpoint on different filters — which is how a per-agent panel ends up
> reporting a higher count than the total beside it. The three counts come out
> of the same materialized run set as `successfulTasks`, so numerator and
> denominator can never disagree, and they are shipped as counts rather than a
> pre-computed rate so a client can render "289 of 326" or a percentage without
> the server picking. `lastActiveAt` answers "is this person still working
> here"; without it a client pages the activity feed filtered to one actor
> purely to read the first row's timestamp.

> **Changed.** `mostUsedModel` was a nested `{provider, model}` object; it is
> two flat nullable strings, `mostUsedProvider` and `mostUsedModel`. Two
> scalars of one row do not need a wrapper, and a nullable wrapper made every
> consumer null-check the object before reading either half. The endpoint also
> rejected *any* query string with a bespoke check ahead of everything else,
> while its siblings applied the normal unknown-parameter rule. That special
> case is gone; the endpoint is ordinary.

Errors:

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 403 | `company_access_required` | Membership is missing, inactive, or the caller's role no longer matches the stored one |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 422 | `validation_failed` | Any query parameter |
| 500 | `internal_error` | Store failure |

## Get the company usage report

```
GET /api/v1/companies/{companyId}/usage?period=month&time_zone=Europe/Berlin
```

Company-wide token and run totals for a calendar period, the same totals for
the immediately preceding period, a per-agent breakdown, and the per-model mix.
**Needs `usage.read`** (owners and admins) — a member without it gets `403 company_permission_required`, a non-member `404`.
The totals count every Run in the Company, including Runs in other members'
private chats: token and run counts reveal nothing of a chat, so the creator
rule that scopes the Run list does not apply here.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company to report on |
| `period` | query | **yes** | `week`, `month`, or `year` |
| `time_zone` | query | **yes** | IANA zone name, e.g. `Europe/Berlin`. `Local` is rejected |

Both are required — there is no default period and no default zone — and the
query must contain exactly these two, each once and non-empty.

**Every token and run counter in a usage response is a decimal string, never a
JSON number.** They routinely exceed 2^53 and JavaScript consumes them, which
is the big-integer rule in [conventions.md](conventions.md): sum them with
`BigInt` or a decimal library, never with `Number()`. They arrive from Postgres
already rendered as text and are not converted server-side. The same holds for
`providerCostUsdTicks`, which is a count of 1e-10 USD ticks and reaches the
same magnitudes. The exceptions are the genuinely small integers —
`shareBasisPoints`, `position`, and the calendar's day counts —
which stay numbers.

The server derives the window from the current time in `time_zone`: `week`
starts the current Monday and runs 7 days, `month` the 1st of the current
month, `year` January 1st. The comparison window is the immediately preceding
one of the same length.

The response is sent with `Cache-Control: private, no-store`. The report is
computed per caller and per company; a shared cache must never hold it, and a
re-read recomputes the window rather than serving a frozen result.

Every total is aggregated directly from Runs and their immutable `run_usage`
facts in the requested half-open window. There are no hourly summary tables,
startup reconstruction, approximation flags, or reporting-watermark state to
merge into the result. A stored fact is the auditable source for both the
per-Run view and these grouped reports.

| Field | Type | Notes |
|---|---|---|
| `periodStartAt` / `periodEndAt` | timestamp | Half-open: start inclusive, end exclusive |
| `comparisonStartAt` / `comparisonEndAt` | timestamp | The preceding window of the same length |
| `timeZone` | string | Echo of the request. Describes the whole report |
| `totals.inputTokens` … `totals.cacheWriteTokens` | string | The four token counters. There is no combined input+output figure on the wire — a client that wants it sums the two counters it already holds |
| `totals.runCount` | string | Runs in the window |
| `comparisonTotals` | object | The same five fields for the preceding window |
| `agents[].agentId` | uuid string | |
| `agents[].name` / `agents[].title` | string | |
| `agents[].avatarKey` | string \| null | The agent's Fold look key (`fold-<palette>`), `null` when it has none |
| `agents[].inputTokens` … `agents[].runCount` | string | The same five counters, inline on the agent |
| `models[].provider` / `models[].model` | string | Both, always — see below |
| `models[].inputTokens` … `models[].cacheWriteTokens` | string | The four token counters for that slice |
| `models[].providerCostUsdTicks` | string \| null | 1e-10 USD ticks, `null` when nothing in the slice reported a cost |
| `models[].costCoverageState` | string | `complete`, `partial`, or `missing` |
| `models[].costReportedFactCount` / `costMissingFactCount` | string | How much of the slice was priced |

`totals` and `comparisonTotals` keep their names, and so does each row of
`agents`: all three are the same five-counter shape, which is a value object a
client hands to one renderer and one delta function. The *window* fields do
not, and that is the difference — `timeZone` describes the report, not either
window.

> **Removed.** `inputOutputTokens`, from every token block in both reports.
> It was always exactly `inputTokens + outputTokens` — a sum the client takes
> from the two counters it already holds, and a second carrier of the same
> fact that could only agree with them or lie.

Call budget: the settings screen is **2 requests** against this contract —
this report and the activity insight.

```json
{
  "periodStartAt": "2026-07-31T22:00:00Z",
  "periodEndAt": "2026-08-31T22:00:00Z",
  "comparisonStartAt": "2026-06-30T22:00:00Z",
  "comparisonEndAt": "2026-07-31T22:00:00Z",
  "timeZone": "Europe/Berlin",
  "totals": {
    "inputTokens": "18446744073709551616",
    "outputTokens": "412889",
    "cacheReadTokens": "2044188",
    "cacheWriteTokens": "51200",
    "runCount": "412"
  },
  "comparisonTotals": {
    "inputTokens": "9120334",
    "outputTokens": "301442",
    "cacheReadTokens": "1500220",
    "cacheWriteTokens": "40960",
    "runCount": "355"
  },
  "agents": [
    {
      "agentId": "0197f2aa-1d40-7c93-8e21-6f5b4c3d2e11",
      "name": "Andrew",
      "title": "CEO Agent",
      "avatarKey": null,
      "inputTokens": "5120334",
      "outputTokens": "201442",
      "cacheReadTokens": "900220",
      "cacheWriteTokens": "20480",
      "runCount": "180"
    },
    {
      "agentId": "0197f2ab-6c80-7d14-9a55-3e1f7b220c48",
      "name": "Eli",
      "title": "Engineering Agent",
      "avatarKey": "fold-periwinkle",
      "inputTokens": "1420334",
      "outputTokens": "60442",
      "cacheReadTokens": "120220",
      "cacheWriteTokens": "4096",
      "runCount": "44"
    }
  ],
  "models": [
    {
      "provider": "claude",
      "model": "claude-sonnet-4-5",
      "inputTokens": "6440668",
      "outputTokens": "251884",
      "cacheReadTokens": "1010440",
      "cacheWriteTokens": "23552",
      "providerCostUsdTicks": "427318000000",
      "costCoverageState": "partial",
      "costReportedFactCount": "172",
      "costMissingFactCount": "8"
    },
    {
      "provider": "codex",
      "model": "gpt-5-codex",
      "inputTokens": "100000",
      "outputTokens": "10000",
      "cacheReadTokens": "10000",
      "cacheWriteTokens": "1024",
      "providerCostUsdTicks": "8300000000",
      "costCoverageState": "complete",
      "costReportedFactCount": "4",
      "costMissingFactCount": "0"
    }
  ]
}
```

`totals.inputTokens` in that example is deliberately past 2^64 to make the
parsing rule impossible to miss; the `agents` and `models` rows below it are
ordinary, and the example does not attempt to make them sum to it.

`agents` and `models` are always arrays, `[]` when the period has no attributed
usage.

> **Added.** `models` — the same window sliced by `(provider, model)` instead
> of by agent. The per-agent grouping collapses the model dimension away
> entirely, so the one question an owner opens this screen to ask, *what are we
> spending our tokens on*, could only be answered by issuing one per-agent
> report per agent and re-summing their model breakdowns client-side. Both
> halves of the pair are kept because neither identifies a price on its own: a
> bare model id collides across providers, and a client prices from its own
> per-model table. Cost rides along in ticks with its coverage state, so a row
> that is only partly priced says so rather than reading as cheap.

> **Changed.** `timeZone` is now the snake_case query parameter `time_zone`,
> matching every other query string in the API. The `period` and
> `comparisonPeriod` objects are gone: their boundaries are now
> `periodStartAt` / `periodEndAt` and `comparisonStartAt` / `comparisonEndAt`,
> while `timeZone` appears once at the top level. The period objects also carried
> `bucket`, which this report never used: it returns no series, and the field
> was there only because the shape was shared with the agent report.

Errors:

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 403 | `company_permission_required` | Authenticated active member without `usage.read` |
| 404 | `company_not_found` | Unknown company, or the caller is not a member |
| 422 | `validation_failed` | `period` or `time_zone` missing, empty, repeated, or an unknown value; an unparseable or `Local` zone; any extra query parameter |
| 500 | `internal_error` | Store failure |

## Get one agent's usage report

```
GET /api/v1/companies/{companyId}/agents/{agentId}/usage?start_at=2026-07-31T22:00:00Z&end_at=2026-08-31T22:00:00Z&time_zone=Europe/Berlin&bucket=day
```

One agent's usage over a caller-chosen window: totals, a bucketed series, a
per provider/model breakdown, and its top tasks. **Needs `usage.read`.**

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the agent belongs to |
| `agentId` | path | yes | Agent uuid |
| `start_at` | query | **yes** | RFC 3339. Must be exact local midnight in `time_zone` |
| `end_at` | query | **yes** | RFC 3339, strictly after `start_at`, at most 367 days later. Must be exact local midnight in `time_zone` |
| `time_zone` | query | **yes** | IANA zone name |
| `bucket` | query | **yes** | `day` or `month` |

All four query parameters are required — there are no defaults — and the query
must contain exactly these four, each once and non-empty.

**`start_at` and `end_at` must each land on an exact local midnight in
`time_zone`, with a zero sub-second component.** `2026-08-01T00:00:00Z` with
`time_zone=Europe/Berlin` is `422`, because that instant is 02:00 local; the
correct value is `2026-07-31T22:00:00Z`. The range is half-open — `start_at`
inclusive, `end_at` exclusive.

A malformed `agentId`, and a well-formed one belonging to another company, are
both `404 agent_not_found`. The id is never echoed back as a validation error.

Token counters are decimal strings, for the reason given above.
`providerCostUsdTicks` is different only in being nullable: `null` when no
provider cost is known for that slice, while `"0"` is an exact reported zero.
The response carries `Cache-Control: private, no-store` for the same reason as
the company report.

Notes on the parts that are easy to misread:

- `costCoverageState` is `complete` (every fact reported a cost), `partial`
  (some did), or `missing` (none did). When it is not `complete`,
  `providerCostUsdTicks` is an undercount, not an estimate of the whole.
- `shareBasisPoints` is that task's share of `topTasksTotalInputOutputTokens`
  in hundredths of a percent, `0..10000`, rounded half-up. It is a plain
  number, not a string.
- `topTasks` is capped at **4**, while `topTasksTotalInputOutputTokens` is the
  agent's total across *all* tasks in the window — so the shares do not sum to
  10000.
- A live task's `status` is its own — one of the `TaskStatus` values, so a
  finished task reads `done`. Deleting a Task detaches its historical
  Conversation and clears `runs.task_id`; the retained Runs and immutable
  usage facts still contribute to totals. The deleted Task itself is omitted
  from `topTasks`, while its usage remains in
  `topTasksTotalInputOutputTokens`.
- `topTasks` lists only tasks in Projects the caller reads
  ([ADR 0017](../adr/0017-project-access-scopes.md)): reading usage is a
  company permission and opens no task. A task in another Project is left
  out, while its tokens still count in `topTasksTotalInputOutputTokens`.
- A series point's `models` carries tokens but no cost. Cost sits on the point,
  because a per-slice figure would have to carry its own coverage caveat to be
  read honestly; for per-model cost inside one bucket, request that bucket as
  the whole window and read the top-level `models`.

```json
{
  "periodStartAt": "2026-07-31T22:00:00Z",
  "periodEndAt": "2026-08-31T22:00:00Z",
  "timeZone": "Europe/Berlin",
  "bucket": "day",
  "totals": {
    "inputTokens": "5120334",
    "outputTokens": "201442",
    "cacheReadTokens": "900220",
    "cacheWriteTokens": "20480",
    "runCount": "180",
    "providerCostUsdTicks": "427318000000",
    "costCoverageState": "partial",
    "costReportedFactCount": "172",
    "costMissingFactCount": "8"
  },
  "series": [
    {
      "startAt": "2026-07-31T22:00:00Z",
      "endAt": "2026-08-01T22:00:00Z",
      "inputTokens": "180440",
      "outputTokens": "7211",
      "cacheReadTokens": "31200",
      "cacheWriteTokens": "1024",
      "runCount": "7",
      "providerCostUsdTicks": "15540000000",
      "costCoverageState": "complete",
      "costReportedFactCount": "7",
      "costMissingFactCount": "0",
      "models": [
        {
          "provider": "claude",
          "model": "claude-sonnet-4-5",
          "inputTokens": "180440",
          "outputTokens": "7211",
          "cacheReadTokens": "31200",
          "cacheWriteTokens": "1024"
        }
      ]
    },
    {
      "startAt": "2026-08-01T22:00:00Z",
      "endAt": "2026-08-02T22:00:00Z",
      "inputTokens": "0",
      "outputTokens": "0",
      "cacheReadTokens": "0",
      "cacheWriteTokens": "0",
      "runCount": "0",
      "providerCostUsdTicks": null,
      "costCoverageState": "missing",
      "costReportedFactCount": "0",
      "costMissingFactCount": "0",
      "models": []
    }
  ],
  "models": [
    {
      "provider": "claude",
      "model": "claude-sonnet-4-5",
      "inputTokens": "5020334",
      "outputTokens": "191442",
      "cacheReadTokens": "880220",
      "cacheWriteTokens": "19456",
      "providerCostUsdTicks": "419018000000",
      "costCoverageState": "partial",
      "costReportedFactCount": "168",
      "costMissingFactCount": "8"
    },
    {
      "provider": "codex",
      "model": "gpt-5-codex",
      "inputTokens": "100000",
      "outputTokens": "10000",
      "cacheReadTokens": "20000",
      "cacheWriteTokens": "1024",
      "providerCostUsdTicks": "8300000000",
      "costCoverageState": "complete",
      "costReportedFactCount": "4",
      "costMissingFactCount": "0"
    }
  ],
  "topTasks": [
    {
      "taskId": "0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
      "taskKey": "ENG-142",
      "taskNumber": 142,
      "title": "Draft the launch checklist",
      "status": "done",
      "inputTokens": "1420334",
      "outputTokens": "60442",
      "shareBasisPoints": 2782
    }
  ],
  "topTasksTotalInputOutputTokens": "5321776"
}
```

`series`, `models`, `topTasks`, and a series point's `models` are always
arrays, `[]` when empty. `series` is dense — every bucket in the window is
present, zeroed where nothing happened — so a chart draws without filling gaps.

> **Added.** `model`, `cacheReadTokens` and `cacheWriteTokens` on each series
> slice. The slice was split by provider alone and carried only input, output
> and their sum, which no pricing table can consume: price is per model, not
> per provider, and cache reads are priced apart from fresh input — often an
> order of magnitude apart. A client holding that slice could draw a token
> chart and nothing else. With the model and the two cache counters it can draw
> the cost chart the tokens were being collected for, and it costs the server
> one extra `GROUP BY` column on an aggregate it already runs.

> **Changed.** The four query parameters are snake_case — `start_at`, `end_at`,
> `time_zone`, `bucket` — rather than camelCase. The `period` object is gone;
> its fields sit flat at the top level. `breakdown` is now `models`,
> named for what it holds and matching the company report and the series slice.
> `topTasks` is the array itself and its denominator is the sibling
> `topTasksTotalInputOutputTokens`, rather than `{totalInputOutputTokens,
> items}` — `items` is the page envelope's key and this is not a page.
> `providerCostEstimateUsd`, a decimal string of dollars, is
> `providerCostUsdTicks`, a decimal string of integer 1e-10 USD ticks: money is
> integer minor units by [conventions.md](conventions.md), the tick is the unit
> the daemon reports and the unit the stored dollar figure is always a whole
> number of, and a dollar decimal could not be summed exactly.
> `costCoverage` is flattened to `costCoverageState`,
> `costReportedFactCount` and `costMissingFactCount` beside the cost they
> qualify.

Errors:

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 403 | `company_permission_required` | Authenticated active member without `usage.read` |
| 404 | `agent_not_found` | `agentId` is malformed, or names an agent outside this company |
| 404 | `company_not_found` | Unknown company, or the caller is not a member |
| 422 | `validation_failed` | Any of the four query parameters missing, empty, repeated or unparseable; a boundary not at exact local midnight; `end_at` not after `start_at` or more than 367 days later; `bucket` outside `day`/`month`; any extra query parameter |
| 500 | `internal_error` | Store failure |

## Realtime Events

| Event | Scope | DTO | Kind |
|---|---|---|---|
| `activity.created` | The members whose feed shows the row: a row that names a Project reaches its readers (only its addressee among them when addressed); a `project_grant` row also reaches the `projects.manage` holders; a Company-wide Task-work row with no Project reaches nobody; any other row keeps its retained visibility (Company or original creator). See [Work inside a Project](realtime.md#work-inside-a-project) | `ActivityEntry` — the identical object `GET .../activity` returns in `items`, actor and entity refs already resolved | Cache |

That is the whole of this file's realtime surface, and it matches the
catalogue in [realtime.md](realtime.md). One consequence per resource:

- **Feed.** Prepend the payload to the newest page. It is the same wire type, so
  no translation and no confirming read. A client holding a *filtered* page
  must re-apply its own filter — the event carries no filter context, and an
  entry whose `action` or actor falls outside the page's filter set does not
  belong on it.
- **Calendar.** A client holding the current month may increment
  `days[<createdAt in the calendar's timeZone>]` by one, and `totalCount` with
  it. If it would rather not bucket client-side at all, re-reading the calendar
  is a single request — which is the point of the endpoint.
- **Usage reports and activity insight.** No events. Re-read these windowed
  aggregates when the screen is re-read.
