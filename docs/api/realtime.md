---
title: Realtime
summary: The receive-only client WebSocket plane — envelope, delivery scopes, event catalogue, and catch-up
---

Zenith Team has two WebSocket planes. This document specifies the **client plane**;
the daemon plane is summarized at the end and detailed in [daemon.md](daemon.md).

Realtime accelerates PostgreSQL-backed state. Entity and transcript events
carry a safe committed projection; signals carry bounded presentation hints.
REST remains the convergence path after a disconnect.

`conversation.discussion_huddle_changed` invalidates Huddle cards in their source
discussion after confirmed admission or call end. Its payload is
`{conversationId, huddleId, revision}` (the revision belongs to the Huddle).
The bounded discussion dispatcher resolves current source-discussion recipients
at delivery; it carries no chat content or transcript and grants no Huddle history
access. Clients refresh the authorized message window, even if its conversation
revision has not changed. REST polling/focus recovery repairs a lost hint.

## Connect

```
GET /api/v1/ws?company_id=<uuid>
```

Authentication rides the HTTP upgrade — there is no in-band auth frame. The
request carries either the `boardly_access` cookie or
`Authorization: Bearer <accessToken>`.

`company_id` is **required**. A missing or malformed value is `422`; a company
the caller is not a member of is `404`, never `403`. Origin is checked against
the configured allowlist. (Query parameters are snake_case; the camelCase rule
covers response bodies, not URLs.)

On accept the connection is indexed by the authenticated company and user.
There is no browser-visible room membership and no ready frame — the socket is
live the moment the upgrade succeeds. A client that needs a readiness signal
should treat the upgrade itself as one.

## The envelope

Every server frame has the same shape. Fields are not optional: a client may
rely on all of them being present.

```json
{
  "eventId": "2b5b98d8-8a7a-4e99-a948-7d900527ddee",
  "type": "task.updated",
  "companyId": "0197f58f-2c31-7a10-9e4b-6b1c4a2d0f77",
  "createdAt": "2026-08-16T09:41:02.481113Z",
  "actorId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "actorType": "user",
  "payload": { "id": "0197f5a1-…", "status": "in_progress", "revision": 8, "updatedAt": "2026-08-16T09:41:02.481113Z" }
}
```

| Field | Meaning |
|---|---|
| `eventId` | Opaque dedupe id shared by origin-node delivery and Redis relay echo. It is not an ordering key or replay cursor |
| `type` | `<domain>.<verb>`, from the catalogue below |
| `companyId` | The company the fact belongs to, or `null` for the few facts that follow a person across every company they belong to (`user.updated`, `auth.session_revoked`) |
| `createdAt` | When the change happened, not when it was delivered |
| `actorId` | Actor identity, or `null` for a system action with no concrete actor |
| `actorType` | `user`, `agent`, or `system` — the one actor-kind vocabulary the whole API uses. The envelope's required keys are a protected surface, so the keys never change; this value set is the contract |
| `payload` | The typed event body |

Who else received a frame is an internal routing decision and never appears in
the envelope. `companyId` says which tenant the fact belongs to, not who it was
delivered to — a client cannot infer the recipients of a frame.

### Client frames

The client plane is receive-only. The server drains and ignores application
frames so protocol pong frames are still processed, but there is no Zenith Team
initialization, subscription, ping, or acknowledgement message. Browsers answer
WebSocket protocol pings automatically.

## Scopes

Every accepted connection is indexed by its authenticated company and user.
A frame is delivered to one of four scopes: a company, a user, the
company-user intersection, or the narrow company-or-user union used during an
access change. There are no browser-controlled run or conversation rooms.

A domain event carries no audience. The server decides delivery in one place —
`cmd/server/realtime_routing.go` — where each topic's consumer reads the facts
that decide who may see it and calls the matching delivery method. A topic
absent from that file reaches no browser at all, which is what makes the
daemon wakeup facts internal.

Work inside a Project — its Tasks, Task chats and everything derived from
them — reaches the readers of that Project (ADR 0017): the transaction that
commits the fact also decides its audience, and the frame goes to each
reader's company-user intersection, or to the Company once when the Project
is Company-visible. See [Work inside a Project](#work-inside-a-project).
Creator-private direct conversations and everything derived from them —
messages, attachments, runs, and transcript rows — go to the creator's
company-user intersection. An id that would address nobody is refused rather
than widened to a company broadcast, and a Project frame that arrives without
its audience is dropped, never widened.

## Lanes

This is the distinction that decides what can be recovered and what cannot.

| Behavior | Persisted authority | Relayed across nodes | Recovery |
|---|---|---|---|
| Entity or transcript projection | PostgreSQL row | Yes | Re-read the owning HTTP resource |
| Relayed signal | No event row | Yes | Re-read current authoritative state; the signal itself is not replayed |

This classification is not an envelope field. **`run.message_created`** carries the
complete committed transcript item and the Run's resulting transcript head. It
is published post-commit and relayed to every eligible connected client,
including clients other than the Desktop that is running the daemon.

Token deltas never enter this plane. Desktop can consume them directly from the
same-machine daemon loopback described in the
[local Desktop preview](#local-desktop-preview); browser clients render the
completed item.

## Catch-up after a disconnect

**The socket is an accelerator. The durable state is recovered over HTTP, from
the position the client already holds — not by replaying the socket.**

There is no replay buffer, no retention window, and no resume cursor on the
upgrade. HTTP resources already expose their authoritative cursors and current
projections, so duplicating them inside the socket would create a second
recovery authority.

On reconnect:

1. For each open sequenced resource, ask for what was missed:
   - `GET …/conversations/{conversationId}/messages?after_seq=<last seq held>`
     returns `throughSeq`. This recovers agent text as well as the user's own —
     both are items of the same stream, so there is nothing extra to fetch for
     the reply.
   - `GET …/companies/{companyId}/conversations/{conversationId}/transcript?after_seq=<last conversation seq scanned>`
     returns one ascending provider-item stream across every Run. Each item
     carries `runId`; the client groups them while advancing one shared cursor.
     `throughSeq` is the newest provider-item position in the Conversation and
     `hasMore` says the bounded page stopped short. A terminal Run still
     converges only when its locally applied item reaches that Run projection's
     exact `transcriptThroughSeq`.
2. For everything else, run the central REST convergence pass. The server
   exposes Company bootstrap and Task workspace composites for screens that
   have adopted them; screens still being migrated use their existing
   authoritative reads rather than adding another socket or polling loop.
3. Desktop discards local preview scratch for any Run it catches up; the rows
   it fetched supersede that scratch.

Current Conversation detail returns `resetRequired=false` and
`earliestAvailableSeq=1`; no history-retention reset branch is implemented.
Provider transcript items are retained with the Conversation and likewise
expose no retention-reset branch.
The filtered stream can skip user-message positions; a duplicate, descending
position, or failure to reach a frozen Run head triggers authoritative
Conversation/Run recovery.

Exact head equality is necessary but does not prove complete coverage: if an
earlier frame was lost and the final frame arrived, a head-only check can
still match. Keep live observed heads separate from verified REST scan
coverage. The existing frontend shortcut and Planned correction are specified
in [Conversation TECH §11.2](../specs/conversations/TECH.md#112-verified-scan-coverage-cv-c8).

### Transport limits

| Limit | Value | On breach |
|---|---|---|
| Inbound application frame size | 64 KiB | Connection closed |
| Server ping interval | 54s | — |
| Pong deadline | 60s | Connection closed |
| Write deadline | 10s | Connection closed |
| Per-client send buffer | 256 frames | **Client evicted** |

Eviction is how the server sheds a client that cannot keep up. It closes the
socket, which triggers the client's normal reconnect and catch-up — so a slow
client degrades to the recovery path rather than stalling the fan-out.

## Event catalogue

This is every event that reaches a browser, not every event the server
publishes. The bus also carries facts that no client is meant to see —
`runtime.work_available` and `runtime.pending_work` wake a daemon,
`company.membership_changed` asks a user's daemons to resync — and a topic
absent from `cmd/server/realtime_routing.go` reaches no socket at all. If a
name below is missing from your client's handler map, it is a client gap; if a
name arrives that is not below, that is a server bug.

Every event is labelled:

- **Cache** — the payload carries a complete safe projection or removal
  receipt that can update the relevant cache directly.
- **Refetch** — the payload is intentionally a small signal and the client
  performs the named scoped read.
- **UI hint** — a relayed presentation signal whose loss does not lose product
  state.

Refetch is reserved for cases where membership of a joined or filtered
projection cannot be inferred from one event. It is not a generic size fallback
and there is no `mode` discriminator in the realtime contract.

### Company-scoped events

| Event | DTO / payload | Kind |
|---|---|---|
| `company.created` | `Company` | Cache |
| `company.updated` | `Company` **without** `role` and `permissions` — the frame reaches every member, so the viewer-specific fields are omitted; keep the ones your last Company read gave you | Cache |
| `member.added` | `Member` — with the actor ref embedded, so a roster renders | Cache |
| `member.updated` | `Member` — a role change; the affected member also receives `auth.company_access_changed` | Cache |
| `member.removed` | `{userId, revision}` | Cache (removal) |
| `agent.created` | `Agent` — the template with **no member's setup** and the acting member's `access.configure`; see the note below the table | Cache |
| `agent.updated` | `Agent` — same caveat as `agent.created` | Cache |
| `mailbox.changed` | `{reason, messageId?}` with no email content, sent only to active `mailbox.manage` holders | **Refetch** — Company Email state or its message page |
| `resource.created` | `CompanyResource` | Cache |
| `resource.updated` | `CompanyResource` | Cache |
| `resource.deleted` | `{resourceId, revision}` | Cache (removal) |
| `activity.created` | `ActivityEntry` — with actor refs resolved, and for an Agent acting in a Run the person it acted for (`onBehalfOf`) and the Run (`runId`). Only for the facts the feed shows, not for every audited event, and only to the members whose feed shows that row: a row that names a Project reaches its readers (see [Work inside a Project](#work-inside-a-project)); see [activity-and-usage.md](activity-and-usage.md) | Cache |

> `agent.created` and `agent.updated` are broadcast to the whole company and kept as company activity, so they carry the agent as a member who has not set it up reads it: `binding: null`, `runtimeAvailable: false`, `unavailableReason` `not_bound` (or `agent_archived`), `activeRunCount: 0` and `access.execute: false` — no member's machine is ever named on them (ADR 0014). `access.configure` is the acting member's (ADR 0013). Keep your own cached values for those fields when applying the frame, and refetch the agent when it is new to you or its `createdBy` changed. Your own binding arrives on `agent.binding_changed`.

Most rows in this table go to the whole Company. A newly granted or
reactivated `member.added` uses the narrow Company-or-user union so the affected
user can discover access before they have a socket indexed in that Company.
`company.created` uses the same union for the same reason: at creation the
company room is empty, so the frame must also reach the creator.

### Work inside a Project

A Project restricts who sees the work inside it (ADR 0017), so these frames
reach the Project's readers only: each reader's sockets in the Company, in
batches, or the Company once when the Project is Company-visible. The
audience is decided in the transaction that commits the fact. A member who is
not a reader, including an owner or admin who only manages the Project, never
receives them.

| Event | DTO / payload | Kind |
|---|---|---|
| `task.created` | `Task` | Cache |
| `task.updated` | `Task` | Cache |
| `task.deleted` | `{taskId, revision}` — the row is gone, so there is no projection to snapshot | Cache (removal) |
| `task.report_ready` | `{taskReportEventId, recipientAgentId, taskId, taskRevision}` | **Refetch** — preview pending reports for the relevant direct Conversation |
| `task.report_changed` | `{recipientAgentId, taskId, taskRevision}` | **Refetch** — preview again after a report was superseded or sent. A sent batch with reports from several Projects sends one per Project, each to that Project's readers |

The same audience carries every Task chat frame (`conversation.*`,
`message.*`, `attachment.deleted` and `run.*` on a
Task chat, below), every Approval and Question raised in a Task chat or
naming a Task, and `activity.created` for a feed row that names the Project.
Mailbox draft and Mailbox Activity frames additionally require `mailbox.manage`: only permission holders in that audience receive them.
A Task chat kept after its Task was deleted keeps its Project, so its frames
keep this audience. A Task chat detached before Project access existed has no
Project and reaches every member, as its reads do.

The Project lifecycle frames (`project.*`, [Projects](#projects)) also reach
the members holding `projects.manage`, who manage the Project without reading
it; they carry no Task or chat content.

### A member's own setup and machines

A binding and the runtime it names belong to one member
([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)). These
frames go to that member's sockets in the Company and never to anyone else.

| Event | DTO / payload | Kind |
|---|---|---|
| `agent.binding_changed` | `{agentId, binding}` — the recipient set an agent up on one of their runtimes, moved it, or removed it (`binding: null`). `binding` is the `AgentBinding` the `Agent` projection embeds | Cache — patch the agent's `binding` and `access.execute`, then refetch the agent for its availability |
| `agent.status_changed` | `{agents: [{agentId, runtimeAvailable}]}` — one **batched** frame per transition, for every agent the recipient bound to the runtime that flipped | Cache |
| `daemon.roster_changed` | `DaemonRosterPayload` — `{action, companyId?, daemonId?, runtimeIds?}` where `action` is `register`, `deregister`, `stale_sweep`, `runtime_gc`, `update` (rename), `remove` (runtime removal), or `tools_changed` (tool state); `daemonId` and `runtimeIds` identify affected rows | **Refetch** — `update`/`remove` refresh runtime, Agent, Run and conversation projections. `tools_changed` refreshes only runtime list/detail caches; timestamp-only or identical tool reports emit no signal. Liveness continues through the roster and `agent.status_changed` |

`agent.binding_changed` and `agent.status_changed` go to the binder. The
change can also decide whether the binder's tasks can start, so each of
their open tasks those agents execute follows as a
`task.updated` to that task's Project audience (above) with its `execution` read again and its `revision` unchanged.
That frame names no computer (see [tasks.md](tasks.md#realtime-events)).
`daemon.roster_changed` goes to the runtime's registrant, except
`stale_sweep` and `runtime_gc`: they name no machine, so they go to the whole
Company as a refetch signal.

`agent.status_changed` is the reason the agent-setup screen needs no polling.
Its `runtimeAvailable` is the same predicate the `Agent` projection gives the
recipient: the agent is not archived, the recipient has bound it, and the
bound runtime is online, usable and not signed out of its provider. So a
frame writes into the roster's cache slot without re-deriving anything. A
runtime going offline flips every agent bound to it in one frame: the payload
is an array so a client patches N rows from one delivery instead of receiving
N frames. The frame carries no `unavailableReason`; refetch the agent to show
why it is unavailable.

### Groups

Every active member reads a Company's [Groups](groups.md), so their frames go
to the whole Company. Neither carries more than the Group: refetch `GET
/groups`. Each Project a Group change touches also sends its own
`project.access_changed`.

| Event | DTO / payload | Kind |
|---|---|---|
| `group.changed` | `{groupId, revision}` — a Group was created or renamed, or gained or lost members (a member leaving the Company included) | **Refetch** — the Groups |
| `group.deleted` | `{groupId}` — the Group is gone | **Refetch** — the Groups |

### The invitation surface

Email invitations and the share link are visible only to members whose role
holds `members.invite`. Their frames route to exactly those members' sockets
in the Company; a plain member's socket never sees them.

| Event | DTO / payload | Kind |
|---|---|---|
| `invitation.changed` | `CompanyInvitation` — after create, resend, cancel, accept, expiry or a renewal request | Cache |
| `invitation.share_link_changed` | `{role, expiresAt, revision, available}` — never the URL, which only the authorized `GET /share-link` returns | **Refetch** — the share link, if the panel is open |

### Conversation-derived events

Delivery follows the authoritative conversation: a Task chat reaches the
readers of its Project ([Work inside a Project](#work-inside-a-project)), a
direct conversation only its creator in that company. The conversation's
`kind` decides, not the task id — a plan apply that deletes a task nulls the
run's `taskId` while the conversation survives with its Project, and routing on
the task would lose the Project at exactly that moment.

| Event | DTO / payload | Kind |
|---|---|---|
| `conversation.created` | `{conversation, summary}` — identical payload shape to `conversation.updated`, so a client runs one upsert handler | Cache |
| `conversation.updated` | `{conversation, summary}` — the safe Conversation plus the shared list `summary`; per-user state is deliberately absent, and there is no wrapper-level `taskId` because the Conversation carries it | Cache |
| `message.created` | A **`user_message`** item with its attachments nested, exactly as the conversation read returns them, and `attachmentsWithheld`. Carries `seq` | Cache; **Refetch** the chat's messages when `attachmentsWithheld` is true |
| `message.updated` | `{conversationId, messageId}` — a file on the message joined or left a Project; names no file | **Refetch** — the chat's messages |
| `conversation.read` | `ConversationUserState` | Cache |
| `attachment.completed` | `Attachment` | Cache |
| `attachment.deleted` | `{attachmentId, messageId?, conversationId?, revision}` | Cache (removal) |
| `mailbox.draft_changed` | `{groupId, draftId, status, recipientCount, reason}` with no addresses or content, sent only to `mailbox.manage` holders who can read this Conversation | **Refetch** — Agent Mail draft groups for this Conversation |

The app shell handles Mailbox and Agent Mail hints even while another chat
is open. Draft hints invalidate only their Conversation's
draft groups; they do not refetch Mailbox state. Bursts are coalesced, active
queries refetch, and inactive cached queries are discarded for the next visit.
Neither surface polls or refetches merely because the window regains focus;
socket readiness and recovery reconcile caches centrally.

`message.created` carries the message rather than a reference to it. It removes
the probe-then-page-then-hydrate sequence from an open chat panel — for the
user's own words, which is all it now covers.

### Project files in chats

A file that is an active Project file shows only to that Project's readers,
wherever it was shared (ADR 0017); a chat member who cannot read the Project
finds the message without the file. A frame goes to the chat's audience, not
the file's, so:

- **`message.created`** carries `attachments: []` and `attachmentsWithheld:
  true` when any file on the message is an active Project file — even the
  sender's own frame. Keep the message and refetch the chat's messages; the
  read returns the files this viewer may open, and its own
  `attachmentsWithheld` says whether one was left out for them. Never show
  the placeholder from the frame's flag, which is true for a viewer who may
  open every file too; show it from the read. The field was named
  `attachmentsFiltered` before; the read and the frame now share one name.
- **`message.updated`** `{conversationId, messageId}` is sent for every
  message holding a file that a Project files change added to or removed
  from the Project (a file the change kept sends none), to whoever may see that chat (a channel or human direct message: its
  current members). It names no file. Refetch the chat's messages.
- **`project.access_changed`** is the only frame when a grant, a removal or
  a visibility change changes who may see a Project file already shared in
  a chat: no `message.updated` is sent per message. Reread every open chat
  history on it; the reads decide again what this viewer sees and flag what
  is withheld.

### There is no `message.created` for agent text

An agent's reply used to be written twice: once into the run transcript as it
arrived, and again into the conversation when the run completed successfully.
The second write is gone. Agent text is an ordinary item of the conversation's
one stream, committed by the transcript append, and its only delivery is
**`run.message_created`**.

Concretely, for a client rendering a chat panel:

- **Do not** wait for `message.created` after sending — nothing will arrive for
  the agent's side of the turn.
- **Do** subscribe to `run.message_created` and append the qualifying payloads to the
  same message list the conversation read filled. Each carries the item, its
  `conversationId`, its `runId`, and its `seq`, so it lands in order with no
  follow-up request.
- **Use the server's own test for "qualifying".** A `run.message_created` payload is
  the chat reply when its `role` is `agent` **and** it has a non-empty `text`
  part. That is precisely the predicate the append applies to store the row as
  `agent_text`; the payload does not repeat the stored type, so a client that
  invents a looser rule renders the run's reasoning or tool output in the user's
  chat, and a stricter one drops the answer. Agent-text precedence is
  deliberate: an item may carry reasoning or a tool call alongside its text, and
  the text still wins, because the text is the only part the user asked for.
- **Do not** treat `run.completed` as the moment the answer appears. By then the
  answer has already been delivered; completion only ends the turn and releases
  the run queue. A run can complete having said nothing at all, and there is no
  empty message to mark that — the absence *is* the fact.
- On reconnect, recover agent text the same way as any other item: the
  conversation message window returns it, because it is a row of that stream.

`seq` is conversation-wide, shared with the run detail a chat panel filters out,
so consecutive rendered messages routinely differ by more than one. A gap is not
a missing message; the window's own bounds are what say whether anything is
missing.

### Run events

Delivery is inherited from the Run's conversation. Row projections and
post-commit transcript items are relayed.

| Event | Transition | Delivery / authority | Kind |
|---|---|---|---|
| `run.queued` | ∅ → queued | relayed row projection | Cache |
| `run.dispatched` | queued → dispatched | relayed row projection | Cache |
| `run.running` | dispatched → running | relayed row projection | Cache |
| `run.waiting_local_directory` | dispatched → waiting | relayed row projection | Cache |
| `run.auth_wait_changed` | Studio command enters or leaves sign-in wait; Run remains running | relayed row projection | Cache |
| `run.completed` | running → completed | relayed row projection | Cache |
| `run.failed` | running → failed | relayed row projection | Cache |
| `run.cancelled` | * → cancelled | relayed row projection | Cache |
| `run.message_created` | one completed item committed | relayed post-commit row | Cache — carries item, `seq`, and Run head |

`run.message_created` is the provider item-completed analogue, and the **only** carrier
of agent content. The append flow publishes it before its receipt lets the
daemon consume `Finished` and send the terminal callback. This orders server
publication, **not client receipt**: relay delays, disconnects or dropped
frames can expose terminal status first. Reconcile the transcript over REST;
an append receipt does not acknowledge browser delivery. Completion publishes
`run.completed` and
nothing else about the reply — there is no derived chat frame after it, because
there is no second copy to derive.

Lifecycle transition events carry the typed safe Run snapshot, with every
cache-relevant field from `GET .../runs/{runId}` except raw `error`, so a run
list updates in place with no read. The run carries `agentId` as a bare id —
the roster collections every client already caches supply the names, so the
snapshot embeds no ref objects. Its `runtimeId` is always `null`: the frame
reaches more readers than the member whose machine runs it, and that machine
is theirs alone (ADR 0014); the registrant reads the id from
`GET .../runs/{runId}`.

`run.failed` carries `failureReason`, the provider-aware `failureMessage`
sentence and the bounded `failureDetail` line, so a client can tell the person
exactly what failed from the frame alone. The safe snapshot has no `error`
field at all: the free-form provider text can be large and remains available
only on the authorized run read. Whether a failed run earned a retry is read from the
retry child's `parentRunId` linkage — the child's own `run.queued` frame —
not from a flag on the parent.

`run.message_created` carries all `RunTranscriptEntry` fields plus
`conversationId` and `throughSeq` — nothing a client can join from `runId` is
repeated. The entry's `runId` and the added Conversation routing field let a
client place it immediately; the head lets a client detect a missing prefix. `seq` is
PostgreSQL-assigned within the item's **Conversation**, not within the Run, so a
Run's own items are a subsequence with numbers missing where the user or another
item took one. It is still not a global event sequence.

### Scoped invalidation signals

These post-commit signals refresh subscribed HTTP resources. They are transport
hints and are excluded from the Activity feed. Routing fields such as recipient
lists and creators never appear in the JSON payload.

| Event | Payload | Audience and read |
|---|---|---|
| `runtime.capabilities_changed` | `{runtimeId, catalog}` (`connector` or `plugin`) | Runtime registrant only; refresh Connector/Plugin catalogs and app readiness. Identical fresh discovery and unchanged keepalives emit nothing. Recovery from expired discovery does emit a hint. |
| `connector.policy_changed` | `{}` | Company-wide opaque hint after Connector review or discovery/reaffirmation changes shared execution policy; refresh the viewer's Connector catalog and app readiness. Unchanged policy emits nothing. Runtime capability and policy hints are coalesced before refetching. |
| `conversation.channel_agents_changed` | `{conversationId, activityOnly?}` | Current parent discussion readers; refresh Agent invocations and the authorized activity view. Run transitions, retry, waiting input, approvals and questions emit status hints. Transcript changes set `activityOnly`; private execution payloads never enter this frame. |
| `conversation.discussion_resync` | `{}` | Company-wide recovery hint when the bounded discussion queue overflows; recheck only the recipient's own discussion lists, states, activity and artifacts. No private conversation identifier is included. |
| `artifact.changed` | `{}` | Current authorized artifact recipients captured in the mutation transaction; refresh artifact/results reads. Publication create, progress, retry, failure, expired leases, subdomain routing, unpublish and Studio attachment changes emit it. Reads recheck access. |
| `schedule.changed` | `{id}` | Schedule creator only; refresh definitions, lifecycle and firing receipts, including worker pause/skip/failure transitions. |
| `calendar.changed` | `{}` | Company calendar changes reach that Company; personal/shared changes reach the organizer and affected invitees across their Company tabs. Occurrence completion reaches only the acting user. |
| `wiki.run_reads_changed` | `{runId}` | Task Run evidence reaches its Company; direct and Schedule evidence reaches only the Conversation creator. Channel execution evidence is not exposed by this read surface. |
| `whiteboard.changed` | `{boardId, sequence}` | Readers of the Whiteboard Artifact (`boardId` is its Artifact ID) who have the Board open (a presence session); they catch up by delta from their last sequence. A sharing change is the Artifact's own `artifact.access_changed`. A replayed request emits nothing. |
| `whiteboard.renamed` | `{boardId, sequence}` | Every reader of the Whiteboard Artifact, open Board or not: the title also names it in chats and Artifact lists, so they refresh the Board header, discussion cards and Artifact reads. |

`whiteboard.presence` (`{boardId, session, leave}`) is not a post-commit signal:
it is an ephemeral frame, relayed across nodes without the durable stream,
delivered only to readers who currently have the Board open and never replayed.
A room holds at most 128 sessions and four per person; a reader's own heartbeat
returns every live session, which repairs a missed frame.

The discussion dispatcher resolves current recipients at delivery, retries
transient lookup/relay failures with backoff, and coalesces status hints without
letting a later activity-only hint erase a pending status change.

The client batches refresh domains and uses delayed RTK Query tag invalidation:
older in-flight reads finish before the refresh, active queries refetch, and
inactive caches are evicted without issuing requests. Initial socket readiness
and reconnect run a convergence pass. Authenticated product views do not use
periodic refresh polling. Presence heartbeats, bounded provider sign-in checks,
and the anonymous public artifact viewer have separate purposes and remain.

### Human-input events

Approvals and questions carry their complete public snapshot. A request raised
in a Task chat, or naming a Task, reaches the readers of that Project; a
direct-conversation request reaches only the origin user in that Company. The
scope is frozen on the row when the request is raised, so nothing later
re-routes it.

| Event | DTO / payload | Kind |
|---|---|---|
| `approval.requested` | `Approval` | Cache |
| `approval.resolved` | `Approval` | Cache |
| `approval.expired` | `Approval` | Cache |
| `question.asked` | `Question` | Cache |
| `question.resolved` | `Question` | Cache |
| `question.expired` | `Question` | Cache |

### Facts that follow a person

Account and access events reach the authenticated user across all of their
connected Company sockets. `user.updated` and `auth.session_revoked` belong to
no single tenant and carry `companyId: null`; the two `auth.company_access_*`
events name the company whose access changed.

| Event | DTO / payload | Kind |
|---|---|---|
| `user.updated` | `User` | Cache |
| `auth.session_revoked` | `{sessionId, reason}` | Cache — sign out |
| `auth.company_access_changed` | `{companyId, role, permissions, reason}` — `permissions` is the resolved set for the new role; re-gate from the frame, then refetch | Cache |
| `auth.company_access_revoked` | `{companyId, reason}` — one frame per socket; it is the last thing the company sockets receive before the server closes them | Cache — drop the company |

### Inbox notifications across companies

Inbox state and transient notifications route to the exact recipient across
their company connections. The envelope retains the originating `companyId`;
clients must update only that company's cache. No other user receives them.

| Event | DTO / payload | Kind |
|---|---|---|
| `inbox.created` | `InboxItem` flat, with the recipient's refreshed `counts` beside it | Cache |
| `inbox.updated` | `InboxItem` flat, with the recipient's refreshed `counts` beside it | Cache |
| `notification.issued` | `{notificationId, kind, title, body, deepLink?}` | UI hint — transient toast |

The counts ride the data-bearing item frames — the identical shape every
inbox mutation response uses, so frame and receipt land in the same cache
slots. There is no separate counts-only event: the badge is the most
frequently rendered number in the product, and it updates from the same
delivery that changed it. The one exception is a Project access change, which
hides or shows items with no Inbox frame: on `project.access_changed` the
client reads the counts again, and that read carries a higher
`countsVersion`. `counts.countsVersion` is monotonic, so a client discards a
stale frame's counts without ordering guarantees from the socket.

## Local Desktop preview

When Desktop launches the daemon, the daemon writes a versioned
`local-preview.json` beside its machine credential with mode `0600`. The file
contains a loopback-only `http://127.0.0.1:<port>/local-preview` endpoint and a
rotated bearer capability. Electron main reads that file, opens the NDJSON
stream, validates every frame, and crosses the renderer seam through the
narrow preload bridge. Browser clients have no access to this path.

A preview frame is local rendering scratch:

```json
{
  "type": "run.preview.delta",
  "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
  "runId": "4821",
  "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
  "itemKey": "assistant-7",
  "channel": "text",
  "operation": "append",
  "content": "partial"
}
```

`channel` is `text` or `reasoning`; the renderer maps `reasoning` to its
`thinking` presentation. `operation` is `append` or `replace`. Identity is
`(runId, itemKey, channel)`. There is deliberately no `seq` or delivery
acknowledgement: each subscriber has a bounded queue, adjacent appends may
coalesce, later replacements may supersede earlier ones, and old frames may be
dropped under load.

`runId` here is the Run UUID string, the same id the public Run/item DTOs
carry. This frame is disposable scratch the desktop correlates by string key
and validates as bounded text. Daemon wakeup hints and the local workspace
contracts use the same UUID string; validate at each documented boundary.

The daemon emits a payload-free reset when a preview stream is established,
and the Desktop bridge resets again when it stops or disconnects:

```json
{ "type": "run.preview.reset" }
```

Reset discards all preview scratch. It does not affect committed transcript
items or the durable cursor used for catch-up.

The matching post-commit `run.message_created` seals that item and is authoritative
even if a late local preview arrives. This direct path saves cloud throughput
without weakening multi-client consistency because every eligible client still
receives the full completed item over the client WebSocket.

## The daemon plane

`GET /api/v1/daemon/ws`, authenticated by the daemon bearer before upgrade.
Identity is pinned at connect from `X-Boardly-Daemon-ID`, `?runtime_ids=`,
`X-Client-Version` and `X-Client-Capabilities`. The pinned runtime scope is
the requested `runtime_ids` narrowed to the caller's own runtimes: registered
by the signed-in member, from this daemon, in one of their companies, and not
removed. Other ids are dropped without refusing the upgrade. A heartbeat frame
for a dropped id gets no ack, so that runtime is served by the daemon's HTTP
heartbeat instead.

| Direction | Frame | Purpose |
|---|---|---|
| daemon → server | `daemon.heartbeat` | Liveness per runtime, with its `credentialIsolation` |
| server → daemon | `daemon.heartbeat_ack` | `status: "ok"` with `serverCapabilities`, or `runtime_gone` |
| server → daemon | `daemon.run_available` | Wakeup hint; the daemon still claims over HTTP or RPC. Its optional `runId` is a **string** — nothing parses it back into an identity, the daemon only logs it |
| server → daemon | `daemon.pending_work` | Asks for an immediate heartbeat; kind `runtime_refresh` asks the daemon to probe its provider setup again and register the result instead |
| server → daemon | `daemon.companies_changed` | Re-sync the company roster; carries no data |
| daemon → server | `daemon.rpc_request` | `runs.claim` or one `runs.transcript.append` completed item |
| server → daemon | `daemon.rpc_response` | `status` mirrors an HTTP status so both paths are handled alike |

Wakeup frames are **hints that carry no work**, so a lost or duplicated hint is
harmless — the daemon claims over the authoritative path either way.

For `runs.transcript.append`, the daemon submits `runId`, `itemKey`, role, and
structured parts without a sequence. The server takes the **Conversation** row
lock, allocates the next `seq` from that one counter, and responds only after
PostgreSQL commits — so the daemon cannot predict the number it will get, and
must read it from the receipt. The daemon waits for that receipt before sending
its next durable item; transport `requestId` is RPC correlation, while
`(runId, itemKey)` is the item's natural replay identity.

These frames never reach a browser, and client-plane events never reach a
daemon.

### Projects

`project.created`, `project.updated` and `project.archived` are published
after commit to the Project's readers and the members holding
`projects.manage` (ADR 0017), or to the whole Company when the Project is
Company-visible; the audience is decided in the change's transaction. Their
payload is `ProjectLifecyclePayload`: the Project's `id`, `companyId`, `key`,
`title`, `description`, `visibility`, `revision`, `createdAt`, `updatedAt`
and `archivedAt`, and never its `files` or anyone's `access`. Kind:
**Refetch** — read the Project list or `GET /projects/{projectId}` for the
viewer's own `access` and, for a reader, the files. Task events include the
immutable `projectId`. The change writes its own `project` Activity row, so
the frames are not copied into the evidence sink.

`project.access_changed` (ADR 0017) says who may do what on a Project
changed: a grant set or removed, a Join, a visibility change, a Group's grant
or members changed or the Group deleted, or a member leaving the Company. Its payload is `{projectId, revision}` and nothing else;
`revision` is the Project's access revision after the change. It reaches the
Project's readers before and after the change, the members holding
`projects.manage` and the actor, decided inside the change's transaction; a
Project that was or became Company-visible reaches the whole Company. Kind:
**Refetch** — read `GET /projects/{projectId}` again; a 404 means the viewer
lost the Project, so drop it and everything under it from the cache. No other
frame follows the change, so a client also rereads what it has cached that
depends on who may do what there: the Project's Tasks (their
`assigneeOutsideProject`), open chat histories and Task chats (which files
show, and `attachmentsWithheld`), the Inbox counts, and the Approvals and
Questions lists, `view=decide` included. The
change writes its own `project_grant` Activity row, so the frame is not copied
into the evidence sink.

### Wiki

`wiki.page_created`, `wiki.page_updated`, `wiki.page_archived` and
`wiki.page_restored` are Company-visible facts published after commit. Their
payload is `{id, title, status, revision, contentRevision, currentRevisionId}`
— an invalidation hint, not a body transport: a client refetches the page and
the Library/Graph lists. Restoring a historical revision publishes
`wiki.page_updated` because it creates a new revision. See [Wiki API](wiki.md).
`wiki.collection_created`, `wiki.collection_updated` (a rename) and
`wiki.collection_deleted` carry `{id, name, revision}` so every member's
collection lists refetch. A deletion moves that collection's pages to Unfiled
without publishing a page event per moved page.
