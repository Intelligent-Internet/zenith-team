---
title: Runs
summary: Runs, their transcript heads, cancellation, and runtime model discovery
---

A run is one execution of one agent against one conversation — the unit a
daemon claims, streams, and finishes. Nothing here creates a run: runs are
enqueued as a side effect of posting a conversation message, or by
`POST /companies/{companyId}/tasks/{taskId}/runs`. What this resource exposes
is the read projection over runs, the per-Run transcript head, the cancel action,
the runtime roster, and discovery of an exact runtime's models by any member
who may use it.

Run preparation and native configuration are specified in the
[Run PRD](../specs/runs/PRD.md) and [TECH](../specs/runs/TECH.md). The
[workspace API boundary](workspaces.md) distinguishes cloud Run state from
local files and records the Planned managed-only release. No cwd, environment
object, direct-folder selector or GitHub repository reference is added to
this public Run API.

The [Runtime and Run sequence guide](../specs/runs/runtime-and-run-sequences.TECH.md)
traces registration, enqueue, claim, start, transcript receipts and terminal
reconciliation without introducing another Run or transcript API.

For input freeze timing and current producer gaps, see
[Run snapshot coverage](../specs/runs/TECH.md#81-snapshot-timing-and-producer-coverage).

The [Run tracking design](../specs/runs/tracking-history.TECH.md) implements
UUID identity, explicit `runType` and one frozen source `revision`, without a
lifecycle version. The [consumer contract](run-tracking-history.md) covers
Agent history pages, Company Activity filters, privacy and realtime recovery.
The served [OpenAPI](spec/openapi.yaml) generates the frontend's canonical types.
See the [cutover procedure](run-history-cutover.md) before deploying this change.

Prompt/brief freeze at Run creation, Connector authority at claim, and local
files/native policy resolve during preparation. A native session pointer is
not a cloud transcript replay or a filesystem snapshot.

Every route is company-scoped and gated on **member**; a non-member gets `404`,
never `403`. Task-conversation runs are visible to the readers of the task
chat's Project ([ADR 0017](../adr/0017-project-access-scopes.md)), and keep
following that Project after the task is deleted, because the chat keeps it.
Direct-conversation runs remain creator-private across list, read, and cancel. Provider items are
read through their owning Conversation.

> **Changed.** `GET /runs` now reads its query string — it previously ignored
> every parameter and returned a hard-wired newest 100 with no cursor, so a
> company's 101st run was unreachable. Conversation transcripts are paged by `seq`, and
> timestamps are nanosecond UTC like the rest of the API.
>
> Run identities and typed references are UUID strings. Keep the full identity
> in links, requests and cache keys; UUID ordering is only a timestamp tie-breaker.
> Runs expose immutable `runType`, frozen source `revision` and `source` labels.
> Source revision can be null for legacy input. Cancellation has no revision
> precondition: this revision describes input, not mutable lifecycle state.

## Call budget

**A client should never fetch runs to filter them.** Three separate places in
the shipped client fetch all 100 company runs and filter in JavaScript — for
one task's active runs, for one agent's run steps, and for one conversation's
state transitions. The filters below make each of those one scoped request, and
the run state a screen renders continuously arrives over the socket instead
(see [Realtime Events](#realtime-events)).

**A run row carries ids, and the client joins.** `agentId` and `runtimeId`
resolve against the agent roster and runtime roster every client already
caches and keeps live from events — embedding name/status refs in every run
row would put the same facts on the wire once per run and let them go stale
against the very events that update those caches. `runtimeId` is named only
to the member who registered that runtime, whose own roster holds it; every
other reader gets `null` (ADR 0014).

---

## List the company's runs

```
GET /api/v1/companies/{companyId}/runs
```

Newest first (`createdAt DESC, id DESC`). The cursor carries both values; the
next page uses `(created_at, id) < (cursor_created_at, cursor_id)`. The id is a
required tie-breaker for runs created at the same instant.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | A malformed value is `404 company_not_found` — the membership middleware parses it before the handler does |
| `limit` | query | no | 1..100, default 50 |
| `cursor` | query | no | Opaque cursor from a previous `nextCursor` |
| `status` | query | no | **Comma-separated** list of statuses — `status=queued,running`. Not repeatable: a repeated query parameter is `422` API-wide |
| `active` | query | no | `true` restricts to the non-terminal statuses in one parameter. Sending it together with `status` is `422` |
| `task_id` | query | no | Runs serving one task |
| `conversation_id` | query | no | Runs in one conversation |
| `agent_id` | query | no | Runs executed by one agent |
| `runtime_id` | query | no | Runs dispatched to one of **the caller's own** runtimes; another member's runtime returns no runs (ADR 0014) |

> **Added.** `runtime_id`. The runtimes roster shows a machine; the only
> question anyone asks next is what that machine is working on. Without the
> filter a client reads the company's runs and matches `runtime.id` itself.

```json
{
  "items": [
    {
      "runType": "task",
      "revision": 7,
      "source": {
        "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
        "type": "task",
        "key": "ENG-14",
        "title": "Add keyset pagination to the run list"
      },
      "id": "00000000-0000-0000-0000-0000000012d5",
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "agentId": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
      "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "taskExecutionGeneration": 1,
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
      "provider": "codex",
      "status": "running",
      "attempt": 1,
      "maxAttempts": 2,
      "parentRunId": null,
      "triggerMessageId": "0197f4c9-6d10-7a55-9b02-4e1c7a9d3f60",
      "transcriptThroughSeq": 42,
      "error": null,
      "failureReason": null,
      "failureMessage": null,
      "failureDetail": null,
      "waitReason": null,
      "createdAt": "2026-08-15T09:41:02.481113Z",
      "updatedAt": "2026-08-15T09:41:07.902114Z",
      "fireAt": null
    },
    {
      "runType": "direct_chat",
      "revision": 8,
      "source": {
        "id": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
        "type": "conversation",
        "key": null,
        "title": "Andrew conversation"
      },
      "id": "00000000-0000-0000-0000-0000000012c4",
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "agentId": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
      "taskId": null,
      "taskExecutionGeneration": null,
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
      "provider": "codex",
      "status": "failed",
      "attempt": 2,
      "maxAttempts": 2,
      "parentRunId": "00000000-0000-0000-0000-0000000012bf",
      "triggerMessageId": null,
      "transcriptThroughSeq": 31,
      "error": "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 22nd, 2026 11:27 AM.",
      "failureReason": "agent_error.provider_quota_limit",
      "failureMessage": "Codex has hit its usage limit or run out of credit. Retrying will not help until the plan or balance is topped up.",
      "failureDetail": "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 22nd, 2026 11:27 AM.",
      "waitReason": null,
      "createdAt": "2026-08-15T09:12:44.100022Z",
      "updatedAt": "2026-08-15T09:14:02.773640Z",
      "fireAt": null
    }
  ],
  "nextCursor": null
}
```

The same REST object is returned by the read and by cancel. Lifecycle events
carry a distinct safe snapshot with the same cache-relevant fields but no raw
`error` field.

`transcriptThroughSeq` is the authoritative committed Run head. A lifecycle
event carries it, so a client already holding that exact prefix does not
perform a terminal transcript read; a gap falls back to the owning
Conversation's transcript endpoint.

| Field | Type | Notes |
|---|---|---|
| `runType` | string | Immutable source classification: `task` \| `direct_chat` \| `schedule` |
| `revision` | number \| null | Frozen source revision at input freeze (`tasks.version` for a task run, `conversations.version` otherwise); retries inherit it; `null` for legacy input |
| `source` | object \| null | Immutable enqueue-time source `{id, type, key, title}` with `type` `task` \| `conversation`; retained after the source is deleted |
| `id` | UUID string | PostgreSQL-generated execution identity |
| `companyId` | UUID string | The owning company |
| `agentId` | string | The agent executing the run. Joins client-side against the cached agent roster |
| `taskId` | string \| null | `null` for a direct-conversation run |
| `taskExecutionGeneration` | number \| null | The Task execution generation frozen into this run; `null` for a non-Task run. Manual workspace acceptance echoes it |
| `conversationId` | string | Always present — a run always belongs to a conversation |
| `runtimeId` | string \| null | The runtime the run is placed on, for the member who registered it — a run executes on its authorizer's own binding (ADR 0014). `null` for every other reader and on every `run.*` frame. The registrant resolves it against their own runtimes roster |
| `provider` | string | The runtime's provider (`claude` \| `codex`), carried so a surface can name it without a roster join |
| `status` | string | See the lifecycle below. Enforced by a database check constraint |
| `attempt` | number | 1-based. A retry child starts at the parent's attempt + 1 |
| `maxAttempts` | number | The ceiling this run's lineage may reach, never below `attempt`. Equals `attempt` on a failure the server will not retry, so `attempt == maxAttempts` always reads as final |
| `parentRunId` | UUID string \| null | Set only on a server-created retry child |
| `triggerMessageId` | string \| null | The run's own first `user_message` conversation item — still a UUID, since items keep uuid identity. `null` for a run with no words of its own |
| `transcriptThroughSeq` | number | The conversation `seq` of the newest item committed to this Run, and the whole of the committed head |
| `error` | string \| null | Free-form provider failure text, or the fixed cancellation marker |
| `failureReason` | string \| null | Coarse classifier — see below |
| `failureMessage` | string \| null | The user-facing sentence for `failureReason`, naming the provider. Present exactly when `failureReason` is |
| `failureDetail` | string \| null | The provider's or daemon's own words, as one bounded cleaned line (≤ 320 chars). Present when `failureReason` is and any text was recorded |
| `waitReason` | string \| null | Why a `waiting_local_directory` run is parked |
| `createdAt` | string | Enqueue time; the list sort key, with `id` as the tie-breaker |
| `updatedAt` | string | Authoritative mutation clock for cache ordering |
| `fireAt` | string \| null | When a `deferred` run becomes claimable |

Every nullable field is **explicitly `null`** when it has no value, never
absent — the projection is complete on every surface that carries it.

There is no `priority` field. The column exists server-side — a task run
inherits its task's priority, and a direct-conversation run queues at the
`medium` value — but no client writes it and, since the per-agent capacity
gate, it would not mean what a reader would assume. **Priority orders a lane,
not an agent's whole queue.** The gate closes an agent's task lane by removing
those candidates from the claim entirely rather than ranking them lower, so an
at-capacity agent dispatches a direct-chat run ahead of an `urgent` task run
that has waited far longer. Publishing the number would invite clients to
render a queue position it does not describe. See
[the Run lifecycle](../specs/runs/TECH.md#4-lifecycle).

There is no `retryQueued` flag either —
whether a failed run earned a retry is read from the retry child's
`parentRunId` linkage, and the child is an ordinary run that arrives through
the list and `run.queued`.

> **Added.** `maxAttempts` and `fireAt` — the retry story. `attempt` alone
> renders "attempt 2" with no denominator; `failureReason` is an open set, so
> a client cannot decide whether a failure will come back. The server has
> already made that call — `attempt < maxAttempts` means a child is coming,
> and a failure the server will not retry reports its own `attempt` as the
> ceiling — and `fireAt` says when. A failed run used to
> render as final and then a duplicate-looking run appeared seconds later.

> **Added.** `waitReason`. `waiting_local_directory` on its own tells a user
> the run is waiting and nothing about what for.

> **Added.** `triggerMessageId` — the run's own `user_message` item. It is
> `null` on a retry child, which deliberately does not inherit the trigger;
> follow `parentRunId`.
>
> The reverse edge is now `Message.runId`, which is present and non-null on
> **every** conversation item, so the link works from either side. It used to
> exist only on a run's output, which is why the message a human actually sent
> could not carry a run indicator.

**`failureReason` is an open set.** It is a plain text column with no check
constraint, and new reasons ship without an API change. Current values include
`queued_expired`, `runtime_offline`, `runtime_recovery`, `timeout`,
`iteration_limit`, `agent_blocked`, `skill_bundle_unavailable`,
`run_context_invalid`, `no_eligible_resolver` (a Task Run raised an Approval
or Question that nobody who reads its Project may decide; ADR 0017), and
`agent_error.*` values such as `agent_error.provider_quota_limit` or
`agent_error.context_overflow`. Match on the prefix before the dot, and always
have a fallback branch.

**Show `failureMessage`, then `failureDetail`; never invent copy from
`failureReason`.** The sentence is server-owned and names the provider
("Codex has hit its usage limit or run out of credit. Retrying will not help
until the plan or balance is topped up."); the detail is the provider's own
words reduced to one readable line ("You've hit your usage limit. … try again
at Sep 22nd, 2026 11:27 AM."). Both ride the realtime `run.failed` frame and
every run read, so a chat, a task page and a notification can all say the
same thing. The raw `error` stays on the authorized run read only.

One client exception: a Task Run seen by someone who did not provably run it.
When the fix for a reason is on the computer that ran the Run —
`queued_expired`, `runtime_offline`, `skill_bundle_unavailable`,
`run_context_invalid`, `agent_error.provider_auth_or_access`,
`agent_error.provider_network`, `agent_error.missing_config`,
`agent_error.runtime_version_unsupported` and
`agent_error.runtime_missing_executable` — the web client keeps the first
sentence (what happened) and replaces the steps with "Only someone at the
computer that ran it can fix this" (`runFailureMessageForViewer` in
`frontend/src/features/company-plan/model/task-execution.ts`). The proof is
the viewer-scoped `runtimeId`: a run read names it only to the member who
registered that runtime, so `runtimeId != null` means the Run ran on the
reader's own computer. The Task's current assignee is not the proof — a
handoff keeps the earlier Runs, which ran on the previous assignee's
computer. A `run.*` frame never names the runtime, so when a `failed` frame
reports a Run no run list has named yet (the usual case for a Run the chat
just started, a retry child, or a Run started elsewhere), the client reads
that run (`GET /runs/{runId}`) and words the card from its `runtimeId`. Until
that read answers, or if it fails, the card reads the neutral sentence. The
person who ran it, every other reason and every unknown one get the sentence
as sent.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 422 | `validation_failed` | Unknown parameter, an unrecognized `status` value, or `status` together with `active` |
| 422 | `invalid_cursor` | Malformed cursor or unsupported encoding version |

---

## Read one run

```
GET /api/v1/companies/{companyId}/runs/{runId}
```

Returns the object above.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `run_not_found` | Unknown run, one in another company, another creator's direct-conversation run, or a malformed `runId` — a path id that cannot name a row is indistinguishable from one that names nothing |

---

## Read a Conversation's provider transcript

```
GET /api/v1/companies/{companyId}/conversations/{conversationId}/transcript
```

The durable provider record for one Conversation across all its Runs, ordered
by `seq`. An item names its `runId`, has a stable per-Run `itemKey`, one role
(`agent` or `tool`), and one or more structured `text`, `reasoning`, `toolCall`,
`toolResult`, or `error` parts. This read is owned by the Conversation domain;
it is documented here because Run heads are what clients prove against it.

**`seq` is the conversation's counter, not a per-run ordinal.** It is assigned
under the conversation row lock. User messages take positions this filtered
read does not return, so its values ascend but are not dense. Page with the
last returned row's `seq`; never assume the next one is one higher. The same numbering is why a
transcript row and a chat message can always be ordered against each other —
they are two reads of one sequence.

`user_message` items are not returned here. They have no provider parts or item
digest and remain on the chat surface, where a user's words belong.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The owning Company id |
| `conversationId` | path | yes | The Conversation whose provider stream is read |
| `limit` | query | no | 1..200, default 200 |
| `after_seq` | query | no | Rows with `seq > after_seq`. **This is the reconnect path** |

**Catch up here, not by replaying the socket.** After a disconnect, ask for
`after_seq = <the last Conversation seq scanned>` and receive every provider
item committed after it. The socket carries each same item live as a
post-commit `run.message_created`, with Conversation routing and the current
Run head.

```json
{
  "items": [
    {
      "id": "0197f5b0-7781-7a03-9c14-6d2e8f4b1a29",
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "seq": 41,
      "itemKey": "assistant-1",
      "role": "agent",
      "parts": [
        { "type": "reasoning", "text": "Reading the revenue export." },
        {
          "type": "toolCall",
          "callId": "call-1",
          "name": "read_file",
          "input": { "path": "data/revenue.csv" }
        }
      ],
      "itemDigest": "f1a85885c7d01805bd1608852e60fa4c10c77da0a9d5ff50a3aca01dd3f169d0",
      "createdAt": "2026-08-15T09:41:09.114220Z"
    },
    {
      "id": "0197f5b1-1290-7b44-8e05-3a7c9d0e2f81",
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "seq": 42,
      "itemKey": "tool-call-1-result",
      "role": "tool",
      "parts": [
        {
          "type": "toolResult",
          "callId": "call-1",
          "name": "read_file",
          "output": "week,revenue\n2026-W32,184210\n",
          "status": "completed"
        }
      ],
      "itemDigest": "6e4b2576fcc9b65370cdf63307f3ff9c6b658186dfd6db0f55cd9ee7951632b9",
      "createdAt": "2026-08-15T09:41:11.552108Z"
    }
  ],
  "throughSeq": 42,
  "hasMore": false
}
```

Every row repeats `runId` because one page can interleave multiple Runs. Live
`run.message_created` events add `conversationId` and the producing Run's
`throughSeq`; their item fields are otherwise the same.

`role` is a **display label derived on read** from the item's stored type: the
stream stores `type`, because type is what decides visibility, and the role the
daemon sent survives only inside the item's content digest. A `tool_result`
item reads back as `tool` and everything else as `agent`, so a read never
returns `system` even though the append seam accepts it. Nothing should branch
on the difference.

`toolResult.status` uses the closed Zenith Team vocabulary `in_progress`,
`completed`, `failed`, `declined`, or `cancelled`. Providers that report them
also include non-negative `exitCode` and `durationMs`; absent means the provider
did not supply that fact. Tool output is redacted but not silently shortened:
the complete item must fit the 2 MiB transcript-item bound or the Run fails
before an append RPC is sent.

`itemKey` is the provider item's natural identity inside this Run. Replaying
the same key with the same normalized role and parts returns the existing row;
different content conflicts. It is not a generic idempotency key. `itemDigest`
binds that normalized item, and that is the only digest the transcript carries:
`seq` is allocated under the conversation row lock and a committed item is
never rewritten, so the ordering needs no chain to prove it.

This list is sequenced, so a caller pages on with `after_seq` set to the last
returned row's `seq`; there is no page-local cursor, because the last element
of `items` already carries it. `throughSeq` is the newest provider-item
position in the Conversation when the page began. `hasMore` says the bounded
window stopped short of that position. Per-Run convergence does **not** compare
a grouped Run against this Conversation head: it compares against that Run's
own `transcriptThroughSeq` projection. The response has no opaque cursor.

On successful completion, the daemon does not submit an output string. It proves
the committed Run head and nominates its answer with `finalItemKey`; the server
checks that key names this Run's newest committed `agent_text` item and
transitions. **Nothing is copied.** That item is already in the conversation —
it was written there when the daemon appended it, at the position the
conversation lock gave it, and broadcast then as `run.message_created`. Completion
selects; it does not derive a second row. A stale head or an item that is not
the newest agent-text item cannot terminalize the Run.

Two consequences follow. A Run whose turn produced no agent text omits
`finalItemKey` and completes with no `agent_text` item at all — the absence is
the fact, and there is no empty placeholder to render. And a Run that is
cancelled, fails, or is swept keeps every word it had already said, because
those words were never waiting on a successful completion to be written.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `run_not_found` | Unknown run, one in another company, or another creator's direct-conversation run |
| 422 | `validation_failed` | Unknown parameter, or `limit` outside 1..200. A malformed `runId` is the `404` above |

---

## Cancel a run

```
POST /api/v1/companies/{companyId}/runs/{runId}/cancel
```

Requests cancellation and returns the authoritative run projection. The route
takes no request body. A successful transition stores the fixed
`user_cancelled` marker in the run's `error` field.

Cancelling a task run stops work in its Project, so the caller needs
`project.tasks.execute` there (`403 project_permission_required`); someone who
cannot read the Project gets `404 run_not_found`
([ADR 0017](../adr/0017-project-access-scopes.md)).

**Only the run's authorizer or a `tasks.assign` holder may cancel it.**
Stopping a run acts on the computer it runs on, which is its authorizer's
own (ADR 0014). Any other member who can read the run — a colleague reading a
task's runs, for example — gets `403 company_permission_required` and nothing
changes. A client shows its Stop control only where the run is the viewer's
(its `runtimeId` is set for them) or the viewer holds `tasks.assign`.

**This mutation takes no `expectedRevision`**, the one place the runs domain
deviates from the concurrency rule in [conventions.md](conventions.md). A run
has an immutable source `revision`, which is not a lifecycle precondition. Cancellation is idempotent: the transition
matches only non-terminal statuses, and a run that finished first is returned
unchanged.

```json
{
  "runType": "task",
  "revision": 7,
  "source": {
    "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
    "type": "task",
    "key": "ENG-14",
    "title": "Add keyset pagination to the run list"
  },
  "id": "00000000-0000-0000-0000-0000000012d5",
  "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
  "agentId": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
  "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
  "taskExecutionGeneration": 1,
  "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
  "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
  "provider": "codex",
  "status": "cancelled",
  "attempt": 1,
  "maxAttempts": 2,
  "parentRunId": null,
  "triggerMessageId": "0197f4c9-6d10-7a55-9b02-4e1c7a9d3f60",
  "transcriptThroughSeq": 42,
  "error": "user_cancelled",
  "failureReason": null,
  "failureMessage": null,
  "failureDetail": null,
  "waitReason": null,
  "createdAt": "2026-08-15T09:41:02.481113Z",
  "updatedAt": "2026-08-15T09:43:18.004920Z",
  "fireAt": null
}
```

The response is the whole run, so no confirming `GET` is needed.

The database transition is immediate. Stopping an already-running provider
process is cooperative, but the returned row is already `cancelled` and the
server publishes `run.cancelled` after commit.

Cancelling a Run does not cancel its Task. The Task keeps its current workflow
state and Conversation, so the user can send a follow-up message to start a
fresh Run in the same retained Task workspace. Task cancellation remains a
separate Task mutation.

**Cancelling never erases what the agent already said.** Each `agent_text` item
was committed to the conversation as it arrived, not held back until the run
succeeded, so the words the user has already read stay in the stream. A
cancelled run simply stops adding to it.

**A cancellation transition never produces a retry.** An already-terminal run
is returned unchanged; whether a failed run already earned a retry child is
read from that child's `parentRunId` linkage, not from a flag on the parent.

**A Task Run needs its member's `project.tasks.execute`** ([ADR
0017](../adr/0017-project-access-scopes.md)). Every Run in a task chat is
queued only while the member it works for holds that permission on the
chat's Project, and claimed only while they still do: a write that would
start one without it is refused with `403 project_permission_required`
naming the agent, and a queued one is never handed to a computer. An
Approval or Question continuation the member may no longer run is abandoned
with reason `project_access_revoked`, and the member gets a `run_failed`
Inbox item.

**The server cancels a Run when the member it works for loses access to its
Project** ([ADR 0017](../adr/0017-project-access-scopes.md)). A grant change
that takes `project.read` or `project.tasks.execute` from a person, or a
Company-visible Project made restricted, cancels that person's queued,
deferred, claimed and running Runs on the Project's chats. `error` reads
`project_access_revoked`, the Run tokens are deleted so the daemon's next call
fails, and `run.cancelled` is published. A Task still `in_progress` on that Run
moves to `cancelled`; it keeps its assignee, and someone holding
`tasks.assign` who reads the Project hands it over. A member who still reads
the Project (they lost only `project.tasks.execute`) gets one `run_failed`
Inbox item per stopped Run, naming the agent and saying they can no longer
run tasks in this work; one who lost `project.read` is told nothing
([inbox.md](inbox.md)). When a member leaves
the Company, their Runs end with the runtime sweep instead (`error`
`runtime_owner_inactive`), because every Run they authorized executes on a
runtime they own.

The upgrade that introduced Project access did the same once, in its
migration: every Run in a task chat that had not ended and whose member did
not hold `project.tasks.execute` on the chat's Project afterwards (at that
point only owners held it), or whose task chat had no Project, was cancelled
with `error` `project_access_revoked`, its tokens deleted and its Task moved
as above. No `run.cancelled` frame was sent; clients load fresh state after
the upgrade.

**The server cancels the live Runs of a task a Company Plan deletes.** A plan
apply that deletes a task by leaving it out cancels its queued, deferred,
claimed and running Runs first: `error` reads `task_deleted`, the Run tokens
are deleted and `run.cancelled` is published. Otherwise a queued Run would
outlive its task, still claimable for work nobody holds.

**The server fails a Task Run that raises something nobody may decide.**
When a Task Run raises an Approval or Question and no reader of its task
chat's Project holds the deciding permission (`project.approvals.resolve`),
the raise answers `409 no_eligible_resolver`, nothing is stored, and the Run
fails in the same transaction with `failureReason` `no_eligible_resolver`:
its tokens are deleted, `run.failed` is published, it is never retried, and
the member it worked for gets a `run_failed` Inbox item with the
provider-aware `failureMessage`. `error` stays `null`: nothing went wrong
with the provider.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 403 | `csrf_invalid` | Cookie caller omitted a matching CSRF header |
| 403 | `company_permission_required` | The caller can read the run but neither authorized it nor holds `tasks.assign` |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `run_not_found` | Unknown run, one in another company, another creator's direct-conversation run, or a malformed `runId` |

> **There is no rerun endpoint.** When a run fails with a retryable
> `failureReason`, the server enqueues a **child** run carrying `parentRunId`
> and an incremented `attempt`, up to `maxAttempts`. The child says when it
> will fire with `fireAt`, and is an ordinary run that appears in the list —
> its `parentRunId` linkage is how a client learns the failed parent got a
> retry. To re-ask the agent, post a new conversation message.

---

## List the company's runtimes

```
GET /api/v1/companies/{companyId}/runtimes
```

The runtimes a daemon has registered for this company **that the caller
registered**. A runtime is private to its registrant, always (ADR 0014):
every caller receives their own machines and nothing else — the company
owner and admins included never receive another member's runtime. Another
member's machine is absent, not flagged, so an empty list means the caller
has registered nothing here. Runs are dispatched to these: only the
registrant's own authorized Runs, on the agents they have
[set up](agents.md#set-up-an-agent-on-your-computer) there. All matching
runtimes are returned in one response; `status` and `provider` are filters,
not pagination controls.

**A runtime is the daemon's record, with two user-plane writes.** There is no
enable, disable, or create on this plane — a runtime exists because a daemon
registered it and goes offline when that daemon deregisters or is swept. The
registrant writes two things: the machine's name
([Rename a machine](#rename-a-machine) sets `customName` beside the daemon's
`name`), and they may take a runtime out of the company
([Remove a runtime](#remove-a-runtime)), after which it is gone from this
list while its run history stays readable. Model discovery below is a
transient action against the record, not a runtime write.

| Parameter | In | Required | Description |
|---|---|---|---|
| `status` | query | no | Filter by liveness — `online` or `offline` |
| `provider` | query | no | Filter to one provider |

> **Added.** `provider`. An agent can only be set up on a runtime of its own
> provider, so the binding picker read the whole roster and discarded most of
> it.

```json
{
  "items": [
    {
      "id": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "daemonId": "8f1c0b6a-3d92-4d1e-9a77-2b5f0c8e1d34",
      "name": "duong-macbook",
      "customName": null,
      "provider": "claude",
      "status": "online",
      "unusableReason": null,
      "signIn": {
        "state": "signed_in",
        "detail": "Signed in (max).",
        "checkedAt": "2026-08-15T09:40:58.104000Z"
      },
      "owner": {
        "id": "0197f4a0-2200-7b31-9d44-8a0e5c1b7f22",
        "kind": "user",
        "name": "Duong Phung",
        "avatarUrl": null
      },
      "deviceInfo": "macOS 15.5 (arm64)",
      "providerVersion": "2.1.4",
      "cliVersion": "0.9.3",
      "launchedBy": "desktop",
      "lastSeenAt": "2026-08-15T09:41:00.001200Z",
      "createdAt": "2026-08-01T11:02:44.881000Z",
      "updatedAt": "2026-08-15T09:41:00.001200Z"
    }
  ]
}
```

`provider` is the closed `claude | codex` vocabulary, validated exactly at
registration and enforced by PostgreSQL. `deviceInfo` is the daemon's
self-reported device string, verbatim and unparsed — a display label, not a
structure.

`unusableReason` is `null` for an ordinary row, or `{code, message}` when the
machine's provider CLI cannot run work there — `code` is
`below_minimum_version` (the CLI parsed a version below the minimum Zenith Team
supports) or `not_executable` (the binary was found but the daemon could not
execute it); `message` is the daemon's sentence, for example
`"Codex CLI 0.90.0 is below the minimum 0.100.0"`, never a path. A runtime
carrying a reason is always `offline`, and stays offline through heartbeats:
the machine is on, the fix is local. The daemon clears it on its next
registration once the CLI is fixed (an upgrade is noticed within ten
minutes). Agents the caller has set up on such a runtime read
`unavailableReason: "runtime_unusable"` for them — see [Agents](agents.md).

`signIn` is `null` until a registration carries a verdict, or `{state,
detail, checkedAt}`: `state` is `signed_in`, `signed_out`, or `unknown` (the
provider's status command failed or printed something unexpected), `detail`
is the daemon's short sentence naming the fix ("Not signed in. Run `claude
auth login` on this computer, then refresh." for Claude Code, "Not signed
in. Run `codex login` on this computer, then refresh." for Codex), and
`checkedAt` is when the daemon looked. The daemon asks the provider CLI
itself — `claude auth status`, `codex login status` — in the same probe
round as the version, so the verdict never comes from a failed run. It does
not change `status`: a signed-out provider stays `online`, and a run on it
fails with a readable `provider_auth_or_access` reason; rosters and pickers
show the state so nobody starts that run. The background version check
re-registers when the verdict changes, and `POST …/refresh` (below) asks the
daemon to look right now.

> **Added.** `signIn`. The roster used to mark a provider online from its
> `--version` alone, so a signed-out Claude or an exhausted Codex looked as
> ready as a working one until the first run failed.

> **Added.** `unusableReason`. A provider that was found but cannot run used
> to be dropped from the registration and was visible only in the daemon's
> local `/health`; the roster showed nothing, or a stale offline row that
> looked like a closed laptop. "Machine off" and "machine on, CLI broken"
> call for different recoveries, and this is the field that tells them
> apart.

> **Added.** `providerVersion`, `cliVersion`, `launchedBy`. The daemon
> announces all three at registration and the row already stored them; the
> roster and the detail read now serve them. `providerVersion` is the provider
> CLI's version as probed on the machine, `cliVersion` is the `zen`
> daemon's own version, and `launchedBy` is `desktop` when the desktop app
> spawned the daemon or `cli` for a terminal start. The versions are plain
> strings, empty when the daemon reported none (a row registered before
> versions were announced); they are never `null`. The daemon only ever
> announces the desktop case, so `cli` is the server's name for "not the
> desktop app" — a client never branches on an empty string.

`customName` is the name a person gave the machine, or `null` when nobody
renamed it. It sits beside `name` rather than replacing it — the daemon keeps
writing `name` on every registration and never touches `customName` — so a
rename survives a restart and clearing it restores the device name. Render
`customName ?? name`. Every runtime the same daemon registered shares it when
the rename applied to the machine; see [Rename a machine](#rename-a-machine).

> **Added.** `customName`. Two members with the same laptop registered two
> "Claude (MacBook Pro)" rows nobody could tell apart, and the daemon's name
> is not the user's to edit — the next registration would write it back. A
> separate column is what lets the name be the user's for good.

A runtime carries **no `revision`**: the table has no version column. The
rename is a last-writer-wins set of a display field with nothing to guard,
and the removal moves or drops only the caller's own bindings, judged under
the row locks. `updatedAt` moves on every registration,
heartbeat-with-a-DB-write, sweep, rename, and removal; `lastSeenAt` is the
liveness signal.

There is no `runtimeMode`: it was a one-value enum with no writer for any
second value, and a field nothing varies is not a contract. There is no
per-row run counter either — "what is this machine working on" is
`GET /runs?runtime_id={id}&active=true`, which the roster links into.

> **Added.** `daemonId`. One laptop running both providers registers **two**
> runtimes, and this is the only value that says they are the same machine. A
> roster that groups by device cannot be built without it. Always present:
> every runtime is registered by a machine, and the id is half of the natural
> identity the registration upsert arbitrates on.

> **Added.** `owner` — the member whose machine this is, resolved from the
> registering principal, `null` when the registration carried no user. On a
> user-plane read it is always the caller (ADR 0014); it is served so the
> row means the same thing on every plane.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown company or not a member |
| 422 | `validation_failed` | Unknown parameter |

---

## Read one runtime

```
GET /api/v1/companies/{companyId}/runtimes/{runtimeId}
```

Returns the object above — one roster row by id, owner included. A member
may read a runtime they registered and nothing else: another member's
runtime is `404 runtime_not_found` for every role, exactly like an unknown
id, so a known id is not an oracle (ADR 0014).

The detail screen needs two more reads, and both already exist as filters
rather than embedded lists so each stays one request and caches on its own:

- the agents the caller has set up on the runtime:
  `GET /agent-bindings?runtime_id={runtimeId}` — see
  [Agents](agents.md#list-your-bindings);
- the work on the machine right now:
  `GET /runs?runtime_id={runtimeId}&active=true` (member).

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No session |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `runtime_not_found` | Unknown, another company's, another member's, or malformed `runtimeId` — the answer never says whether the id exists elsewhere |

---

## Rename a machine

```
PATCH /api/v1/companies/{companyId}/runtimes/{runtimeId}
```

Sets or clears the name a person gives the machine. The registrant renames
their own machine, whatever their role; another member's runtime is
invisible to every role and answers `404 runtime_not_found` (ADR 0014). With
`applyToMachine` the name reaches every runtime the same daemon registered:
the name is the machine's. The daemon's `name` is never touched, so clearing
the custom name restores it.

```json
{
  "customName": "Office Mac",
  "applyToMachine": true
}
```

| Field | Type | Notes |
|---|---|---|
| `customName` | string | Required. Trimmed by the server; at most 100 characters including surrounding whitespace; `""` clears |
| `applyToMachine` | boolean | Optional, default `false`. `true` renames every runtime the same daemon registered in this company — both provider rows of one laptop — which is what renaming a *machine* means; `false` renames this row only |

Returns the renamed runtime, the same object the reads serve:

```json
{
  "id": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
  "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
  "daemonId": "8f1c0b6a-3d92-4d1e-9a77-2b5f0c8e1d34",
  "name": "duong-macbook",
  "customName": "Office Mac",
  "provider": "claude",
  "status": "online",
  "unusableReason": null,
  "signIn": {
    "state": "signed_in",
    "detail": "Signed in (max).",
    "checkedAt": "2026-08-15T09:40:58.104000Z"
  },
  "owner": {
    "id": "0197f4a0-2200-7b31-9d44-8a0e5c1b7f22",
    "kind": "user",
    "name": "Duong Phung",
    "avatarUrl": null
  },
  "deviceInfo": "macOS 15.5 (arm64)",
  "providerVersion": "2.1.4",
  "cliVersion": "0.9.3",
  "launchedBy": "desktop",
  "lastSeenAt": "2026-08-15T09:41:00.001200Z",
  "createdAt": "2026-08-01T11:02:44.881000Z",
  "updatedAt": "2026-09-09T08:00:00.000000Z"
}
```

The write carries **no `revision`**, deliberately: the row has no version
column, and the rename is a last-writer-wins set of one display field. Two
people renaming the same machine at once end with whichever committed last,
which is the right outcome for a label.

After the commit the registrant receives `daemon.roster_changed` with
`action: update`, naming the machine (`daemonId`) and the runtimes it
touched (`runtimeIds`), and refetches the roster — see
[realtime.md](realtime.md). Agents they set up on the machine carry the new
name in their `binding.runtime.customName` on the next read; nothing about
availability changed, so no `agent.status_changed` follows.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON or an unknown field |
| 401 | `authentication_required` | No session |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `runtime_not_found` | Unknown, another company's, malformed, or removed `runtimeId` — or another member's runtime, which the caller cannot see |
| 413 | `request_too_large` | Body over the limit |
| 415 | `unsupported_media_type` | Body without `Content-Type: application/json` |
| 422 | `validation_failed` | `customName` missing or `null` (a "clear" is `""`), or longer than 100 characters |

---

## Remove a runtime

```
DELETE /api/v1/companies/{companyId}/runtimes/{runtimeId}
POST   /api/v1/companies/{companyId}/runtimes/{runtimeId}/remove
```

The registrant's removal (ADR 0014), whatever their role. Takes a runtime
out of the company. Run history outlives a machine, so removal is **soft —
the row is stamped removed and hidden from every runtime read — plus a
destination** for the caller's bindings on it: every agent they had
[set up](agents.md#set-up-an-agent-on-your-computer) there.

Two forms. `DELETE` drops the bindings: it answers `204` with no body, and
the caller's `queued` and `deferred` Runs on the machine are cancelled
(`run.cancelled`, reason `runtime_removed`). `POST …/remove` names where the
bindings go instead:

```json
{ "targetRuntimeId": "0198c0b2-2c3d-7e4f-9a51-6b7c8d9e0f11" }
```

| Field | Type | Notes |
|---|---|---|
| `targetRuntimeId` | uuid \| null | Required key. Another runtime **the caller registered**, of the same provider, online or not — or `null`, which is exactly the `DELETE`. The runtime being removed is refused with `422` |

Both forms check, inside one transaction under the runtime and binding
locks, that the runtime is **offline** — a live daemon would register the
row again moments later, so the answer is `409 runtime_remove_blocked` with
"Quit Zenith Team on that computer first, or stop its zen daemon, then try
again". With a target, it must be another
live runtime the caller registered (`404 runtime_not_found`, like an
unknown or another member's) of the same provider
(`422 agent_binding_provider_mismatch`). "Offline" is judged on the row
alone, so an **unusable** runtime (`unusableReason` set) can be removed
while its machine is still on — but that removal is futile: the daemon's
next heartbeat is answered `404 runtime_not_found` and it re-registers the
row, with the same reason, within about 30 seconds. Stop the daemon first
there too.

Then, in the same transaction: every binding the caller holds on the
runtime moves to the target and their `queued` and `deferred` Runs follow
so the target's daemon picks them up — or, with no target, the bindings are
deleted and those Runs cancelled; and the runtime is stamped removed. The
agents themselves are untouched: a template has no machine, so nothing
about it changes when one member's machine goes. Runs a daemon already
claimed (`dispatched`, `running`, `waiting_local_directory`) stay on the
offline runtime, where the offline sweepers fail and retry them as usual.

`POST …/remove` returns the receipt:

```json
{
  "removedRuntimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
  "movedBindingCount": 2,
  "retargetedRunCount": 2,
  "cancelledRunCount": 0
}
```

Every count is always present: `movedBindingCount` bindings now on the
target, `retargetedRunCount` queued and deferred Runs now waiting there,
`cancelledRunCount` Runs cancelled because the bindings were dropped.

After the removal the runtime is absent from
[the list](#list-the-companys-runtimes), [the read](#read-one-runtime), the
rename, the daemon's heartbeat and the claim path — a daemon that beats for
it is told the runtime is gone, and the same machine registering the same
runtime again revives the row, which is what a deliberate restart means.
`GET /runs?runtime_id={runtimeId}` still answers with the machine's run
history. The garbage collector treats a removed row like any offline one:
kept while a run references it or a binding still names it, dropped after
seven days otherwise.

After the commit the registrant receives one `agent.binding_changed` per
moved or dropped binding (the new binding, or `null`), one
`agent.status_changed` for the target when anything moved, and
`daemon.roster_changed` with `action: remove` naming the machine
(`daemonId`) and the row (`runtimeIds`) — see [realtime.md](realtime.md).
No `run.queued` is re-emitted for the moved runs: their ids are unchanged.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, an unknown field, or a `DELETE` body carrying fields |
| 401 | `authentication_required` | No session |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `runtime_not_found` | Unknown, another company's, malformed, already removed, or another member's `runtimeId` or `targetRuntimeId` — invisible to the caller, so it answers like an unknown id (ADR 0014) |
| 409 | `runtime_remove_blocked` | The runtime is online |
| 413 | `request_too_large` | Body over the limit |
| 415 | `unsupported_media_type` | Body without `Content-Type: application/json` |
| 422 | `validation_failed` | `targetRuntimeId` missing, not a UUID or `null`, or naming the runtime being removed; the detail says which |
| 422 | `agent_binding_provider_mismatch` | The target runs the other provider |

---

## Refresh a machine's provider setup

```http
POST /api/v1/companies/{companyId}/runtimes/{runtimeId}/refresh
```

The registrant's ask, whatever their role (ADR 0014); another member's
runtime is invisible and answers `404 runtime_not_found`. No request body,
answers `202` with no body. The daemon behind
the exact online runtime in the path probes every provider CLI on that
machine again — path, version, sign-in — and registers what it finds; that
registration is the result, served on the `Runtime` rows and announced by
`daemon.roster_changed`, so a client that already refetches the roster on
that event sees the new `providerVersion` and `signIn` without polling.
One request covers every provider on the machine, whichever runtime id
names it. The hint rides the daemon's realtime socket and is safe to lose or
repeat — a lost one costs a second click. A runtime that is `offline` has no
daemon to hear it: `409 runtime_offline`.

```text
202 Accepted
```

---

## Discover a runtime's models

```http
POST /api/v1/companies/{companyId}/runtimes/{runtimeId}/models
```

This action, the registrant's whatever their role (ADR 0014), schedules
discovery against the exact online runtime in the path. It has no request body and always returns `202`, including when an
earlier request for that runtime is still `pending` or `running`; in that case
the existing request is returned instead of scheduling duplicate work.

```json
{
  "requestId": "0197f61a-b881-7a22-b1ca-a3e972e2f21d",
  "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
  "status": "pending",
  "models": [],
  "createdAt": "2026-08-19T09:41:02.481113Z",
  "updatedAt": "2026-08-19T09:41:02.481113Z"
}
```

Discovery coordination is transient and never enters PostgreSQL. A pending
request times out after 30 seconds without a daemon claim; a running request
times out after 60 seconds without a result. Terminal requests are retained
for two minutes.

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `runtime_not_found` | Unknown, malformed, or out-of-company runtime — or another member's machine, which the caller cannot see |
| 409 | `runtime_offline` | The exact runtime is not currently online |

## Read model-discovery status

```http
GET /api/v1/companies/{companyId}/runtimes/{runtimeId}/models/{requestId}
```

This read, gated like the action that created the request, returns `pending`,
`running`, `completed`, `failed`, or `timeout`. The company, runtime, and request must be the exact tuple that
created the request; a cross-scope lookup is the non-disclosing
`404 model_discovery_not_found`.

A completed response carries provider-native ids and optional reasoning
choices. The selected values can be passed back to that provider unchanged.

```json
{
  "requestId": "0197f61a-b881-7a22-b1ca-a3e972e2f21d",
  "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
  "status": "completed",
  "models": [
    {
      "id": "gpt-5.6-sol",
      "label": "GPT-5.6-Sol",
      "default": true,
      "reasoning": {
        "defaultLevel": "medium",
        "levels": [
          { "value": "medium", "label": "Medium" },
          { "value": "high", "label": "High" }
        ]
      }
    }
  ],
  "createdAt": "2026-08-19T09:41:02.481113Z",
  "updatedAt": "2026-08-19T09:41:04.002411Z"
}
```

A failed or timed-out response instead carries a bounded, user-safe `error`
with `code` and `message`. Executable paths, stderr, environment values,
credentials, and tokens are never returned.

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `runtime_not_found` | Unknown, malformed, or out-of-company runtime — or another member's machine |
| 404 | `model_discovery_not_found` | Unknown, malformed, expired, or cross-runtime request |

---

## Realtime Events

Run state is the most volatile thing in the product and it should reach a
screen without a confirming read. The server derives its typed event audience
from the Run's conversation; clients do not control routing.

| Event | Audience | Carries | Delivery / authority | Kind |
|---|---|---|---|---|
| `run.queued` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.dispatched` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.running` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.waiting_local_directory` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.deferred` | conversation-derived | The safe run snapshot with `waitReason` | relayed row projection | Cache |
| `run.completed` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.failed` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.cancelled` | conversation-derived | The safe run snapshot | relayed row projection | Cache |
| `run.message_created` | conversation-derived | One full structured item plus routing ids, `seq`, item digest, and committed Run head | relayed post-commit row | Cache |

**The safe lifecycle type has no `error` field.** Provider failure text can be
large and remains available only on the authorized run read. `failureReason`,
`attempt`/`maxAttempts`, and `fireAt` stay on the event, which is what a
client needs to render "failed — retrying in 30s" without a read; the retry
child itself arrives as its own `run.queued`.

The cloud API carries no token-delta event. `run.message_created` is published only
after the item and Run head commit, then relayed to every client eligible for
the owning conversation. It is published before the daemon can submit the
terminal callback, preserving provider item-before-finished ordering. Web
clients therefore show provider output at item completion. Electron can
separately render the daemon's authenticated same-machine preview, which is
not part of this event catalogue and is never a recovery source.

**`run.message_created` is also the only delivery of the agent's chat reply.** There is
no `message.created` for agent text — the reply is an `agent_text` item of the
conversation, so it arrives here and nowhere else, and nothing further follows
`run.completed`. A chat panel appends from this event rather than waiting for
the run to finish. See [realtime.md](realtime.md#there-is-no-messagecreated-for-agent-text).

---

## Run lifecycle

```
                 ┌──────────────► deferred ──┐
                 │                           │
∅ ──► queued ──► dispatched ──► running ──►  ├─► completed
                 │      │                    ├─► failed ──► (retry child: new run, attempt+1)
                 │      └─► waiting_local_directory
                 │                           │
                 └───────────────────────────┴─► cancelled
```

Terminal states are `completed`, `failed`, and `cancelled`; for all three
`updatedAt` is when the run got there — the row carries its current state and
one clock, and the sequence of transitions is the activity feed's.
`waiting_local_directory` is a park on a busy local path, not a
human wait — for that, see [approvals.md](approvals.md); `waitReason` says
which path. `deferred` parks a Run for a retry backoff (`fireAt`).
When a running Agent actually invokes a Studio command that needs sign-in,
the daemon sets `waitReason: studio_auth_required` through
`POST /api/v1/daemon/runs/{runId}/wait-studio-auth` with `waiting: true`.
The Run stays `running`: its provider, heartbeat, transcript and CLI remain
alive. Chat shows **Connect Zenith Studio** inline in the conversation.
After successful sign-in Desktop returns to the foreground, the CLI clears the
wait with `waiting: false`, and continues the same command without confirmation,
retry, or requeue. Enabling Studio alone never prompts or blocks unrelated work.
Cancellation stops the waiting execution; it cannot be revived by sign-in.

A retry is a **new run** with `parentRunId` set, never a state change on the
failed one. A failed run stays failed forever, which is what makes the
transcript of an attempt stable.
