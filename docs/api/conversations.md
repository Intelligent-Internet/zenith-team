---
title: Conversations
summary: Conversations, messages, attachments, read state, and the run queue
---

A Conversation is **one ordered stream**. Agent direct, Task and Schedule
conversations use the Run-backed protocol below. Human DMs and private channels
use the separate [human discussion protocol](#human-discussions).
Every Run-backed item — the user's words, the agent's answer, and
the run detail in between — is a row of the same sequence, immutable, carrying a
monotonic per-conversation `seq`. Attachments are uploaded separately and
referenced by id when a message is sent.

What a surface shows is decided by the item's `type`, not by which table it came
from. This file's reads carry the two chat types, `user_message` and
`agent_text`; the reasoning, tool-call, tool-result and error types are filtered
out here and read through [the Conversation transcript](#read-the-provider-transcript)
instead. One consequence worth internalizing: **`seq` is shared**, so two consecutive messages on this
surface are usually not consecutive numbers, and a gap is not a lost message.

Every Run-backed item also carries a non-null `runId`. The user's words belong
to the run that consumed them; the agent's answer belongs to the run that wrote
it. That is why a conversation with nobody to answer cannot take a message at
all — see [Send a message](#send-a-message).

An author is `user` or `agent`, using the shared concrete `ActorRef` vocabulary.
System activity has its own nullable actor shape because it names no concrete
person or agent. The provider word `assistant` belongs to the CLI protocols the
adapters speak and never reaches the wire.

Agent direct chats are **creator-private**: only the user who created one can read it,
list it, or write to it, even though every member of the company shares the
agent. A task conversation is visible to the readers of its task's Project
(`project.read`, [ADR 0017](../adr/0017-project-access-scopes.md)); posting
in it needs `project.tasks.write` there. A task chat detached before Project
access existed has no Project and stays visible to every active member.

Send-retry, complete-suffix recovery and provider-history snapshots are
Planned; the served protocol below does not include them. A private chat
controls who can see it on the server; its Agent still executes on the
assigned machine in the managed shared Company workspace. It does
not create a per-person filesystem sandbox or authorize a direct-workdir.

Reads are company-scoped in the path. The write and sub-resource routes that
hang off a conversation take only a conversation id and resolve the company
server-side from the caller's membership. A conversation the caller may not
read is `404`, identical to one that does not exist.

## Chat model and thinking

`GET /conversations/{conversationId}/model-settings` reads the effective Agent,
provider, model, thinking level, revision and `canEdit` for a direct or Task chat.
`PUT` on the same path accepts `agentId`, `expectedRevision`, `model` and
`thinkingLevel`; both choices are required and may be explicitly `null` to omit
that provider override, without falling back to the Agent. A resumed provider
session may retain its previous choice when no override is sent. A stale
revision or a changed Task Agent returns `409`.

Direct chats copy the Agent defaults at creation. New Task chats copy them at
the first Run or settings edit. Subsequent Agent edits affect new chats, not
existing chat choices. Every enqueued Run freezes its own choices in its Agent
brief; changes during execution apply to the next Run, and retries keep the
original brief. A deferred queue item takes the chat's choices when materialized
into a Run. Task handoffs to another Agent use that Agent's own chat settings.

Only the creator may edit a direct chat. Task readers may inspect its settings;
only the current assignee with `project.tasks.execute` may edit them. Updates
publish `conversation.updated` for existing realtime cache invalidation.
Channel and Schedule execution continue to use Agent defaults.

The response also includes `changes`, the Conversation's committed model-change
history in chronological order. Each entry carries its Agent, previous and new
model (null means provider default), timestamp, and `afterSeq`, the last committed
Conversation item when it was saved. The update and history entry commit together;
thinking-only edits, unchanged models and rejected writes add no entry. Clients
render these entries as timeline notices, never as messages sent to the provider.

## Human discussions

The [OpenAPI conversation contract](spec/conversations.yaml) serves human-only,
Company-scoped routes under `/api/v1/companies/{companyId}`:

- `/human-conversations` finds/creates the unique DM participant set and lists the caller's
  DMs; its `/{conversationId}` resource reads the current summary and members.
  Supply either `peerUserId` for one person or `peerUserIds` for 1–49 people,
  excluding yourself. Include `agentIds` for up to 20 Agents, with or without
  other people. The creator is always included. Agents appear in the roster and
  can be invoked with structured `@Agent` mentions. Only tagged Agents run;
  untagged messages remain human discussion. Each Agent has its own private
  execution conversation and FIFO queue for this group, and publishes its final
  answer into the triggering thread. As in channels, a tag runs on the tagger's
  own setup of that Agent on one of their computers (ADR 0014); tagging an Agent
  the sender has not set up is refused with `409 agent_binding_required`.
  Tagging an Agent that is not in the chat or channel is refused with
  `422 discussion_agent_required`: add the Agent first.
  New chats deduplicate by the selected participant set. Group titles use all other active human and Agent participants’ names, excluding
  revoked or unavailable members. Each name is bounded individually; the complete
  comma-separated title is returned and the client truncates it visually with CSS. Reopening the
  same set is idempotent regardless of selection order or which participant opens it,
  until the group is expanded by invitation.
  Remaining group members can keep chatting while another member is inactive;
  only currently eligible participants regain grants when a group is reopened.
- `POST /human-conversations/{conversationId}/members` invites people (`memberUserIds`)
  and/or Agents (`agentIds`) into an existing group DM. Any active participant with
  `conversations.discuss` can invite eligible Company members; channel-management
  permission is not required. Pair DMs remain immutable. New members receive retained
  message/file history and start unread tracking at the current head. Invitations are
  additive and idempotent. Each changed batch appends one ordered `discussion_system`
  item (`members_added`, inviter ID, added human/Agent IDs); unchanged retries append
  nothing. The client renders invitations centered in the timeline with member avatars.
  Invitations have limits of 50 people and 20 Agents including revoked
  identities. Expanded groups keep their ID and history but leave canonical create/open
  lookup: selecting the old roster opens a separate private chat. Separate histories
  are never merged, even when they have the same current roster.
- The Agent status, activity and retry endpoints under
  `/channels/{conversationId}/agent-invocations` and
  `/channels/{conversationId}/agents/{agentId}/activity` also accept group DM
  conversation IDs. They require current membership in that conversation.
  Every member reads the activity of their own Runs only: a tagged Run works
  for the person who tagged the Agent, with their access, so nobody else
  reads its requests or tool output, the Agent's creator included. The
  roster's `canViewDetail` is `true` for every viewer, a member who never
  tagged the Agent reads an empty list, and each invocation names its
  `callerUserId`. These shared invocation endpoints retain their existing
  paths.
- `/channels` creates/lists private groups. Its `/{conversationId}` resources
  read/update metadata, add/remove human or Agent grants, leave, archive and restore.
  `view=manage` requires `channels.manage` and returns management metadata only.
- `/conversations/{conversationId}/discussion-messages` sends root text and reads
  bounded history. Sends use a UUID request key and return a committed receipt
  without Run fields; exact retries replay the original receipt's current item.
- `/conversation-states/query` reads up to 50 unique IDs, omitting inaccessible
  resources. `/conversations/{conversationId}/discussion-state` updates personal
  pin/hide state with its own revision.

Channel owners have content access without joining; sending does not enroll them.
Pin/hide enrolls an owner in the same transaction. Admins outside a channel have
management access and must join for content. DM access always requires the
participant's current grant. Agent grants authorize retained history, with no Agent
history API/tool or invocation. Legacy transcript, message and context-snapshot
routes reject human discussion types.

Reads use private/no-store responses and current ACL. Message pages are at most
100 items and 512 KiB; request bodies are at most 128 KiB and serialized message
bodies at most 64 KiB. Treat every returned cursor as opaque. Revalidate state
after reconnect, replace loaded windows using their refresh cursors, and drain
the newer cursor even after an empty latest page. A revoked resource returns 404;
a still-authorized cursor with stale membership/grant returns
`discussion_cursor_expired` (409). Bodyless realtime hints only invalidate state.

Human DMs and private channels are available to every Company without a feature
flag. Current membership, role permissions and conversation grants govern access.
Discussion chat supports typed human/agent mentions, `@everyone`/`@here`, attachments,
threads and durable notifications. Notifications target DM peers, thread followers
and explicit mention recipients; links open the exact message and thread. Active
channel/thread views suppress presentation of matching notifications. Membership
generations fence notification visibility after a member is removed and re-added.

`POST /companies/{companyId}/member-presence` reports recent client activity and
reads up to 50 requested members. Active heartbeats expire after 90 seconds;
manual Away is stored per Company membership and overrides every device. Clients
report at 45-second intervals while focused, with inactivity after five minutes.
`@here` uses the same shared presence state. Agents are not human presence entries. See the
implementation status and validation.

## Call budget

**An open chat panel is one request for its current window.** The conversation
detail composite includes messages, caller state, run state, queue state, and
the answering agent. The sequenced messages route remains available for
bounded catch-up after reconnect.

Everything that panel needs now arrives from
`GET /companies/{companyId}/conversations/{conversationId}` — including who is
answering, whether their runtime is online, and why the last run failed — and
everything that changes afterwards arrives over the socket carrying the same
DTOs, see [realtime.md](realtime.md).

**A conversation list is one request too.** The rows carry the agent's name and
online state, so the list no longer needs the agent roster loaded beside it to
render.

---

## Read the provider transcript

```
GET /api/v1/companies/{companyId}/conversations/{conversationId}/transcript
```

This is one provider-item stream for the whole Conversation, not one request
per Run. Rows are ascending by the Conversation's canonical `seq` and each row
carries the `runId` that produced it. `reasoning`, `toolCall`, `toolResult`,
`text`, and `error` parts remain groupable by Run without putting a Run filter
in the route.

`limit` is 1..200 (default 200). `after_seq` returns provider items with a
strictly greater `seq`; continue with the final returned row's value. The
filtered stream may skip positions occupied by `user_message` rows, so do not
increment the cursor or treat a gap as loss. `throughSeq` is the newest
provider-item position in the Conversation when the page began and `hasMore`
says the bounded page stopped short.

The Conversation head is a paging bound, not a replacement for per-Run
integrity. Reconciliation requires that Run's exact `transcriptThroughSeq`,
but merely observing the largest seq does not prove that earlier items were
received. Read an authoritative suffix from the last verified scan boundary;
do not advance that boundary from a live frame alone. The current client
still takes a head-only shortcut here; correcting it is Planned. See
[Runs](runs.md#read-a-conversations-provider-transcript) for the structured
item shape and terminal-proof relationship.

---

## List conversations

```
GET /api/v1/companies/{companyId}/conversations
```

The calling user's own direct chats, newest activity first (descending
`updatedAt`, ties broken by descending id). Gate: **member**, plus the
creator-private filter — two members of the same company see disjoint lists.
The complete matching collection is returned in one response. `updatedAt` is a
mutable display order, not a stable cursor boundary.

**Archived chats are excluded by default.** Pass `include_archived=true` to
include them when browsing or restoring history. They remain readable by id.
Each row includes a nullable `archivedAt` timestamp.

Each row is the conversation's own fields flat, the caller's `pinnedAt`, and
the shared activity `summary` beside them. `summary` is the **same** object
the `conversation.created` and `conversation.updated` events carry —
`{updatedAt, lastMessage, runs}` — so a frame is written into this slot
unchanged; row and event are one shape. `summary.lastMessage` carries a
**content snippet**, so a list renders a preview line without a second
request.

Run detail is **counted, not embedded**. `summary.runs` uses the API's one
[run-counting vocabulary](conventions.md#counting-runs) — `queuedCount`,
`activeCount`, and the nested `latest` run — replacing the up-to-20 nested run
objects a row used to carry just to render a badge. A client that needs the
runs themselves asks the runs endpoint for that conversation.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company that owns the conversations |

```json
{
  "items": [
    {
      "id": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
      "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
      "kind": "direct",
      "taskId": null,
      "agent": {
        "id": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
        "name": "Ada",
        "title": "Head of Analytics",
        "runtimeAvailable": true
      },
      "title": "Weekly metrics",

      "pinnedAt": null,

      "summary": {
        "updatedAt": "2026-08-15T16:02:57.903118Z",
        "lastMessage": {
          "seq": 41,
          "content": "Revenue is up 4% week over week; the drop on Tuesday was a tracking gap.",
          "type": "agent_text",
          "authorKind": "agent",
          "createdAt": "2026-08-15T16:02:57.903118Z"
        },
        "runs": {
          "queuedCount": 0,
          "activeCount": 1,
          "latest": {
            "runId": "00000000-0000-0000-0000-0000000012d5",
            "agentId": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
            "status": "running",
            "createdAt": "2026-08-15T16:01:40.220900Z",
            "failureReason": null,
            "attempt": 1,
            "maxAttempts": 2
          }
        }
      },

      "createdAt": "2026-08-11T09:14:22.481113Z",
      "updatedAt": "2026-08-15T16:02:57.903118Z",
      "revision": 12
    }
  ]
}
```

> **Added.** `agent`, replacing the bare `agentId`. Every row in this list is a
> chat *with someone*, and a UUID does not render. The old row forced a client
> to hold the entire agent roster in memory to draw fifty names, and to hold the
> runtime list too if it wanted the online dot. `runtimeAvailable` is
> denormalized here exactly as it is onto [the agent itself](agents.md): it is
> what tells a user their message will sit queued *before* they type it.
> `null` only when the agent has been deleted.

> **Removed.** `state` on a message. It admitted `pending`, `failed` and
> `cancelled` back when the agent's reply was a placeholder row that a terminal
> transition later filled in. There is no placeholder now: an item is written
> once, when its content exists, so there is no state to move. A turn that
> failed is read from `runs.latest.failureReason`, which this file's
> conversation read already carries.

> **Changed.** `kind` is now `type`, over the two chat values `user_message`
> and `agent_text`. `run_output` and `no_response` are gone with the second
> write that produced them: agent text is an ordinary item of the stream, and a
> run that answered nothing leaves no item rather than an empty one.
> [ADR 0008](../adr/0008-run-and-activity-history.md) records the one-stream
> decision.

The caller's `pinnedAt` and `archivedAt` timestamps match the state endpoint
and `conversation.read` event. Active pins exclude archived chats; restoring a
conversation preserves its previous pin.

`summary.lastMessage` is explicitly **`null`** on a conversation with no
chat-visible items — run detail never becomes a preview line. `content` is
truncated server-side for the preview and marked `contentTruncated: true` when
it was cut; it is omitted entirely for an item that carries no displayable text.

There is no `createdByUserId` on a row: the list is already filtered to the
caller's own chats, so the field could only ever repeat the caller's id.

**There is no unread count and no read watermark.** What tells a member
something happened is the [Inbox](inbox.md), which is addressed per recipient
and already carries its own read and archive state. A second unread mechanism
on the conversation would answer the same question from different data, and the
two would disagree the first time either was wrong.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company, not a member, or a malformed `companyId` |
| 422 | `validation_failed` | Any query parameter — the list takes none |
| 500 | `internal_error` | Unexpected |

---

## Read one conversation, with its messages

```
GET /api/v1/companies/{companyId}/conversations/{conversationId}
```

Returns the conversation, a window of its messages with their attachments
nested, the caller's read state, and the conversation's run state — in **one
request**.

> **Changed.** The conversation's own fields are now at the **top level**
> instead of under a `conversation` key. This is a read of one entity plus
> derived data, which the [conventions](conventions.md#a-response-is-the-thing-itself)
> shape as the entity flat with the extras as siblings. It also makes this read
> and the list row name the same fields, so a client can write either into the
> other's cache slot. The task workspace composite keeps its nesting, because
> there the conversation is one of several co-equal parts and the task is the
> subject.

Gate: **member**, then creator-only — this read serves direct chats. A direct
chat belonging to another member of the same company is `404`, and so is a
task conversation: those are read through the task workspace composite and
`GET /api/v1/tasks/{taskId}/messages`. Unlike the list, this read has no
archived filter, so an archived chat is still readable by id.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | |
| `conversationId` | path | yes | |
| `limit` | query | no | Newest N messages, 1..100, default 50 |
| `after_seq` | query | no | Messages with `seq > after_seq`. This is the reconnect path |
| `through_seq` | query | no | Bound the window above, so a page is reproducible. Alone, it is a scroll-back: the newest `limit` messages at or below the bound |

These are the ordinary [sequence parameters](conventions.md#sequenced-resources).
A cursor past the conversation's head is not an error: `after_seq` at or past
it returns an empty window carrying the real `throughSeq`, as the transcript
read does, and `through_seq` past it is clamped to the head.

**The message window is bounded, not complete.** A conversation grows without
limit, so "every message" is not a size any client can hold. The default of 50
(at most 100) covers the open panel in one request — which is the point of
folding the endpoints together — and older history is paged back with
`through_seq` on this same read.

**There is no `mark_read`.** Opening a conversation marks nothing: what
announced it was an [Inbox](inbox.md) item, and reading that item is what
clears it. One notification surface, one place to mark something seen.

```json
{
  "id": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
  "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
  "kind": "direct",
  "taskId": null,
  "agentId": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
  "createdByUserId": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
  "title": "Weekly metrics",
  "createdAt": "2026-08-11T09:14:22.481113Z",
  "updatedAt": "2026-08-15T16:02:57.903118Z",
  "revision": 12,

  "agent": {
    "id": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
    "name": "Ada",
    "title": "Head of Analytics",
    "runtimeAvailable": true
  },

  "messages": [
    {
      "id": "0197f5a2-88b1-7f04-9e13-5c7d2a4b8e60",
      "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
      "seq": 41,
      "type": "agent_text",
      "author": { "id": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72", "kind": "agent", "name": "Ada", "avatarUrl": null },
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "content": "Revenue is up 4% week over week; the drop on Tuesday was a tracking gap.",
      "createdAt": "2026-08-15T16:02:57.903118Z",
      "attachments": [
        {
          "id": "0197f5a3-2c40-7b88-a512-9f0e3d6c7188",
          "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
          "uploadedByUserId": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
          "filename": "weekly-metrics.csv",
          "contentType": "text/csv",
          "sizeBytes": 20481,
          "status": "ready",
          "createdAt": "2026-08-15T15:58:11.204417Z",
          "updatedAt": "2026-08-15T15:58:12.771903Z",
          "revision": 2
        }
      ]
    }
  ],

  "earliestAvailableSeq": 1,
  "throughSeq": 41,
  "resetRequired": false,

  "userState": {
    "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
    "userId": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
    "pinnedAt": null,
    "archivedAt": null,
    "updatedAt": "2026-08-15T16:02:58.114003Z",
    "revision": 4
  },

  "runs": {
    "queuedCount": 0,
    "activeCount": 1,
    "latest": {
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "agentId": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
      "status": "running",
      "createdAt": "2026-08-15T16:01:40.220900Z",
      "failureReason": null,
      "attempt": 1,
      "maxAttempts": 2
    }
  },

  "runQueue": {
    "currentRun": {
      "id": 4821,
      "status": "running",
      "createdAt": "2026-08-15T16:01:40.220900Z"
    },
    "items": [],
    "limit": 20
  }
}
```

**Run IDs are UUID strings.** Keep them as strings in request parameters,
transcript grouping and caches. Source revision belongs to the Run's frozen
input; Conversation item `seq` still orders the shared transcript.

**Attachments are nested inside the message that references them**, not
returned as a parallel array the client has to join by id.

> **Added.** `agent` — who answers here, with the name the header renders and
> whether their runtime is online. On a direct chat it is the conversation's own
> `agentId`; on a **task** conversation `agentId` is absent and this is the
> task's current assignee, which is the agent the send path itself resolves. It
> is `null` only for a deleted agent or an unassigned task. The panel used to
> read the agent list to draw its own title bar.

> **Removed.** `hasOlder` and `hasNewer`. Whether the window missed older or
> newer rows is derived, not carried: compare the window's lowest `seq` against
> `earliestAvailableSeq` and its highest against `throughSeq`. Two booleans
> restating that comparison could only agree with it or lie.

> **Added.** `runs.latest` gains `createdAt`, `failureReason`, `attempt` and
> `maxAttempts`. Every one is a column of the row this summary already reads.
> `createdAt` anchors the "running for 12s" caption server-side, so the timer
> survives a reload; `failureReason` is
> the only thing a turn that failed before the agent spoke can show, since it
> leaves no `agent_text` item at all; and `attempt < maxAttempts` tells the client the
> server will retry by itself, so it should neither offer a retry button nor
> poll for one. It stays nested — it is one run, not a bag of the
> conversation's own scalars.

> **Changed.** `userState` is the full state object — the one
> `GET .../state` returns and `conversation.read` carries — rather than a
> trimmed copy without `conversationId`, `userId` and `updatedAt`. Two shapes
> for one entity meant an arriving event could not be written into the slot this
> read filled without adaptation, which is the exact failure
> [domain-owned wire projections](conventions.md#domain-owned-wire-projections)
> exist to prevent.

`throughSeq` is the conversation's highest `seq` **over the whole stream**, run
detail included, so a client compares it against what it holds and knows whether
one page caught it up. It can therefore exceed the highest `seq` in `messages`
without anything being missing. The current implementation returns
`earliestAvailableSeq=1` and `resetRequired=false`; it implements no
history-retention reset branch. The reset field is reserved for a future
window-expiry contract, not a reason to assume older items were deleted.

`runs` and `runQueue` are here because a chat panel renders both and neither is
worth a round trip. `runs` is the same summary object every list row carries,
and `runQueue.items` is the same collection the run-queue endpoints mutate — both
stay nested because both are separately addressable projections, not groupings
of this conversation's own scalars. `userState` stays nested for a second
reason: it carries its own `revision`, which would collide with the
conversation's if it were flattened.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `conversation_not_found` | Unknown conversation, a task conversation, or a direct chat that is not the caller's |
| 422 | `validation_failed` | Unknown query parameter, or `limit` outside 1..100 |
| 500 | `internal_error` | Unexpected |

---

## Open a direct chat with an agent

```
POST /api/v1/companies/{companyId}/conversations
{
  "agentId": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
  "title": "Weekly metrics"
}
```

Gate: **member**. PostgreSQL generates the conversation `id`.

To start a fresh session with the same Agent, supply a new UUID `requestKey`:

```json
{
  "agentId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
  "requestKey": "d4e87361-b733-4d84-8669-51d2abfb8b98"
}
```

A new key creates an independent Conversation and provider session (`201`),
without archiving earlier chats. Reusing the same creator/company/key returns
the original Conversation (`200`), even after archive; changing its Agent
conflicts (`409 conversation_conflict`). Retain the key when retrying a failed
network request. Message sends use their own separate request key.

Omitting `requestKey` preserves the quick-open behavior: reuse the oldest active
Conversation for that creator and Agent, or create one when none exists. Archive
keeps history readable by id and removes the chat from the active collection.

`title` is optional; omit the key for an untitled chat. An explicit `null` is
rejected with `400 invalid_request`. A supplied title is trimmed and must be
1..120 runes. `agentId` must be a valid non-nil UUID.

For a chat created without a title, its first committed user message supplies
the title: whitespace is collapsed and the result is limited to 120 Unicode
characters. Later messages do not rename it, and an explicitly supplied title
is preserved. The message transaction publishes the resulting title in the
normal `conversation.updated` snapshot. Clients must key drafts, transcript
caches and navigation by `conversationId`, not only by `agentId`.

> **A bad `agentId` is `409`, not `404`.** The insert resolves the agent as a
> join requiring an unarchived agent in the same company; a no-row insert is
> reported as a mutation conflict. An unknown agent, a foreign agent, and an
> archived agent are indistinguishable. Validate the agent against
> [the agent list](agents.md) first if you need to tell them apart.

The response is the bare conversation object — the same fields the read above
returns at its top level, without the messages, state and run members a
freshly created conversation has nothing to put in.

```json
{
  "id": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
  "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
  "kind": "direct",
  "taskId": null,
  "agentId": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72",
  "createdByUserId": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
  "title": "Weekly metrics",
  "createdAt": "2026-08-11T09:14:22.481113Z",
  "updatedAt": "2026-08-11T09:14:22.481113Z",
  "revision": 1
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, an unknown field, or an explicit `null` `title` |
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 409 | `conversation_conflict` | Unknown, foreign, or archived `agentId`, or another database integrity conflict |
| 413 | `request_too_large` | Body over cap |
| 415 | `unsupported_media_type` | Not `application/json` |
| 422 | `validation_failed` | Missing or non-UUID `agentId`, or a title outside 1..120 runes |

---

## Rename a direct chat

```
PATCH /api/v1/companies/{companyId}/conversations/{conversationId}
{
  "title": "Monthly metrics review",
  "expectedRevision": 7
}
```

Gate: **the direct chat's creator**. Archived direct chats remain renameable.
Task, Schedule, channel, and human/group direct conversations are not renamed
through this endpoint. An inaccessible or non-direct conversation returns
`404 conversation_not_found`.

Both fields are required. The submitted `title` must contain 1..120 Unicode
runes. Leading and trailing Unicode whitespace and byte-order marks are removed;
the result must be nonblank. `expectedRevision` is the conversation's current
revision, an integer in 1..9007199254740991.

Success returns `200` with the same bare Conversation object shown above. A
changed title advances its revision and publishes `conversation.updated` after
commit. Saving the same normalized title at the current revision returns the
unchanged Conversation without advancing the revision. Revision checking happens
first: a stale `expectedRevision` returns `409 conversation_conflict`, even when
the submitted title matches the saved title.

The client displays nonblank saved titles literally in the list and header;
only blank or null titles use `Chat with <Agent>`. Failed saves retain the draft
in the open rename dialog. After a conflict, the client reloads and shows the
latest saved title, preserving the draft for an explicit **Save my title** retry
with the refreshed revision. It does not silently overwrite a concurrent rename.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON or an unknown field |
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `conversation_not_found` | Unknown, inaccessible, or non-direct conversation |
| 409 | `conversation_conflict` | Stale conversation revision, including a same-title request |
| 413 | `request_too_large` | Body over cap |
| 415 | `unsupported_media_type` | Not `application/json` |
| 422 | `validation_failed` | Missing, null, blank, or overlong title; missing or invalid revision |

---

## Send a message

```
POST /api/v1/conversations/{conversationId}/messages
{
  "requestKey": "0197f5a5-3d81-7c22-b944-0e5f7a213c60",
  "content": "Summarise last week's revenue.",
  "attachmentIds": ["0197f5a3-2c40-7b88-a512-9f0e3d6c7188"]
}
```

The task variant is identical in body and response:

```
POST /api/v1/tasks/{taskId}/messages
```

**The client supplies one `requestKey`.** It is a UUID retry token, not an
entity id. Within the sender's company and conversation, retrying the same key
with the same normalized content and ordered attachment ids returns the
canonical prior outcome rather than appending a second message. Reusing the key
with different payload returns 409. PostgreSQL creates the item id and the Run
id.

> **A send needs an Agent to answer.** Every item of the stream belongs to a
> run, so there is no such thing as words nobody was asked to answer. A task
> assigned to a **person**, or a direct chat whose agent was deleted, refuses
> the send with `409 conversation_conflict` rather than storing a message no run
> will ever read. Check `agent` on the conversation read before enabling the
> composer — it is `null` in exactly these cases.

> **A task chat needs the assignee's `project.tasks.execute`.** Every Run a
> task chat starts is the assignee's (ADR 0014), so a send is refused with
> `403 project_permission_required` when the assignee may not run tasks in the
> task's Project ([ADR 0017](../adr/0017-project-access-scopes.md)), whoever
> sends it. The short detail names the agent ("Eli didn't start: the assignee
> can't run tasks here."); it never names the Project. See
> [Project access](tasks.md#project-access).

> **A task chat follows its Project.** Only the Project's readers find a task
> chat: its messages, transcript, state, pin, summary and Agent Mail drafts
> are `404 conversation_not_found` to anyone else, and the chat keeps its
> Project after its task is deleted. Posting is task work: the sender needs
> `project.tasks.write` on the Project, else `403 project_permission_required`
> ([ADR 0017](../adr/0017-project-access-scopes.md)). A colleague who may
> write still has the message saved for the assignee. Reordering or removing
> queued messages and changing or sending the chat's Agent Mail drafts are
> posting too and need `project.tasks.write`. A colleague who later loses
> `project.read` or `project.tasks.write` loses the messages they queued
> there; the assignee's next Run never waits on them.

A user may send into an Agent-assigned Task whose latest business outcome is
`failed`. That message is the explicit resume action: when its fresh Run
materializes, the Task returns to `in_progress`, its previous pending failure
report is superseded, and the same Conversation history remains in scope.
`done` and `cancelled` Tasks still refuse new messages.

A user message may start a run, or join the queue behind one already in flight.
Until the founder's onboarding research has been started, the CEO conversation
named by the receipt refuses new sends with
`409 conversation_initialization_pending`; no message or queue item is written.
Exact request replays still return their original receipt. Once the research
Run exists the conversation is open: a message joins the queue behind that Run
like behind any other, and the Run's terminal path promotes it. Other
conversations stay available.

**Branch on `disposition`** — the response is a union and the other fields
depend on it.

| `disposition` | Status | Meaning | Also carries |
|---|---|---|---|
| `message` | 201, or 200 on replay | Persisted immediately, with the run it started | `message`, `runId` |
| `queued` | 202, on the first call **and** on a replay | Persisted and queued behind the in-flight run | `queueItem` |
| `removed` | 200 | Replay of a send whose queued item had already been withdrawn | — |

The status code alone does not distinguish create from replay on the `queued`
branch — both answer `202`. Branch on `disposition`, and treat a repeat of your
own send as the no-op it is.

The `queued` and `removed` branches carry **no** `message`: nothing has been
appended to the conversation yet. They carry **no `runId`** either. `runs.id`
is a database-generated UUID allocated by inserting the Run, so a
prompt still waiting behind a live run has no run identity to report — follow
`queueItem.id` until promotion, and let the promoted run arrive on its own
`run.queued` frame. PostgreSQL still reserves the future item id, so the request
key finds that durable queue row on replay.

```json
{
  "disposition": "message",
  "message": {
    "id": "0197f5a5-3d81-7c22-b944-0e5f7a213c60",
    "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
    "seq": 42,
    "type": "user_message",
    "author": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
    "runId": "00000000-0000-0000-0000-0000000012d6",
    "content": "Summarise last week's revenue.",
    "createdAt": "2026-08-15T16:10:02.118221Z",
    "attachments": []
  },
  "runId": "00000000-0000-0000-0000-0000000012d6"
}
```

> **Changed.** `message` is the flat message with its attachments nested — the
> one wire type every read in this file returns. It used to be
> `{message: {message: …, attachments: []}}`, so a caller reached through
> `message.message.content` for the text it had just sent.

The `runId` beside the message is the same value the message itself carries;
it is repeated at the top level so the queued-vs-immediate branch reads the same
way on every disposition that has one.

**No output placeholder comes back**, and none is coming. The answer is not a
row the server reserves and later fills in — it is appended when the agent
completes an item, and delivered as a `run.message_created` event. Render the pending
bubble against `runId`, then append each arriving `agent_text` item; do not wait
for a completion frame to supply the reply.

The queued branch:

```json
{
  "disposition": "queued",
  "queueItem": {
    "id": "0197f5a8-4411-7b90-8c22-19be7f3d55a1",
    "messageId": "0197f5a9-0011-7c33-a844-5d2e9f108b41",
    "content": "Also break it down by channel.",
    "attachments": [],
    "initiatedBy": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
    "queuedAt": "2026-08-15T16:11:30.004112Z",
    "revision": 1
  }
}
```

There is no position or size counter on this branch: the queue read returns
the ordered items, and a client that renders "third in line" already holds
the queue it is rendering into.

`initiatedBy` is the member who wrote the prompt. In a task chat it can be a
colleague of the assignee: their message is saved here and the assignee's
next run carries it (ADR 0014), so the queue names whose words those are.
Its attachments reach that run as task files (`taskAttachments`), frozen
from the item's reservations before it materializes, and every later run
of the task reads them from the materialized message. Only an active
member's words are carried. Removing a member, or their
leaving the company, removes what they left queued. The assignee's
`task_run_requested` item about a saved message is read and archived once the
message leaves the queue, and when the task is handed to another member.

And the removed branch, which is the whole body:

```json
{ "disposition": "removed" }
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON or an unknown field |
| 401 | `authentication_required` | No credential |
| 404 | `conversation_not_found` | Unknown conversation, or not the caller's |
| 409 | `conversation_conflict` | No Agent resolved to answer, an immediate-send `requestKey` has different content/attachments, or the conversation changed under the write |
| 409 | `run_queue_conflict` | A queued `requestKey` has different content/attachments, or the queue changed under the write |
| 409 | `run_queue_full` | The queue is at capacity — wait for a run to drain |
| 413 | `request_too_large` | Body over cap |
| 415 | `unsupported_media_type` | Not `application/json` |
| 422 | `validation_failed` | Empty content with no attachments, or an unknown `attachmentId` |

---

## Read a task's messages, addressed by task

```
GET /api/v1/tasks/{taskId}/messages
```

The same window, for a caller that holds a task id rather than a conversation
id — the server resolves the task's conversation. It takes the same
`after_seq`, `through_seq` and `limit` as the conversation read, and returns the
message window alone: no conversation, no run state. Like
`GET /conversations/{conversationId}/messages`, it is the paging form: with no
cursor it starts at the beginning of the stream, `through_seq` alone is a
scroll-back to the newest `limit` messages at or below the bound, and a cursor
past the head is clamped, never refused. The task workspace read is what opens
on the newest messages.

Prefer `GET /companies/{companyId}/conversations/{conversationId}` when you have
the conversation id; it is the one-request read the panel is budgeted around.
The task-addressed form exists for the task workspace, which already knows the
task and wants only the message window.

```json
{
  "items": [
    { "id": "0197f5a2-88b1-7f04-9e13-5c7d2a4b8e60",
      "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
      "seq": 41, "type": "agent_text",
      "author": { "id": "0197f58f-7d21-7e55-b6a4-1c8e5f930a72", "kind": "agent", "name": "Ada", "avatarUrl": null },
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "content": "Revenue is up 4% week over week.",
      "createdAt": "2026-08-15T16:02:57.903118Z", "attachments": [] }
  ],
  "earliestAvailableSeq": 1,
  "throughSeq": 60
}
```

The window carries only the two bounds. Whether older or newer rows exist, and
where to continue, is derived: compare the window's lowest and highest `seq`
against `earliestAvailableSeq` and `throughSeq`, and continue with `after_seq`
set to the last row's `seq`. `throughSeq` counts the whole stream, so a caller
that has reached the end of the chat can still see a higher number here.

> An unknown or unauthorized task reports `conversation_not_found`. There is no
> task-specific code here even though the path names a task, because the read is
> authorized as the conversation it resolves to.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `conversation_not_found` | Unknown task, or one the caller may not see |
| 422 | `validation_failed` | Unknown, repeated, or empty-valued query parameter |

---

## Read the context around one message

```
GET /api/v1/conversations/{conversationId}/messages/{messageId}/context
```

Backs search result navigation: returns a window centred on one message so a
client can render it with its neighbours without loading the whole thread.

| Parameter | In | Required | Description |
|---|---|---|---|
| `conversationId` | path | yes | |
| `messageId` | path | yes | The message to centre on |
| `limit` | query | no | Number of chat messages in the window, 1..100, default 50. The target sits at index `limit/2` when neighbours exist on both sides; near either end of the chat the window fills from the other side. Run detail between messages shares the `seq` counter and never counts |

```json
{
  "items": [
    { "id": "0197f5a2-88b1-7f04-9e13-5c7d2a4b8e60",
      "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
      "seq": 40, "type": "user_message",
      "author": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
      "runId": "00000000-0000-0000-0000-0000000012d5", "content": "How did last week go?",
      "createdAt": "2026-08-15T16:00:11.402001Z", "attachments": [] }
  ],
  "targetSeq": 41,
  "earliestAvailableSeq": 1,
  "throughSeq": 60
}
```

> **Changed.** `items` are flat messages with their attachments nested, matching
> the conversation read, instead of the `{message, attachments}` pair this
> endpoint used to return. One message wire type now serves every read in this
> file.

There is no `nextCursor`: this is a
[sequenced resource](conventions.md#sequenced-resources), so which directions
have more is derived from the window's own `seq` bounds against
`earliestAvailableSeq` and `throughSeq`, and a client continues with
`after_seq` or `through_seq` on the conversation read.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `conversation_not_found` | Unknown conversation, unknown message, or not the caller's |
| 422 | `validation_failed` | Unknown parameter, or `limit` outside 1..100 |

A message that does not exist in a conversation the caller can read reports the
conversation's code too: the message is not separately addressable, so there is
no `message_not_found`.

---

## List the caller’s pinned chats

```
GET /api/v1/companies/{companyId}/conversations/pins
```

Returns `{ "items": [ConversationUserState] }`, ordered by `pinnedAt` descending,
then Conversation id. It combines the caller’s unarchived direct-chat pins and
personal Task-chat pins. Another member’s private chats and pin choices never
appear. Schedules do not appear in this collection. Task titles and identifiers
come from the existing Task projection; pinning never renames a Task.

## List saved conversation filing states

`GET /api/v1/companies/{companyId}/conversations/states` returns
`{ "items": [ConversationUserState] }` for the caller's pinned or archived task
and direct chats. Missing entries have neither flag set. The Chat navigator
uses these states to keep archived Task chats out of its active list. Archive
does not change the Task, cancel Runs, or remove message history. Archiving a
legacy default direct chat assigns it an independent session key so restoration
can coexist with a newer default chat for the same Agent.

## Read and update the caller's conversation state

```
GET   /api/v1/conversations/{conversationId}/state
PATCH /api/v1/conversations/{conversationId}/state
{ "expectedRevision": 4, "pinned": true }
```

Per-user, per-conversation: pinning and archiving. **One object serves all
four surfaces** — this read, this write, the `userState` nested in the
conversation read, and the `conversation.read` event — so an event frame is
written straight into the slot a read filled.

There is no read watermark here. Being told about a conversation is the
[Inbox](inbox.md)'s job; this object is only how a user files one.

| Field | Type | Notes |
|---|---|---|
| `conversationId` | string | |
| `userId` | string | Whose state this is. Present because the event carries the object outside any request that would fix it |
| `pinnedAt` | string \| null | |
| `archivedAt` | string \| null | Archiving removes the conversation from the list; it stays readable by id |
| `updatedAt` | string | Orders frames from the caller's other devices |
| `revision` | number | |

`PATCH` accepts `pinned` and `archived` for direct and Task conversations.
Both flags are private to the acting member. These are booleans on the way in, timestamps
on the way out, because the server stamps *when* — and omit what you are not
changing. `expectedRevision` is required; `0` is legitimate and claims the
synthesized Task-chat default row that has never been persisted. Task filing
revisions are independent of the shared Task and Conversation revisions.
Changes publish `conversation.read` only to the acting user’s devices. There
is no shared Task-chat archive or Conversation read watermark.

```json
{
  "conversationId": "0197f5a1-4c30-7a11-9d02-6f2b8c1e4d55",
  "userId": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
  "pinnedAt": "2026-08-15T16:12:00.000000Z",
  "archivedAt": null,
  "updatedAt": "2026-08-15T16:12:00.000000Z",
  "revision": 5
}
```

A field set to the value it already holds is a requested no-op: it bumps no
revision and raises no event.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `conversation_not_found` | Unknown conversation, or not the caller's |
| 409 | `conversation_conflict` | `expectedRevision` did not match |
| 422 | `validation_failed` | Unknown field, or no changeable field supplied |

## Read the run queue

```
GET /api/v1/conversations/{conversationId}/run-queue
```

Messages waiting behind the run currently in flight. The same projection is
embedded in the conversation read, so this endpoint is for a client that wants
the queue alone after a mutation elsewhere.

`currentRun` is the **oldest non-terminal** run, which may still be `queued` or
`deferred` for retry backoff rather than executing; it is `null` when there is
none.

```json
{
  "currentRun": { "id": 4821, "status": "running", "queuedAt": "2026-08-15T16:10:02.118221Z" },
  "items": [
    { "id": "0197f5a8-4411-7b90-8c22-19be7f3d55a1", "messageId": "0197f5a9-0011-7c33-a844-5d2e9f108b41",
      "content": "Also break it down by channel.", "attachments": [],
      "initiatedBy": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
      "queuedAt": "2026-08-15T16:11:30.004112Z", "revision": 1 }
  ],
  "limit": 20
}
```

## Reorder the run queue

```
PUT /api/v1/conversations/{conversationId}/run-queue/order
{
  "items": [
    { "id": "0197f5a8-4411-7b90-8c22-19be7f3d55a1", "expectedRevision": 1 },
    { "id": "0197f5aa-77c1-7d02-b933-6a4f0e29b8c7", "expectedRevision": 4 }
  ]
}
```

`items` is the whole new order and must be a permutation of the current queue —
every pending item present, nothing added, nothing dropped, 1..20 entries. A
partial list is `422`.

**Each item carries its own `expectedRevision`**, not one revision for the
queue as a whole: the rows move independently, so a single queue-level revision
would make every reorder lose to any unrelated concurrent edit.

## Remove a run-queue item

```
DELETE /api/v1/conversations/{conversationId}/run-queue/{queueItemId}
{ "expectedRevision": 1 }
```

The delete takes a **JSON request body** — and therefore
`Content-Type: application/json` — and answers `200` with the full queue rather
than `204`.

**Both mutations return the full new queue**, not the changed row, so the
calling client applies the receipt directly. The matching
`conversation.updated` event carries the bumped Conversation and shared list
summary for other sessions; reconnect re-reads the queue when that projection
is active.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `conversation_not_found` | Unknown conversation, unknown queue item, or not the caller's |
| 409 | `run_queue_conflict` | The queue changed under the write, or an `expectedRevision` did not match |
| 415 | `unsupported_media_type` | Not `application/json` |
| 422 | `validation_failed` | `items` is not a permutation of the current queue |

### Promotion semantics

The durable `conversation_run_queue_items` and
`conversation_run_queue_attachments` records are intentional. A queued send
stores its request key and lets PostgreSQL reserve the future item id alongside
its content, attachment references, position, `queuedAt`, and `revision`; it
does not create a partial Run or item before it reaches the head. **No run id is
reserved** — a UUID is generated by inserting the row, so the run does not
exist until promotion inserts it.

When the Conversation's active Run reaches a terminal state, that transition
promotes the next eligible item in the same database transaction. Promotion:

1. inserts the Run, then the reserved user item labelled with it;
2. transfers each queued attachment reservation to the item atomically;
3. marks the queue item materialized and preserves the remaining order; and
4. publishes the message, Run, and queue events and wakes the runtime only
   after the transaction commits.

Attachments referenced by a queued item remain protected from expiry cleanup
until promotion or removal. An automatic Run retry stays ahead of user-queued
work; promotion occurs only after that retry chain is terminal. If a head item
is no longer eligible, the transaction removes it with a durable removal reason,
releases its attachment reservations, and considers the next item. There is no
polling promotion worker.

---

## Attachments

Upload is two steps so the bytes never travel through a JSON body.

```
POST /api/v1/attachments/initiate      multipart: metadata part, then file part
POST /api/v1/attachments/{attachmentId}/complete
GET  /api/v1/attachments/{attachmentId}/download?companyId={companyId}
```

`initiate` takes **multipart/form-data with exactly two parts in order**:
a JSON `metadata` part, then the `file` part. Anything else is
`422 invalid_multipart`. The metadata part carries its own
`Content-Type: application/json`:

```json
{
  "requestKey": "0197f5a3-2c40-7b88-a512-9f0e3d6c7188",
  "companyId": "0197f58e-2b10-7c40-8f31-4a9c2d7e1b03",
  "filename": "weekly-metrics.csv",
  "contentType": "text/csv",
  "sizeBytes": 20481
}
```

The client supplies `requestKey`, a UUID retry token distinct from the
database-generated attachment `id`. Within one company and uploader, an exact
retry with the same normalized filename, content type, and size replays the
same attachment (`200`); reusing the key with different metadata is
`409 attachment_conflict`. Both `initiate` and `complete` answer with the
attachment object — the same one the conversation read nests inside a message.
Use the `id` returned by `initiate` in the `complete` URL.

The download query's required `companyId` is the tenant fence for this
company-less URL. An unknown, foreign, or invisible attachment remains a
non-disclosing `404`.

PostgreSQL generates the attachment `id`; its opaque object key is derived from
that id and stored behind Zenith Team's object-storage abstraction. The key and
provider URLs never appear in an Attachment response.
Deployment config selects one shared S3-compatible backend; there is no durable
storage-authority, binding, or generation model. Downloads stay behind
Zenith Team's download route (or its short-lived redirect), so an explicit backend
migration does not change the API model.

An attachment is referenced by id in `attachmentIds` when sending a message. It
must be `ready` first; referencing a pending upload is
`409 attachment_upload_incomplete`.

Who may open a file follows one rule (ADR 0017): a file that is an active
Project file shows only to the readers of its Projects, wherever it was
shared; otherwise a message file follows its chat's readers, and an unshared
upload its uploader. A file the reader may not open is left out of its message
in every read and list, rather than refused on download. Every message read
that returns files (a chat page, the Task workspace, message context, channel
and direct message history, thread roots and replies, pins, and the send and
edit answers) carries `attachmentsWithheld`: `true` when at least one ready,
active Project file on the message was left out because the caller does not
read its Project. Show that something is there without naming it; the read
names nothing about the file. Other files the caller may not open, such as
one behind an Artifact they cannot see, are left out without the flag, and a
deleted message never carries it. Queued prompts (`RunQueueItem`) leave files
out the same way and carry no flag. On a `message.created` frame the same
field means something else (every file was left out of the frame): never
show the placeholder from a frame; refetch and use the read's value. See
[realtime.md](realtime.md#project-files-in-chats) for the frames.

| Field | Type | Notes |
|---|---|---|
| `status` | string | `pending` → `ready`. A failed upload expires rather than transitioning |
| `sizeBytes` | number | Declared at initiate, verified at complete |
| `revision` | number | Optimistic-concurrency token for attachment lifecycle changes |

Only `pending` and `ready` are public statuses. Cleanup-only deletion state is
kept internal; clients learn that an attachment is gone from the
`attachment.deleted` removal event rather than from an Attachment object whose
status changed to a third value.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `attachment_not_found` | Unknown attachment, or not in the caller's company |
| 409 | `attachment_conflict` | A scoped `requestKey` has different upload metadata, or the attachment is not in a state that allows the transition |
| 409 | `attachment_upload_incomplete` | Referenced before its upload completed |
| 410 | `attachment_upload_expired` | The upload window elapsed — initiate a new one |
| 422 | `attachment_integrity_mismatch` | Bytes do not match the declared size. Re-upload; do not retry `complete` alone |
| 422 | `invalid_multipart` | Parts missing or out of order |
| 503 | `attachment_storage_unavailable` | Object storage unreachable — retry with backoff |
| 503 | `attachment_unavailable` | The service cannot safely process the current attachment state. Retry once, then surface the request id |

---

## Realtime Events

| Event | Audience | Carries | Kind |
|---|---|---|---|
| `conversation.created` | conversation-derived | `{conversation, summary}` — the same payload shape as `conversation.updated`, so one upsert handler serves both | Cache |
| `conversation.updated` | conversation-derived | `{conversation, summary}` — Company-safe summary with no per-user state; no wrapper-level `taskId`, the Conversation carries it | Cache |
| `conversation.read` | exact user in company | The state object above, whole — the same one `GET .../state` returns and the conversation read nests as `userState` | Cache |
| `message.created` | conversation-derived | A **user** message with its attachments nested, as the sender's read returns them. Carries `seq`. When any file on the message is an active Project file it carries `attachments: []` and `attachmentsWithheld: true`; refetch the chat's messages | Cache |
| `message.updated` | conversation-derived | `{conversationId, messageId}` only: a file on the message joined or left a Project, so who may see it changed. Names no file | **Refetch** the chat's messages |
| `attachment.completed` | conversation-derived | The attachment object above | Cache |
| `attachment.deleted` | conversation-derived | `{attachmentId, messageId?, conversationId?, revision}` | Cache (removal) |

`conversation.updated` carries the safe Conversation plus the shared summary a
list needs. Per-user pin/archive/read state moves only through
`conversation.read`. `message.created` carries the message rather than a
reference, so an open panel updates without a confirming read.

> **`message.created` is the user path only.** Agent text is not delivered
> here. It enters the stream through the transcript append, which broadcasts the
> committed item as **`run.message_created`**, and there is no second frame after the
> run completes — a client that renders the reply by listening for
> `message.created` shows nothing at all.
>
> Append the qualifying `run.message_created` payloads to the same list this file's read
> filled. A payload is the chat reply when **`role` is `agent` and it has a
> non-empty `text` part**; that is exactly the test the server applies when it
> stores the item as `agent_text`, so applying anything else puts the run's
> reasoning or tool output in the user's chat. Each payload carries the item,
> its `seq`, its `runId` and its `conversationId`, so it lands in place with no
> follow-up read. See [realtime.md](realtime.md#run-events).

Task-conversation events go to the readers of the task's Project (the whole
Company for a Company-visible Project, ADR 0017). Direct-conversation events
go only to the creator's company-user audience. See
[realtime.md](realtime.md).

## Item lifecycle

There isn't one. An item is inserted once, with its content, at the position the
conversation lock gives it:

```
user_message ─► committed        the words, labelled with the run they started
agent_text   ─► committed        the answer, as the run appended it
```

Identity, `seq`, `type`, `runId`, authorship and content are all fixed from the
moment the row exists. There is no pending envelope that a later transition
completes, fails, or cancels — which is why cancelling or failing a run cannot
take back an answer the user already saw, and why a run that said nothing leaves
no item rather than an empty one.

The **ordering rule** behind that: a Conversation has one monotonic item
sequence, and only the holder of the conversation row lock may advance it.
Everything appending to the stream — a user send, a queue promotion, a
transcript append — takes that lock first, so there is exactly one writer
deciding what comes next.

### Channel notification preferences

`PATCH /companies/{companyId}/conversations/{conversationId}/discussion-state`
accepts `notificationLevel` with `expectedRevision`. Levels are `all` (default),
`mentions`, and `muted`. Preferences belong to the current membership grant;
a fresh grant resets to `all`. All delivers new top-level channel posts plus
mentions and followed-thread replies. Mentions delivers mentions and followed
threads. Muted retains those Inbox entries but emits no notification hint.
The sender never receives their own notification. Desktop delivery is suppressed
when the corresponding channel is visible in the focused app window.
