---
title: Inbox
summary: Per-recipient notification items, the counts projection behind the badge, and the bulk read that replaces the per-item loop
---

The Inbox is one user's own notification list. Every item is written for exactly one recipient by the domain that raised it. Company items retain their membership fence; personal Calendar reminders belong to the account independently of any Company.

There is **no create endpoint**. Items are inserted inside the transaction of the
mutation that caused them and deduped on `(companyId, recipientUserId, dedupeKey)`,
so a retried source write never doubles a notification.

An item about a Task, its chat, a Run, an Approval, a Question or a result
belongs to that Task's Project ([ADR 0017](../adr/0017-project-access-scopes.md)).
It is raised only for a recipient who reads the Project, and every read, count
and bulk read leaves it out while its recipient cannot read the Project. Losing
access hides such items without deleting them; they come back, in their old
state, if access returns.

The migration that introduced Project access also addressed pending
Approval and Question items from task chats again: the people who may now
decide them got the item and every other open copy was read and archived
(see [Approvals](approvals.md)).

| `kind` | Raised when |
|---|---|
| `calendar_reminder` | An event or reminder reaches its reminder time; delivered to the organizer and invited active members |
| `calendar_update` | An invitation is added/removed, a guest changes their RSVP, or shared event details/occurrences are updated/cancelled; delivered to the affected active participants, excluding the actor |
| `task_assigned` | Someone else made you a task's assignee. The body names who, and the next step on your computer: set the agent up, start it, or reply in its chat when the task's status has no start. Nobody is told about a task they assigned to themself. It is read and archived for you once the task goes to another member, whoever moved it: only the assignee starts a task |
| `task_handoff` | A task of yours was handed to another member; files you never merged stay on your computer. When the handoff stopped your live run of it, the body says so. Also raised when someone moved your task to another agent (or into your own hands) and that stopped your run: the task stays yours, and the body says who moved it and how to run it again. Not raised for a move you made yourself. A handoff also reads and archives every `run_completed` item about the task's earlier runs, which nobody can review any more |
| `task_run_requested` | A colleague wrote in the chat of a task you are the assignee of; their message waits for your next run. The body names who wrote and the same next step `task_assigned` gives: set the agent up, start it, or reply in its chat when the task's status has no start. It is read and archived for you once the message stops waiting: a run carries it, it is deleted or removed (its writer left the company, for example), or the task is handed to another member |
| `blocker` | A task entered `blocked` — to its creator and its assignee |
| `run_completed` | An agent turn finished in a conversation — to the member whose computer ran it |
| `run_failed` | An agent turn failed and will not be retried — to the member whose computer ran it; also the turn an approval or question should have resumed, when its continuation is abandoned (to the conversation's owner); also a run stopped because the member it works for may no longer run tasks in its Project, while they still read it |
| `approval_request` | An agent needs someone allowed to decide it to approve a frozen action |
| `question_request` | An agent needs an eligible member to answer a question |
| `discussion_mention` | A new human message you should see in a channel or a direct message: every top-level post in a channel you keep at the `all` notification level, a reply in a thread you follow, every direct message, any message that mentions you or `@everyone`, and `@here` when you are online. One kind covers them all. The stored body names the sender ("Mai Owner wrote in a channel.") or says you were mentioned; it never quotes the message or stores the channel name. The deep link opens the channel, thread or direct message. The client names the channel when it shows the notice: it finds the deep link's `conversationId` in the channel list the viewer has loaded and turns "in a channel." into "in #sales." and "in a thread." into "in a thread in #sales."; a channel it has not loaded keeps the stored words. Native desktop banners additionally fetch the current message and conversation through authorized REST reads, showing the sender/context in the title and a plain-text preview in the body. Deleted/inaccessible messages are suppressed; network failures retain the generic receipt copy. The sender gets none |
| `project_access_granted` | People were added to a [Group](groups.md) and can now read a restricted Project they did not read, an admin adding themself included. One item per Project and change, to every person who manages that Project's access (by their own grant, a Group or their role, so owners and admins too), except the person who made the change and, when only one person can now see it, that person. Title "{Person} can now see {Project}" for one person, "{N} people can now see {Project}" for several; body "Added to {Group} by {Actor}."; deep link Team › Project access for that Project. Its entity is the Project itself, so it stays visible while the recipient reads the Project or their role holds `projects.manage`, and hides otherwise. A Group's grant on a Project raises none |
| `runtime_offline` | A stale sweep took a runtime offline while it still held in-flight runs — to the runtime's registrant only ([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)); a runtime whose registrant was deleted raises nothing; `entityType` is `runtime`, the deep link is the runtime detail, and every copy is read and archived automatically when the machine registers or beats again |

**The Inbox is the only place a member is told something happened.** A
conversation carries no unread count and no read watermark: a run reaching a
terminal state raises an item here, and following its `deepLink` is what reads
it. Two notification surfaces answering the same question from different data
would disagree the first time either was wrong — so there is one.

Calendar titles name the event and, for invitations or RSVP changes, the person:
`Mai Nguyen invited you to “Design review”` or `Alex Tran may attend “Design review”`.
Bodies show compact dates and local times with numeric UTC offsets; all-day
ranges use inclusive displayed dates. Updates name the changed details, and a
schedule change asks guests to respond again.

`runtime_offline` uses `{computer} went offline` and reports how many active runs
were interrupted, with a prompt to reconnect the computer and check the affected
work. It does not promise automatic retry, which is bounded by the Run policy.

`analytics_budget` names the Company or Agent whose monthly budget reached the
80% or 100% threshold. The 100% notice explicitly says work continues: budgets
are soft alerts. Existing threshold dedupe and usage-read recipients apply.

`huddle_missed` names the caller, with `Open Chat to follow up.` as its stored
body. The deep link includes `invitationId`. Native desktop presentation fetches
the recipient's invitation history and adds Company/source context only when
currently readable; it never stores a private source title in the receipt.
Incoming Huddle invitations use the same caller/context format, fetched through
recipient REST reads, and use the desktop native bridge with Chat preferences.

`run_failed` is raised only when the server will *not* retry. An intermediate
failure that the retry policy will pick up is not something a human needs to
act on, and the run list already shows it.

A run the server cancels because the member it works for lost
`project.tasks.execute` in its Project ([ADR
0017](../adr/0017-project-access-scopes.md)) raises one too, to that member,
in the transaction that cancels it, and presents a desktop toast after
commit. Only a member who still reads the Project is told: they lost the
right to run tasks there, not to see the work. Its `entityType` is `run`,
its dedupe key `run:{runId}:project_access_revoked`, its title "Run stopped:"
and the task's label and title, and its body names the agent: "Eli stopped:
your access to this work no longer includes running tasks. Ask someone who
manages this work to let you run tasks." It never names the Project. A
member who lost `project.read` is told nothing, because the Project is
hidden from them now.

An abandoned approval or question continuation raises one too, because to the
owner that is what happened: the agent's next turn never ran and will not be
retried. It goes to the member the continuation would have run for — a task's
current assignee, a direct conversation's creator (ADR 0014) — in the
transaction that abandons it, and like every `run_failed` item it raises a
desktop toast after commit. Its `entityType` is the `approval` or `question`,
its dedupe key `{entityType}:{id}:continuation_abandoned`, and its deep link
opens the conversation at the run that asked. The body says why the agent
stopped and names only a step the member can take — for the commonest case,
"Set Eli up on your computer, then reply in the task's chat." It never says
to run the task again: a task waiting on a request stays `in_progress`, where
there is no Run button, and a reply in its chat is what starts the agent
again. Only a cause
the member can act on raises it: the agent is not set up on their computer or
is archived, or the task failed, waits on a task it depends on, or is in an
archived project. A handoff, a task a member marked done or cancelled, or an
archived conversation raises none, because a member chose it, and neither
does a channel tag, whose channel already shows it blocked, or an owner who
is no longer an active member.

A run that ended to wait for a person is never "ready to review": a Question or
Approval it raised is still pending, or its continuation has not started. It is
the same rule that keeps the task `in_progress`. When the recipient holds that
request's own `question_request` or `approval_request` item, read or not, that
item asks for the answer, the run raises no `run_completed`, and the
continuation run's terminal item reports the result. When they do not, the run
raises `run_completed` saying it is paused for approval or for an answer. An
`approval_request` goes only to the members who may decide it, so a plain
member's task run can wait on an Approval they never receive.

A terminal run item has one recipient: the member whose computer ran the run.
For a task run that is its authorizer, always the task's assignee
([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)), never a
colleague whose queued message the run read or the member who created the task.
A direct conversation or a schedule is its creator's. A channel tag's run
raises no item: its answer lands in the channel thread.

Terminal run items name the agent and the task's displayed label
(`{taskPrefix}-{number}`, never its `key`) and title, direct conversation
title, or schedule name. Task-run completion says the result is ready to review;
it does not claim the task itself is done. A run paused for a person says so
instead. Hybrid tasks retain the founder-action instruction. A `run_failed`
body also says why: the run's provider-aware
`failureMessage` sentence, then the bounded `failureDetail` line introduced by
"Codex said:" / "Claude said:" for provider failures or "Details:" otherwise.
Notification copy otherwise uses these display labels only, never prompts,
agent instructions, or raw provider output. Existing items keep
their original frozen copy.

There is no `severity` field. Urgency is a fixed projection of the kind —
`blocker` is always a warning, `run_completed` never is — so serving it as a
second field put one fact on the wire twice and let the copies disagree.
Branch on `kind` for behaviour and for styling; the vocabulary is closed and
validated on write.

Company routes are gated on **member**. Human-only `/me/inbox` routes aggregate
personal items and currently accessible Company items. The recipient is always
the caller: there is no parameter that reads another member's Inbox, and no
company-wide Inbox exists.

## Call budget

| Action | Requests |
|---|---|
| Notifications screen on mount | **2** — the list and the counts |
| Live updates | `inbox.created` and `inbox.updated` carry the item and refreshed Company counts; account totals refetch on each frame; initial load, reconnect and periodic reconciliation use HTTP |
| The blockers-only view | **1** — `kind=blocker` (was: page everything, filter client-side) |
| Mark one conversation's notifications read | **1** |
| Mark everything read | **1** |

Two on mount is already the right number and no composite is proposed for it.
Opening a conversation uses the scoped bulk write below, so it never pages the
whole Inbox or serially mutates individual rows.

An Inbox row is a frozen record of an event, not a live view of its subject.
`deepLink` is presentation — where to send a click — and the row carries no
second machine-readable reference beside it: a structured subject ref was
served for a while and nothing ever read it, because every consumer either
follows the link or renders the frozen `title`/`body`.

---

## List inbox items

```
GET /api/v1/companies/{companyId}/inbox?status=unread&kind=blocker&limit=50
```

The caller's own items, newest first.

**This list pages backward.** The cursor names the oldest row you already hold
and the next page is what sits strictly before it, ordered by
`(createdAt, id)` descending — the feed grows at the end you started from, so a
new item never shifts a page boundary you have already crossed. `nextCursor` is
`null` only when a further row genuinely does not exist; the page is fetched one
row wider than `limit` to know that.

The seek predicate is
`(created_at, id) < (cursor_created_at, cursor_id)`. The UUID tie-breaker is
required because multiple items can share a creation timestamp.

This high-volume feed returns `{items, nextCursor}`; bounded entity
collections do not carry a cursor.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |
| `status` | query | no | `active` (default), `unread`, `archived`, or `all` |
| `kind` | query | no | One of the kinds above. One value, not a list |
| `limit` | query | no | 1..100, default 50 |
| `cursor` | query | no | Opaque cursor from a previous `nextCursor` |

`status` selects a view, not a sort: `active` is everything unarchived, `unread`
is the unread half of that, `archived` is only archived items, and `all` is the
true union. Unknown, repeated, or empty-valued query parameters are `422`.

Filters combine with AND. `status` and `kind` are both optional and independent,
so `status=unread&kind=blocker` is the unread blockers and nothing else.

> **Added.** `kind`. The blockers-only view was served by fetching every page and
> filtering in the client, which also made any count taken from that view wrong
> the moment it spanned more than one page. `kind` was already stored on every
> row and simply could not be asked for.

> **Changed.** `status=all` used to mean "all *unarchived*", so no value returned
> the union and a client wanting full history made two calls and merged them.
> The default view is now named `active` for what it is, and `all` means all.

| Field | Type | Notes |
|---|---|---|
| `id` | string | uuid |
| `companyId` | string | uuid |
| `kind` | string | Closed vocabulary, validated on write. See the table above |
| `title` | string | Up to 160 characters |
| `body` | string | Up to 2048 bytes |
| `deepLink` | string | **Absent when there is none** — see below |
| `unread` | boolean | `readAt === null && archivedAt === null`. The badge predicate |
| `readAt` | string \| null | |
| `archivedAt` | string \| null | |
| `createdAt` | string | |
| `updatedAt` | string | |
| `revision` | number | Guards every single-item mutation |

The discriminator is `kind` on the wire even though the Go field is `Type`;
`kind` is what every other discriminator in this API is called — conversation
`kind`, message `kind`, notification `kind` — and it stays that way here.

> **Not added: `subject`, `subjectStatus`, and `actor`.** All three were drafted
> for this revision and all three are cut, because nothing on the server can
> produce them. `inbox_items` has no subject or actor column, and the writer
> does not supply one: the task id survives only inside `deepLink` and inside
> the `dedupeKey` string, and the actor is passed to the realtime envelope and
> then dropped. Contracting them would have promised three required fields that
> every read would have had to invent. They need a migration and a writer
> change, in that order, before they can be contracted.

> **Added.** `unread` is the exact predicate behind `unreadCount`: neither read
> nor archived. Two nullable timestamps are not the same question, and the
> conjunction is the one clients get wrong in the direction that matters —
> archiving an unread item lowers the badge without touching `readAt`, so a
> client deriving unread from `readAt` alone drifts from the server's own count
> and never converges.

> **Removed.** `recipientUserId` and `severity`. The recipient is always the
> caller on these routes and the socket-scope recipient on the realtime
> frames, so the field could only ever repeat who is asking; severity is
> kind-derivable, above.

`deepLink` is a **root-relative in-app path** the client resolves against its own
base: it always begins with a single `/`, is never absolute or protocol-relative,
never contains a backslash, and is at most 1024 characters. Task items point at
the task workspace, `/app?desktop_search=1&taskId={taskId}`. It is **omitted**,
not `null`, when the raising domain had nowhere to send the reader — this is the
one field on the resource whose absence means "there is none" rather than
"unchanged", which is why it carries `omitempty` and is documented here.

```json
{
  "items": [
    {
      "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
      "kind": "task_assigned",
      "title": "Task assigned",
      "body": "Alice Owner assigned you “Draft the launch checklist”.",
      "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
      "unread": true,
      "readAt": null,
      "archivedAt": null,
      "createdAt": "2026-08-16T09:12:44.512883Z",
      "updatedAt": "2026-08-16T09:12:44.512883Z",
      "revision": 1
    },
    {
      "id": "0197f58a-4411-7c62-b930-5e1d2a7f6c08",
      "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
      "kind": "blocker",
      "title": "Task blocked",
      "body": "“Draft the launch checklist” is blocked.",
      "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
      "unread": false,
      "readAt": "2026-08-16T10:03:11.204118Z",
      "archivedAt": null,
      "createdAt": "2026-08-16T08:41:09.883210Z",
      "updatedAt": "2026-08-16T10:03:11.204118Z",
      "revision": 2
    }
  ],
  "nextCursor": "AQAGWSYD7ux4AZf1ikQRfGK5MF4dKn9sCA"
}
```

`items` is always an array, `[]` on an empty page.

The cursor is the standard opaque `(createdAt, id)` seek boundary. The UUID
breaks timestamp ties; clients round-trip the value and do not construct it.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, not an active member, or a malformed `companyId` |
| 422 | `validation_failed` | Unknown, repeated, or empty-valued query parameter; `status` or `kind` outside the vocabulary; `limit` outside 1..100 or not a plain integer |
| 422 | `invalid_cursor` | Malformed cursor or unsupported encoding version |
| 500 | `internal_error` | Unexpected |

---

## Get the unread counts

```
GET /api/v1/companies/{companyId}/inbox/counts
```

The counts projection alone, for the badge.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |

**This endpoint accepts no query parameters, and anything in the query string is
`422`.** `unreadCount` counts items that are neither read nor archived.
`countsVersion` is a monotonic ordering token in epoch milliseconds, or `1`
for a recipient with no items. It is the newest of two times: the last write
to any of the caller's items, including items they cannot see right now, and
the last access change of a Project those items name. So losing a Project, or
leaving a channel, hides items without lowering it, and the access change
itself raises it (ADR 0017). Any access change on such a Project raises it,
including one that is not the caller's own, such as a grant to someone else
on a Project they no longer read. It says only that something moved on a
Project their items name, never what. This timing is accepted: the change
time is kept per Project, not per person. Compare it; never do arithmetic on
it. It is what lets a client discard the `counts` of a realtime frame older
than the state it already applied. It is not an optimistic-concurrency token
— nothing accepts it back — which is why it is not named `revision`.

```json
{ "unreadCount": 12, "countsVersion": 1786871564512 }
```

> **Changed.** This handler never parsed its query string, so unknown parameters
> were silently ignored here while the list rejected them. Silent acceptance is
> the failure mode [conventions.md](conventions.md) names: adding a parameter the
> server does not know is a breaking change for that client, not a no-op.

> **Changed.** A caller whose membership is no longer active now reads
> `404 company_not_found` here and on every Inbox write below. It used to answer
> the Inbox's bare `not_found`, which said "item missing" about a request that
> names no item.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 422 | `validation_failed` | Any query parameter — this endpoint accepts none |
| 500 | `internal_error` | Unexpected |

---

## Get one inbox item

```
GET /api/v1/companies/{companyId}/inbox/{itemId}
```

One item addressed to the caller. **Archived items are returned normally** — the
archive filter belongs to the list, not to identity.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |
| `itemId` | path | yes | Item uuid |

```json
{
  "id": "0197f58a-4411-7c62-b930-5e1d2a7f6c08",
  "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
  "kind": "blocker",
  "title": "Task blocked",
  "body": "“Draft the launch checklist” is blocked.",
  "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
  "unread": false,
  "readAt": "2026-08-16T10:03:11.204118Z",
  "archivedAt": null,
  "createdAt": "2026-08-16T08:41:09.883210Z",
  "updatedAt": "2026-08-16T10:03:11.204118Z",
  "revision": 2
}
```

`title` and `body` are frozen at write time: this row still says the task is
blocked however the task has moved since. The notification is a record of an
event, not a live view, and the row carries nothing that tracks the subject — so
a client that needs to know whether the blocker still matters reads the task
behind `deepLink`.

> **Changed.** A malformed `itemId` is `404 inbox_item_not_found`, not `422`. A
> path id that cannot name a row is indistinguishable from one that names
> nothing, and this is how every other resource in the API answers it — the
> `422` here was a local exception to the 400/422 split rather than a rule. The
> mutation below answers the same way.

> **Changed.** This handler never parsed its query string either, so a stray
> parameter was silently ignored here while the list rejected it. It is now
> `422 validation_failed`, like every other route.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `inbox_item_not_found` | Unknown item, malformed `itemId`, or an item addressed to another member |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 422 | `validation_failed` | Any query parameter — this endpoint accepts none |
| 500 | `internal_error` | Unexpected |

---

## Change one item's read or archived state

```
POST /api/v1/companies/{companyId}/inbox/{itemId}/read
{ "expectedRevision": 1 }
```

Four verbs share the shape: `read`, `unread`, `archive`, `restore`. `read` and
`unread` set and clear `readAt`; `archive` and `restore` set and clear
`archivedAt`. **The two axes are independent — archiving does not mark an item
read**, and an archived item leaves `unreadCount` because the count excludes
archived rows, not because its `readAt` moved.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |
| `itemId` | path | yes | Item uuid |
| `mutation` | path | yes | `read`, `unread`, `archive`, or `restore` |
| `expectedRevision` | body | yes | The `revision` the caller last saw; 1..9007199254740991 |

`expectedRevision` is required: `{}` is `422`, not an unconditional write. The
body must be a JSON object with `Content-Type: application/json` and is capped at
4 KiB.

A mutation asking for the state the item is already in — `read` on a read item,
`restore` on an unarchived one — is a **no-op that succeeds**: `200`, unchanged
`revision`, and no realtime event.

The response **is the item**, with the recipient's unread counters and the
write receipt beside it — the counters ride along so a badge updates without a
second read.

```json
{
  "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
  "kind": "task_assigned",
  "title": "Task assigned",
  "body": "Alice Owner assigned you “Draft the launch checklist”.",
  "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
  "unread": false,
  "readAt": "2026-08-16T10:03:11.204118Z",
  "archivedAt": null,
  "createdAt": "2026-08-16T09:12:44.512883Z",
  "updatedAt": "2026-08-16T10:03:11.204118Z",
  "revision": 2,
  "counts": { "unreadCount": 11, "countsVersion": 1786874591204 }
}
```

The counts in that body are read inside the same transaction that performed the
write, so they are at least as fresh as anything a follow-up could return. **Never
follow a mutation with `GET .../inbox/counts`.**

`counts` keeps its own key rather than being spread as `unreadCount` and
`countsVersion` beside the row's fields. It is not a group of the item's own
scalars: it is the recipient's projection, it has its own endpoint that
returns it at the top level, and the `inbox.created` / `inbox.updated` frames
carry exactly these bytes beside their item — so a client writes the value
into one cache slot whole, and one wire type serves every place it appears.

> **Changed.** The row is now **flat at the top level**, as
> [conventions.md](conventions.md#a-response-is-the-thing-itself) requires. It
> used to sit under `item`, wrapped in a key named after itself, on the argument
> that a flattened row would collide with the counts projection — but the
> projection keeps its own key, so nothing collides, and the wrapper cost every
> caller an unwrapping step to avoid a collision that was never possible.

> **Changed.** The item is flat at the top level rather than under an `item`
> key, so it lands in the same cache slot the list filled.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Body is not a JSON object, or carries unknown fields |
| 401 | `authentication_required` | No credential |
| 404 | `inbox_item_not_found` | Unknown item, malformed `itemId`, or an item addressed to another member |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 409 | `inbox_item_revision_conflict` | `expectedRevision` did not match the stored `revision`. Re-read the item, re-apply, write again |
| 413 | `request_too_large` | Body over 4 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `expectedRevision` missing or outside 1..9007199254740991, or `{mutation}` outside the four verbs |
| 500 | `internal_error` | Unexpected |

---

## Mark one conversation's notifications read

```
POST /api/v1/companies/{companyId}/inbox/read
{ "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55" }
```

Marks every unread, unarchived item raised about that conversation read, in one
transaction. Archived items are untouched.

**Opening a conversation should cost one Inbox request, not two hundred.** This
is the endpoint that replaces "page the unread Inbox, then `POST` once per
match, then re-read and retry on every `409`" — the worst N+1 in the product.
There is no `expectedRevision` here and there cannot be a conflict: the write
is a set operation over rows the caller owns, and marking an already-read row
read is not a change.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |
| `conversationId` | body | yes | Conversation whose notifications to mark read |

There is no `taskId` form. A conversation is the unit the chat client opens and
the exact association stored on notification rows, so the write accepts only
`conversationId`.

```json
{
  "items": [
    {
      "id": "0197f58a-4411-7c62-b930-5e1d2a7f6c08",
      "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
      "kind": "blocker",
      "title": "Task blocked",
      "body": "“Draft the launch checklist” is blocked.",
      "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
      "unread": false,
      "readAt": "2026-08-16T10:31:52.771904Z",
      "archivedAt": null,
      "createdAt": "2026-08-16T08:41:09.883210Z",
      "updatedAt": "2026-08-16T10:31:52.771904Z",
      "revision": 3
    }
  ],
  "counts": { "unreadCount": 9, "countsVersion": 1786876312771 }
}
```

`items` is the affected row set itself, not a page — there is no `nextCursor`,
and no separate change counter: **the set's size is the count**. A second
identical call answers `items: []` and raises no event. A conversation with
nothing unread is the same answer, not an error — but a `conversationId` the
caller cannot see is `404`, so a typo never reads as "nothing to mark".

Every changed row also arrives as an `inbox.updated` frame for the exact
Company-user audience — each frame carrying the refreshed `counts` beside the
item — so the caller's other sessions converge without a read; the set is
bounded by one conversation's unread notifications.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Body is not a JSON object, or carries unknown fields |
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 404 | `conversation_not_found` | `conversationId` names a conversation the caller cannot see |
| 413 | `request_too_large` | Body over 4 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `conversationId` missing or not a uuid, or any query parameter |
| 500 | `internal_error` | Unexpected |

---

## Mark everything read

```
POST /api/v1/companies/{companyId}/inbox/read-all
```

Marks every unread, unarchived item in the company's Inbox read, in one
transaction. Takes no request body and no query parameters; archived items are
left alone.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company the Inbox belongs to |

```json
{
  "items": [
    {
      "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
      "kind": "task_assigned",
      "title": "Task assigned",
      "body": "Alice Owner assigned you “Draft the launch checklist”.",
      "deepLink": "/app?desktop_search=1&taskId=0197f3b1-88c0-7a12-9c44-2b7d9e0f1a33",
      "unread": false,
      "readAt": "2026-08-16T10:44:18.330517Z",
      "archivedAt": null,
      "createdAt": "2026-08-16T09:12:44.512883Z",
      "updatedAt": "2026-08-16T10:44:18.330517Z",
      "revision": 2
    }
  ],
  "counts": { "unreadCount": 0, "countsVersion": 1786877058330 }
}
```

> **Changed.** This call used to answer counts and nothing else, which left a
> client holding a cached list to clear `readAt` on its own rows by guesswork.
> It now returns the rows it changed, newest first, with their bumped
> `revision`s, so a client holding a cached list does not have to clear
> `readAt` on its own rows by guesswork.

> **Changed.** A stray query parameter was ignored on this route too; it is now
> `422 validation_failed`.

When nothing was unread the call short-circuits: `items: []`, current counts,
no event.

**No per-item frames are raised.** This is the one place the resource
deliberately withholds row events: an unread backlog has no upper bound, and a
frame per row can exceed the 256-frame client send buffer and evict the very
session that asked for the write (see
[realtime.md](realtime.md#transport-limits)). The writer already holds the
rows and the refreshed counts from the receipt above; another session
converges on its next read.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, or the caller is not an active member |
| 422 | `validation_failed` | Any query parameter — this endpoint accepts none |
| 500 | `internal_error` | Unexpected |

---

## Realtime Events

Every Inbox event uses the exact recipient's **User audience**, across companies. Routing
is server-owned, so one member's notifications cannot reach another member's
connection and the browser sends no subscription command.

| Event | Scope | Carries | Kind |
|---|---|---|---|
| `inbox.created` | User | `InboxItem` flat with the recipient's refreshed `counts` beside it — the identical shape the mutation responses use | Cache |
| `inbox.updated` | User | The authoritative updated row, with `counts` beside it | Cache |
| `notification.issued` | User | `{notificationId, kind, title, body, deepLink?}` | UI hint — transient toast |

**The counts ride item frames; initial load and recovery still use HTTP.** There
is no separate counts-only event: the number changes exactly when an item
does, so the frame that delivers the item delivers the badge. The one
exception is a Project access change, which hides or shows items without an
Inbox frame: `project.access_changed` tells the client to read the counts
again, and that read carries a higher `countsVersion`. Apply
`counts` directly, and drop any frame whose `countsVersion` is not greater
than the one already applied — the item half of the frame still applies by
its own `revision`.

Item events carry the exact `InboxItem`; server-only changed-field
bookkeeping is not serialized.

`notification.issued` rides alongside a newly created item as a best-effort
toast. It is presentation noise, not a record: the Inbox row is the durable
truth, and a missed toast costs nothing because the item is already in the
list. `deepLink` is optional; styling derives from `kind`, on the toast as on
the row.

## Item lifecycle

An item has two independent axes — read and archived — and no state is terminal.
Only the top-left cell is counted in `unreadCount`, and it is exactly the cell
where `unread` is `true`; the bulk writes above move rows along the top edge
only.

```
   created by the raising domain, never by a client
                     │
                     ▼
        ┌────────────────────┐    read    ┌────────────────────┐
        │  unread · active   │───────────▶│   read · active    │
        │  counted in        │◀───────────│                    │
        │  unreadCount       │   unread   └────────────────────┘
        └────────────────────┘                  │        ▲
              │        ▲                        │        │
      archive │        │ restore        archive │        │ restore
              ▼        │                        ▼        │
        ┌────────────────────┐    read    ┌────────────────────┐
        │ unread · archived  │───────────▶│  read · archived   │
        │                    │◀───────────│                    │
        └────────────────────┘   unread   └────────────────────┘
```

Items are never deleted by the API; `archive` is how a recipient retires one.
Every transition bumps `revision` and emits `inbox.updated` with the
recipient's refreshed `counts` beside the row — whether the transition crossed
the unread boundary is visible in the counts themselves.

## Account Inbox and Calendar reminders

`GET /api/v1/me/inbox` and `/counts` include the caller's personal reminders and
items from all currently accessible Companies. The same item GET, `/{itemId}/{mutation}`
POST and `/read-all` POST contracts are available under `/me/inbox`, restricted to
human sessions. Company routes remain scoped to one Company. `companyId` on an
Inbox item is nullable for account-owned `calendar_reminder` items only.

Calendar reminders use existing read/archive mutations, recipient-private realtime
frames and Electron native notifications. Their deep link selects the event and
occurrence date. Account counts are refetched on realtime changes; Company count
sidecars must never be treated as account totals.
