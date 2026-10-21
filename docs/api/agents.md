---
title: Agents
summary: Agents, the reporting tree, appearance, and the starter agents
---

An agent is a persistent AI role inside a company: a **template** — name,
title, instructions, `provider`, model and thinking level, capability
selection, appearance and reporting line — that every member sees the same
way. There is no other kind of worker: every Task names the member
responsible for it and, optionally, the agent that does the work.

An agent has no machine of its own (ADR 0014). To run one, a member **binds**
it to one of their own runtimes of the agent's `provider` — "set Lyra up on
this computer" — and every Run that member authorizes on that agent executes
there. Bindings are personal: an agent read carries **the caller's** binding
or `null`, never another member's, and no binding ever lets anyone else use
the machine. See [Set up an agent on your computer](#set-up-an-agent-on-your-computer).

Agents form a single reporting tree. The first agent created has no parent and
is the company's **root agent** — the CEO Agent in product copy; every later
one reports to an existing agent. `provider` — `claude` or `codex` — is chosen
at creation and never changes; duplicate the agent to change it.

**Every operation in this file is member-gated** (ADR 0013). A caller who is not
an active member of `{companyId}` gets `404 company_not_found`. Any member may
list, read and create Agents; changing one — update, archive, restore — is for
its creator and the holders of `agents.manage` (company owners and admins), and anyone else gets
`403 agent_creator_required`. The root Agent alone needs `agents.manage`, to create and to configure. Those two, plus
`401 authentication_required` and `403 csrf_invalid` for cookie callers on
unsafe methods, apply everywhere below and are not repeated in the
per-operation error tables. Request bodies are capped at 64 KiB.

`GET /api/v1/companies/{companyId}/agents/{agentId}/usage` sits under the agent
path but belongs to the usage surface — see
[activity-and-usage.md](activity-and-usage.md).

## Call budget

| Screen | Requests | Served by |
|---|---|---|
| Agent roster / org chart | **1** | `GET /companies/{companyId}/agents` |
| Agent detail | **1** | `GET /companies/{companyId}/agents/{agentId}`, plus its usage report |

The org chart was one request per agent, because `reportsToAgentId` was a
bare id with nothing to resolve it against. It is an embedded ref, and so
is the caller's `binding`, runtime included.

The row carries what only it can carry — identity, the two embedded refs,
the caller's availability, and the agent's run capacity. Other work signals — last outcome,
open task count — are joins the client makes from the run and task collections
it already caches and already keeps live from events, per
[what earns a field](conventions.md#what-earns-a-field): a convenience copy on
the agent row would go stale against the very events that update those caches.

Capacity is on the row because it is not one of those joins.
`maxConcurrentRuns` lives nowhere else, and `activeRunCount` is the dispatch
gate's own count, which decides what runs next — see
[Keeping `activeRunCount` fresh](#keeping-activeruncount-fresh) for what a
client does between reads.

---

## The agent object

**Every agent response in this file is this one object.** List rows, the single
read, and all four mutations return it unchanged; `agent.created` and
`agent.updated` carry the same object as a member who has not set the agent up
reads it (see [Realtime events](#realtime-events)). There is no read-only
wrapper and no write-only variant.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
      "name": "claude (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "claude",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "createdBy": {
    "id": "0198b3c4-9a10-7c33-b7e2-4d1f0e8c6a55",
    "kind": "user",
    "name": "Dana",
    "avatarUrl": null
  },
  "access": { "configure": true, "execute": true },
  "provider": "claude",
  "model": "claude-opus-4-6",
  "thinkingLevel": "high",
  "maxConcurrentRuns": 3,
  "activeRunCount": 1,
  "runtimeAvailable": true,
  "unavailableReason": null,
  "archivedAt": null,
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-14T09:12:44.183921Z",
  "revision": 1,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | Database-generated agent identity |
| `companyId` | string | Always the path company |
| `name` | string | 1..120 runes |
| `title` | string | 1..120 runes |
| `instructions` | string | May be empty. ≤32768 runes and ≤32768 bytes |
| `avatarKey` | string \| null | The agent's look: a Fold avatar palette key such as `fold-periwinkle` (`fold-butter`, `fold-tangerine`, `fold-coral`, `fold-orchid`, `fold-lagoon`, `fold-pistachio`, `fold-periwinkle`, `fold-cocoa`); `null` or an unknown key lets the agent id choose. Starter agents are provisioned with one |
| `accentColor` | string \| null | `#RRGGBB`, uppercased server-side; `null` uses the client's default palette |
| `reportsTo` | object \| null | `{id, name}` of the manager. `null` **only** on the root agent |
| `binding` | object \| null | **The caller's** binding of this agent (ADR 0014): `{id, agentId, runtime, createdAt}`, where `runtime` is `{id, name, customName, status, provider, signIn, unusableReason}` of the caller's own machine. `customName` is the name a person gave the machine or `null` — render `customName ?? name` (see [Rename a machine](runs.md#rename-a-machine)); `status` is `online` or `offline`. `null` when the caller has not set the agent up on any of their runtimes. Never another member's binding: the same agent read by two members carries two different values |
| `createdBy` | object \| null | The member who created the agent (ADR 0013); `null` for agents that predate the record |
| `access` | object | `{configure, execute}` — **the caller's** standing. `configure` is `true` when they may update, archive or restore this agent (its creator, or a holder of `agents.manage`; the root agent needs `agents.manage`); `execute` is `true` when they have a binding, i.e. they can start this agent on one of their computers. Render controls from these, never from a role. Both are per caller: a lifecycle event carries the acting member's `configure` and `execute: false`, so a client applying `agent.created`/`agent.updated` keeps its own |
| `provider` | string | `claude` or `codex`, chosen at creation and immutable; a binding must name a runtime of this provider |
| `model` | string \| null | Per-agent override, or `null` to use the provider's default |
| `thinkingLevel` | string \| null | Per-agent override, or `null` |
| `maxConcurrentRuns` | integer | 1..10, default 3. How many **task** runs may be in flight at once **per binding** (`dispatched`, `running`, `waiting_local_directory`) — the cap is enforced per (agent, runtime), so two members running the same agent on their own machines each get the full lane; task runs past it stay `queued`. Direct-conversation runs neither wait for a slot nor occupy one. Writable via `PATCH` |
| `capabilities` | object | Complete inheritance selection, template state that applies on whichever runtime a member's binding names; see [Configure inherited capabilities](#configure-inherited-capabilities). Empty denial arrays inherit all available capabilities |
| `activeRunCount` | integer | How many runs hold one of **the caller's** binding's slots right now, counted with the same task-lane predicate the dispatch gate enforces — so `activeRunCount` and `maxConcurrentRuns` render together and mean what dispatch does. `0` without a binding. Direct-conversation runs excluded. A snapshot at read time: see [Keeping `activeRunCount` fresh](#keeping-activeruncount-fresh) |
| `runtimeAvailable` | boolean | Live composite, **for the caller**: the agent is not archived **and** the caller has a binding **and** that runtime is online, usable and signed in |
| `unavailableReason` | string \| null | Which of those failed, or `null` when `runtimeAvailable` is `true` |
| `archivedAt` | string \| null | Set by `/archive`, cleared by `/restore` |
| `createdAt` | string | RFC 3339, nanosecond UTC |
| `updatedAt` | string | RFC 3339, nanosecond UTC |
| `revision` | integer | Optimistic-concurrency token; every mutation sends it back as `expectedRevision` |

`binding.runtime.status` and `runtimeAvailable` are different questions and
both are worth carrying. `binding.runtime.status` is the runtime row's own
state, which the daemon plane writes. `runtimeAvailable` folds in this
agent's archive state, whether the caller has a binding at all, and the
machine's usability and sign-in, so an agent bound to an `online` runtime
can still be unavailable — and the same agent is available to one member and
not to another.

`unavailableReason` says **which**, in this precedence:

| Value | Meaning | The recovery the UI should offer |
|---|---|---|
| `null` | `runtimeAvailable` is `true` | — |
| `agent_archived` | `archivedAt` is set | Restore the agent |
| `not_bound` | The caller has no binding: `binding` is `null` | Set the agent up on one of the caller's computers of its provider (`PUT …/binding`); the "connect Codex on this computer" step when they have none |
| `runtime_unusable` | The bound runtime's provider CLI cannot run — below the minimum version, or not executable; the runtime's `unusableReason` says which | Upgrade or fix the CLI on that machine (the daemon clears it on its own), or move the binding to another of the caller's runtimes |
| `runtime_offline` | The bound runtime's `status` is `offline` — the machine is off | Wake the machine, or move the binding |
| `runtime_signed_out` | The bound runtime is online but its provider is signed out | Sign in on that machine and Refresh |

`runtime_unusable` is a refinement of `runtime_offline` — an unusable runtime
is always offline — and is ranked first of the two so it is reachable. It is
the difference between "open the laptop" and "run the upgrade on it". A
binding whose runtime was [removed](runs.md#remove-a-runtime) is deleted with
it, so a removed machine reads as `not_bound`, never as a phantom runtime.

> **Removed.** The run/task rollups — `openTaskCount`, `lastRunId`,
> `lastRunStatus`, `lastRunCompletedAt`. Each was a convenience copy of state
> whose authoritative carrier the client already caches and keeps live from
> events: runs from the runs list and `run.*` lifecycle frames, tasks from
> the board. A copy on the agent row could only lag those caches, and
> no event patched it. The row keeps what only it can say —
> `runtimeAvailable` and `unavailableReason`. `settings` is gone too: it was
> unwritable-but-required, an empty object every client had to carry and none
> could ever read anything out of.
>
> `activeRunCount` was removed with that batch as an unread convenience copy
> and is **back with a reader**: it is the numerator of the capacity display
> next to `maxConcurrentRuns`, and it is not a copy of the runs list — it is
> the dispatch gate's own task-lane count, which no client can re-derive
> without also re-implementing the gate's status-and-lane predicate.

### Keeping `activeRunCount` fresh

The read gives the number at the moment it was served. Nothing patches it on
the agent row afterwards: it moves when a task run is dispatched and when one
reaches a terminal state, and those transitions publish `run.*` frames, not
`agent.updated`. A dispatch does not write the agent row and does not bump its
`revision`, so there is no agent frame to send.

Read the agents again when a run frame says a slot was taken or freed. A run
takes a slot when it enters `dispatched`, holds it through `running` and
`waiting_local_directory`, and returns it on any terminal status. It also
returns it on `run.queued` when a dispatched run is put back in the queue
after its claim failed, so read again on `run.queued` too. Do not add
the frames up yourself: the count is the caller's own, for their binding's
computer (ADR 0014), and a run frame never says whose computer the run is on
(its `runtimeId` is always `null`). Task-conversation runs reach every company
member, so a count built from frames would add other members' runs. A paged
runs cache does not add up to the gate's count either. The Zenith client
batches these reads, so a burst of frames costs one list read.

**Counting from frames also drifts low.** The lane a run belongs to is
a server-side column, fixed at enqueue. On the wire the nearest thing is
`taskId`, and `taskId` is cleared when its task is deleted — which is exactly
what applying a company plan does to unfinished tasks. A run in flight through
that keeps its slot on the server while its frames stop looking like task runs.
Treat the next agent read as authoritative and do not resolve the disagreement
in the client's favour.

**Reads return refs; writes take ids.** `reportsTo` and `binding` are
read-side projections. The corresponding request fields are
`reportsToAgentId` (create and update) and `runtimeId` (create, and the
binding routes), and they are plain UUID strings.

> **Changed (ADR 0014).** The agent used to carry `runtime`, the one machine
> it was bound to for everyone, with the registrant's sharing decision
> beside it. An agent is a template now: the machine is the caller's own
> `binding`, availability is the caller's, and sharing is gone — nobody's
> machine runs anyone else's requests.

---

## List agents

```
GET /api/v1/companies/{companyId}/agents
```

All matching company agents in `(createdAt, id)` ascending order. Archived
agents are excluded unless `include_archived=true`.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `include_archived` | query | no | Exactly `true` or `false`; default `false` |
| `reports_to_agent_id` | query | no | Only this agent's **direct** reports |

Query parsing is strict: any other parameter, a repeated parameter, or an
empty-valued parameter is `422 validation_failed`. Filters combine with `AND`,
and a filter naming an agent that does not exist in this company is an empty
collection rather than a `404` — a filter is not a read.

`reports_to_agent_id` is the follow-up to `409 agent_archive_blocked`: the
error says dependents remain, and this names them in one call instead of a
client-side walk over the roster. "Which agents run on this machine" is a
question about the caller's bindings, answered by
[`GET /agent-bindings?runtime_id=`](#list-your-bindings); the `runtime_id`
filter that used to live here went with the shared machine.

Because every row carries `reportsTo`, one response is enough to draw the whole org
chart — the tree is built from `reportsTo.id`, and each node already has a
label to render.

```json
{
  "items": [
    {
      "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
      "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
      "name": "Ada",
      "title": "Chief Executive Agent",
      "instructions": "Set direction and delegate to the other agents.",
      "avatarKey": null,
      "accentColor": null,
      "reportsTo": null,
      "binding": {
        "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
        "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
        "runtime": {
          "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
          "name": "claude (Dana's MacBook Pro)",
          "customName": null,
          "status": "online",
          "provider": "claude",
          "signIn": null,
          "unusableReason": null
        },
        "createdAt": "2026-08-14T09:12:44.183921Z"
      },
      "provider": "claude",
      "model": null,
      "thinkingLevel": null,
      "maxConcurrentRuns": 3,
      "activeRunCount": 0,
      "runtimeAvailable": true,
      "unavailableReason": null,
      "archivedAt": null,
      "createdAt": "2026-08-12T11:03:19.774201Z",
      "updatedAt": "2026-08-12T11:03:19.774201Z",
      "revision": 1,
      "capabilities": {
        "enabledTools": [],
        "disabledBuiltIns": [],
        "disabledPlugins": [],
        "disabledMcpServers": []
      }
    },
    {
      "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
      "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
      "name": "Eli",
      "title": "Engineering Agent",
      "instructions": "Own the engineering outcomes end to end.",
      "avatarKey": "fold-periwinkle",
      "accentColor": "#8BACE1",
      "reportsTo": {
        "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
        "name": "Ada"
      },
      "binding": {
        "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
        "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
        "runtime": {
          "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
          "name": "claude (Dana's MacBook Pro)",
          "customName": null,
          "status": "online",
          "provider": "claude",
          "signIn": null,
          "unusableReason": null
        },
        "createdAt": "2026-08-14T09:12:44.183921Z"
      },
      "provider": "claude",
      "model": "claude-opus-4-6",
      "thinkingLevel": "high",
      "maxConcurrentRuns": 3,
      "activeRunCount": 1,
      "runtimeAvailable": true,
      "unavailableReason": null,
      "archivedAt": null,
      "createdAt": "2026-08-14T09:12:44.183921Z",
      "updatedAt": "2026-08-14T09:12:44.183921Z",
      "revision": 1,
      "capabilities": {
        "enabledTools": [],
        "disabledBuiltIns": [],
        "disabledPlugins": [],
        "disabledMcpServers": []
      }
    }
  ]
}
```

`items` is `[]` when empty, never `null`.

> **Changed.** The collection key was `agents`, the query parameter was
> `includeArchived`, and the old cursor contract is removed. The roster is
> bounded product state and is returned in full.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | Unknown, repeated, or empty-valued parameter; `include_archived` not `true`/`false`; `reports_to_agent_id` not a UUID |
| 500 | `internal_error` | Unexpected |

---

## Create an agent

```
POST /api/v1/companies/{companyId}/agents
{
  "name": "Eli",
  "title": "Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "reportsToAgentId": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "provider": "claude",
  "runtimeId": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
  "model": "claude-opus-4-6",
  "thinkingLevel": "high"
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |

| Field | Required | Description |
|---|---|---|
| `name` | yes | 1..120 runes after whitespace trimming |
| `title` | yes | 1..120 runes after whitespace trimming |
| `instructions` | no | ≤32768 runes and ≤32768 bytes; defaults to `""`. Explicit `null` is `400 invalid_request` |
| `reportsToAgentId` | no | UUID of an existing **active** agent. Omit to create the root agent |
| `avatarKey` | no | Fold look key (`fold-<palette>`, lowercase letters, digits and dashes); omitted or `null` lets the agent id choose |
| `accentColor` | no | `#RRGGBB`; uppercased server-side; omitted or `null` uses the default palette |
| `provider` | yes | `claude` or `codex`. The template's provider, fixed for its life |
| `runtimeId` | no | "Also set it up on this computer of mine now": UUID of a runtime **the caller registered**, of `provider`. Creates the caller's binding in the same transaction. Required for the root agent |
| `model` | yes | Override string 1..160 runes, or `null` for no override |
| `thinkingLevel` | yes | Override string 1..64 runes, or `null` for no override. A value must be in the provider's vocabulary: Claude Code `low \| medium \| high \| xhigh \| max`; Codex a level from a runtime's discovered model catalog (documented `none \| minimal \| low \| medium \| high \| xhigh`) |
| `maxConcurrentRuns` | no | Integer 1..10, the per-binding task-lane run cap the agent starts with; omitted means 3. `null` is `400 invalid_request` |

`binding`, `runtime`, and `reportsTo` are not accepted; the strict decoder
rejects them as unknown fields.

**An agent is created as a template (ADR 0014).** Without `runtimeId` it
exists for everyone and runs for nobody until members bind it. With
`runtimeId` the caller is bound at once, and the response carries their
`binding`. The runtime must be one the caller registered — another member's
machine, like an unknown or removed id, is `404 runtime_not_found`, since
nobody else's machine is visible to the caller — and of the template's
`provider` (`422 agent_binding_provider_mismatch`). It may be offline:
binding is setup, not a start.

Omitting `reportsToAgentId` creates the root agent. A company may hold at most
one active root, and creating it is what stamps the company's
`agentSetupCompletedAt`. The root requires `runtimeId`: onboarding is the
founder setting the company up on their own computer.

Creating the root agent also provisions the four starter agents — Lyra
(product), Eli (engineering), Susan (marketing) and Ariana (operations) — as
ordinary reports of the root, of the root's provider, bound to the caller's
runtime and carrying their own `avatarKey` and `accentColor`. They are
templates rather than roles: rename, retrain, or archive them like any other
agent, and nothing in the product keys on their names.

PostgreSQL generates the agent `id`. The response is `201 Created` and carries
the created agent projection. A body `id` is not accepted. The existing root and reports-to constraints are the authoritative integrity boundaries. `model` and `thinkingLevel` remain required nullable keys; send
`null` for no override.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
      "name": "claude (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "claude",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "provider": "claude",
  "model": "claude-opus-4-6",
  "thinkingLevel": "high",
  "maxConcurrentRuns": 3,
  "activeRunCount": 0,
  "runtimeAvailable": true,
  "unavailableReason": null,
  "archivedAt": null,
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-14T09:12:44.183921Z",
  "revision": 1,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```


**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, non-object root, unknown field, or explicit `null` instructions |
| 404 | `runtime_not_found` | `runtimeId` unknown, removed, owned by another company, or another member's machine — the caller cannot see it, so it answers like an unknown id (ADR 0014) |
| 404 | `agent_not_found` | `reportsToAgentId` unknown or archived |
| 409 | `agent_create_conflict` | A second active root agent |
| 413 | `request_too_large` | Body over 64 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | Required body field missing; `provider` outside `claude`/`codex`; a body UUID field not a UUID; `name`/`title` empty after trimming or over 120 runes; `instructions` over the cap; `model`/`thinkingLevel` empty or over their caps; `thinkingLevel` outside the provider's vocabulary; `avatarKey` or `accentColor` malformed; `maxConcurrentRuns` outside 1..10; a root agent without `runtimeId` |
| 422 | `agent_binding_provider_mismatch` | `runtimeId` names a runtime of the other provider |
| 500 | `internal_error` | Unexpected |

---

## Read one agent

```
GET /api/v1/companies/{companyId}/agents/{agentId}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `agentId` | path | yes | The agent |

Returns the agent object exactly as a list row carries it, including
`runtimeAvailable` and `unavailableReason`. Archived agents are readable by
id.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
      "name": "claude (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "claude",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "provider": "claude",
  "model": "claude-opus-4-6",
  "thinkingLevel": "high",
  "maxConcurrentRuns": 3,
  "activeRunCount": 1,
  "runtimeAvailable": true,
  "unavailableReason": null,
  "archivedAt": null,
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-14T09:12:44.183921Z",
  "revision": 1,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `agent_not_found` | `agentId` unparsable or nil, or no such agent in this company |
| 500 | `internal_error` | Unexpected |

---

## Update an agent

```
PATCH /api/v1/companies/{companyId}/agents/{agentId}
{
  "expectedRevision": 1,
  "title": "Staff Engineering Agent",
  "thinkingLevel": null
}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `agentId` | path | yes | The agent |

| Field | Required | Description |
|---|---|---|
| `expectedRevision` | yes | 1..9007199254740991; must equal the stored `revision` |
| `name` | no | 1..120 runes after trimming |
| `title` | no | 1..120 runes after trimming |
| `instructions` | no | ≤32768 runes and ≤32768 bytes. `""` is allowed |
| `reportsToAgentId` | no | UUID of an active agent. `null` is rejected |
| `model` | no | String, or `null` to clear the override |
| `thinkingLevel` | no | String, or `null` to clear the override. Checked against the agent's provider |
| `avatarKey` | no | Fold look key (`fold-<palette>`), or `null` to let the agent id choose |
| `accentColor` | no | `#RRGGBB`, or `null` for the default palette |
| `maxConcurrentRuns` | no | Integer 1..10. `null` is `400 invalid_request` — the cap always has a value |
| `capabilities` | no | Complete [capability selection](#configure-inherited-capabilities); omitted preserves it, null is rejected |

At least one mutable field must be present alongside `expectedRevision`.
`provider` is not updatable: any value is `422 validation_failed`, with a
detail that says to duplicate the agent to use the other provider.
`runtimeId` is rejected as an unknown field: the template has no machine,
and where **you** run it is your binding —
[`PUT …/binding`](#set-up-an-agent-on-your-computer), which never touches
the template's revision. `avatarKey` and `accentColor` are nullable
like `model`: an explicit `null` clears the appearance. The returned object
carries the caller's `binding` as it was, and the `agent.updated` frame
carries none; an update changes nothing about who runs the agent where.

Creation may set `maxConcurrentRuns` (default 3); this route is the only
way to change it afterwards. Lowering it below the current `activeRunCount` is accepted: nothing is
cancelled, the runs already in flight finish, and no new task run dispatches
until the count is back under the cap.

Raising it is not an instant ramp. The new cap is read on the next dispatch
poll, so a widened lane does not wait for a run to finish — but a poll claims
at most one run per agent, so an agent given three free slots and three queued
task runs fills them over successive polls. Enqueues and terminal transitions
both wake the daemon, so those polls usually come quickly. Nothing promises
that `activeRunCount` reaches `maxConcurrentRuns` at all: it only rises as far
as the agent has queued task work to dispatch.

**A parent cannot be detached.** `reportsToAgentId: null` is
`400 invalid_request`, so no non-root agent can be promoted to root; and
setting `reportsToAgentId` on the root agent is
`409 root_agent_topology_immutable`. Reparenting only ever moves a non-root
agent under another active agent.

Two requests answer `200` without a revision bump: an update whose every
supplied value already matches the stored row, and a replay where the stored
`revision` is exactly `expectedRevision + 1` and the row already reflects
everything asked for. Neither bumps the revision or raises an event.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Staff Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c9-04ee-7f27-b6a1-9d3e57c8014f",
      "name": "codex (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "codex",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "provider": "codex",
  "model": "claude-opus-4-6",
  "thinkingLevel": null,
  "maxConcurrentRuns": 3,
  "activeRunCount": 1,
  "runtimeAvailable": true,
  "unavailableReason": null,
  "archivedAt": null,
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-16T11:04:07.552118Z",
  "revision": 2,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

> `400` versus `422` on this route: `400 invalid_request` is a body that is
> not a statement this route can read at all — malformed JSON, a non-object
> root, an unknown field, a JSON value of the wrong type for its field (a
> number on `name`, a string on `maxConcurrentRuns`), or an explicit `null` on
> a field that does not accept one (`name`, `reportsToAgentId`,
> `maxConcurrentRuns`, …). `422 validation_failed` is a readable statement
> whose value fails a rule — length, range, or UUID shape.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, non-object root, an unknown field, or an explicit `null` on a field that does not accept one (`name`, `reportsToAgentId`, `maxConcurrentRuns`, …) |
| 404 | `agent_not_found` | Agent missing; new `reportsToAgentId` unknown or archived |
| 409 | `agent_revision_conflict` | `expectedRevision` does not match and the request is not a recognizable replay |
| 409 | `agent_reports_to_cycle` | The new manager is a descendant of this agent |
| 409 | `root_agent_topology_immutable` | Setting a manager on the root agent |
| 413 | `request_too_large` | Body over 64 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `expectedRevision` missing or out of range; no mutable field supplied; a value fails its length, type, or UUID rule; `thinkingLevel` outside the provider's vocabulary; `maxConcurrentRuns` outside 1..10; `provider` present at all (it is fixed) |
| 500 | `internal_error` | Unexpected |

---

## Configure inherited capabilities

Every Agent projection includes `capabilities`. Use the existing revisioned
Agent PATCH — the creator's, or an `agents.manage` holder's — to replace its
complete selection. The selection is **template state** (ADR 0014): it names
plugins and MCP servers from the company's catalog for the agent's provider,
and applies on whichever runtime a member's binding names — an id the
machine does not have is simply not there. The plugin and connector
catalogs are readable and refreshable by any member for their own runtime
(`GET /plugins?runtime_id=`, `GET /connectors?runtime_id=`,
[refresh](connectors-and-plugins.md#refresh-a-computers-catalog)); installing
plugins takes `plugins.manage`, which every role holds for its own
computers, and reviewing a Connector stays a `connectors.manage` write:

```json
{
  "expectedRevision": 4,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": ["founder-input"],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

The three denial lists are required inside a supplied selection and
`enabledTools` is optional; null and unknown fields are rejected. Empty
arrays restore inheritance. Plugin and MCP UUIDs must identify observations
from this Company for the agent's provider. Stored denials remain removable
if discovery later loses the source. No provider paths or credentials are
accepted.

| Field | Values and effect |
|---|---|
| `enabledTools` | Optional, at most one entry, today only `zenith-studio`; absent or empty means off. Opts the Agent into a managed Zenith Team tool |
| `disabledBuiltIns` | Up to five stable ids: `planning`, `coordination`, `saved-work`, `founder-input`, `wiki`; removes the corresponding `zenith-team-mcp` tools and Run-channel `zen` commands (`planning` on the root Agent also makes the server read its direct-chat Run's Tasks as any Run's, its own only, from the next request; see [Reads from inside a Run](tasks.md#reads-from-inside-a-run); `coordination` also withholds `zen task create`, `saved-work` withholds the `present_output` MCP tool, `wiki` withholds the `wiki_search` and `wiki_read` tools, and `wiki_write` from the company-research Run, and makes the server refuse that Agent's Company Wiki reads and writes with `wiki_agent_access_disabled`) |
| `disabledPlugins` | Up to 500 discovered Plugin UUIDs; disables the native plugin bundle, including its Skills and contributed Connector identities |
| `disabledMcpServers` | Up to 500 Connector UUIDs with source `codex_mcp` or `claude_mcp`; excludes them from the Run’s MCP preparation |

The Skills and Plugins cards share the bundle’s switch. Standalone native skill
files are not individual catalogue objects in this API. An enabled preference
means inherit availability, not install or grant access. Company Connector
policy and provider authentication remain required.

The enqueue transaction freezes this selection and the disabled plugin native
identities into the Run context, keyed on the runtime the authorizing
member's binding names. Later edits affect new Task and direct-chat Runs;
queued Runs and retry children keep their frozen input. Scheduled work keeps
its separately reviewed exact Connector scope. A binding moving to another
runtime changes nothing here: the selection is the template's.

The daemon passes process-local native restrictions without editing shared
provider configuration. Codex effective plugin discovery and Claude loaded-plugin
reports are checked; if a provider cannot confirm a denial or a managed setting
keeps that plugin enabled, the Run fails instead of presenting the setting as
enforced. These are capability choices, not a filesystem sandbox.

Errors use existing Agent mappings: malformed selections, unknown fields and
missing/null arrays are `400 invalid_request`; invalid ids and unknown
sources are `422 validation_failed`. Inaccessible Agents are
`404 agent_not_found`, and stale revisions are `409 agent_revision_conflict`. Successful retries of the same
revision and target return the original result without another event.

---

## Set up an agent on your computer

```
PUT /api/v1/companies/{companyId}/agents/{agentId}/binding
{ "runtimeId": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8" }
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `agentId` | path | yes | The agent |

| Field | Required | Description |
|---|---|---|
| `runtimeId` | yes | UUID of a runtime **the caller registered**, of the agent's `provider` |

The caller's **binding** of the agent (ADR 0014): "run this agent on this
computer of mine". Any active member may; no company permission is
involved. The runtime must be one the caller registered — another member's
machine is `404 runtime_not_found`, exactly like an unknown id — of the
agent's provider (`422 agent_binding_provider_mismatch`), not removed, and
the agent must not be archived (`409 agent_archived`). The runtime may be
offline: the binding is setup, not a start.

One binding per agent and member: a second `PUT` moves the binding to the
named runtime, and the caller's queued and deferred Runs on this agent move
with it; Runs a daemon already claimed finish where they are. Naming the
runtime already bound is a no-op `200`.

A binding is the caller's private setup and admits only the Runs the caller
authorizes — their Task Runs as the assignee, their direct chats, their
Schedules. It never lets anyone else use the machine. After the commit the
caller alone receives `agent.binding_changed`.

The response is the agent object as the caller now sees it, `binding`
included, at the template's unchanged `revision`.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, non-object root, or an unknown field |
| 404 | `agent_not_found` | `agentId` unparsable, nil, or not in this company |
| 404 | `runtime_not_found` | `runtimeId` unknown, removed, foreign, or another member's machine |
| 409 | `agent_archived` | The agent is archived; restore it first |
| 413 | `request_too_large` | Body over 16 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `runtimeId` missing or not a UUID |
| 422 | `agent_binding_provider_mismatch` | The runtime runs the other provider |
| 500 | `internal_error` | Unexpected |

### Remove an agent from your computer

```
DELETE /api/v1/companies/{companyId}/agents/{agentId}/binding
```

Deletes the caller's binding of this agent. Their queued and deferred Runs
on it are cancelled (`run.cancelled`, reason `agent_unbound`); Runs a daemon
already claimed finish. Idempotent: no binding is `204` too. After the
commit the caller alone receives `agent.binding_changed` with
`binding: null`. No body; `204` with no content. `404 agent_not_found` for
an unknown, malformed or foreign agent.

### List your bindings

```
GET /api/v1/companies/{companyId}/agent-bindings
GET /api/v1/companies/{companyId}/agent-bindings?runtime_id={runtimeId}
```

Every binding the caller holds in the company, oldest first — always the
caller's own, whatever their role. `runtime_id` narrows the list to one of
the caller's runtimes, which is what the runtime detail screen renders
("agents on this computer") and what a [removal](runs.md#remove-a-runtime)
reviews. Any other parameter, a repeated one, or a `runtime_id` that is not
a UUID is `422 validation_failed`.

```json
{
  "items": [
    {
      "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
      "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
      "runtime": {
        "id": "0198b3c2-77aa-7d10-9e3b-4a8c6f21d5b8",
        "name": "claude (Dana's MacBook Pro)",
        "customName": null,
        "status": "online",
        "provider": "claude",
        "signIn": null,
        "unusableReason": null
      },
      "createdAt": "2026-08-14T09:12:44.183921Z"
    }
  ]
}
```

`items` is `[]` when empty, never `null`.

---

## List the platform skills

```
GET /api/v1/companies/{companyId}/platform-skills
```

Member read. The release-owned procedures every attended Run can load
through `search_skills` / `load_skill_reference`, read from the files
compiled into this release so the list names exactly what ships. It is the
same for every Company and takes no query parameters (`422
validation_failed` otherwise). Today there are two skills, in this order:
`zenith-platform`, and `zenith-workflows`, which holds one
`<workflow-key>/playbook.md` per Workflow in the
[catalog](spec/workflows.yaml). The `workflow.yaml` forms ship beside the
playbooks for the Workflows page but are not references.

```json
{
  "items": [
    {
      "key": "zenith-platform",
      "name": "Zenith platform",
      "description": "Use for Zenith Team platform actions inside a Run that the system prompt does not fully cover: the zen CLI, your Task and its dependencies, the Conversation, presenting result files, founder Questions, Task failure, the Company and its Agents, and Mail drafts. Not for the product code you are working on.",
      "references": [
        "references/company.md", "references/conversations.md",
        "references/mail.md", "references/outputs.md", "references/questions.md",
        "references/tasks.md"
      ]
    },
    {
      "key": "zenith-workflows",
      "name": "Zenith workflows",
      "description": "Use when your Task was launched from a Zenith Team Workflow: its description names a workflow key and a playbook path under this skill. Load that exact playbook and follow it. Each playbook is the procedure, quality bar and deliverable contract for one ready-to-run workflow.",
      "references": [
        "account-brief/playbook.md", "…", "contract-review/playbook.md", "…"
      ]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `key` | The reserved `zenith-<name>` key an Agent passes to `load_skill_reference`. A custom skill can never shadow it |
| `name` | What a person reads: `Zenith platform`, `Zenith workflows` |
| `description` | The one-line trigger the Agent sees in `search_skills` |
| `references` | File paths the Agent can load beside `SKILL.md`, in bytewise path order (Workflow playbooks by key; the `workflow.yaml` forms are not references) |

The skills cannot be switched off. The Agent's `disabledBuiltIns` above
gate the `zenith-team-mcp` tool groups a Run receives; the procedures stay in
the catalog and tell the Agent what those tools do.

---

## Archive an agent

```
POST /api/v1/companies/{companyId}/agents/{agentId}/archive
{ "expectedRevision": 3 }
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `agentId` | path | yes | The agent |

Soft-deletes the agent by stamping `archivedAt`. Refused while the agent still
has active direct reports, active runs, or active tasks assigned to it —
reassign or finish those first. **The root agent can never be archived.**

The three blockers are readable before the attempt:
`GET /runs?agent_id={agentId}&active=true` lists the live runs, the cached
task board answers the open-task question, and
`GET /agents?reports_to_agent_id={agentId}` lists the reports. A roster that
checks them can disable the action instead of offering it and losing.

Archiving an already-archived agent at the matching revision is a no-op: `200`
with the row unmoved, and no event.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Staff Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c9-04ee-7f27-b6a1-9d3e57c8014f",
      "name": "codex (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "codex",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "provider": "codex",
  "model": "claude-opus-4-6",
  "thinkingLevel": null,
  "maxConcurrentRuns": 3,
  "activeRunCount": 0,
  "runtimeAvailable": false,
  "unavailableReason": "agent_archived",
  "archivedAt": "2026-08-16T11:19:40.220814Z",
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-16T11:19:40.220814Z",
  "revision": 4,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

`runtimeAvailable` flips to `false` in the same response and
`unavailableReason` becomes `agent_archived` — an archived agent is never
available, whatever its runtime is doing. Run and task history survives
archiving: a soft delete erases nothing, and the runs and tasks endpoints
still answer for an archived agent.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, non-object root, or an unknown field |
| 404 | `agent_not_found` | `agentId` unparsable, or no such agent here |
| 409 | `agent_revision_conflict` | Revision mismatch outside the replay window |
| 409 | `root_agent_topology_immutable` | The target is the root agent |
| 409 | `agent_archive_blocked` | Active direct reports, runs, or tasks remain |
| 413 | `request_too_large` | Body over 64 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `expectedRevision` missing or outside 1..9007199254740991 |
| 500 | `internal_error` | Unexpected |

---

## Restore an agent

```
POST /api/v1/companies/{companyId}/agents/{agentId}/restore
{ "expectedRevision": 4 }
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | The company |
| `agentId` | path | yes | The agent |

Clears `archivedAt`. **The agent's direct manager must be active** — restoring
under an archived manager is `404 agent_not_found`, so restore an ancestor
before its reports. Only the direct manager's archive state is checked; the
walk further up the chain looks for missing rows and cycles, not for archived
ancestors.

Restoring an already-active agent at the matching revision is a no-op: `200`
with the row unmoved, and no event.

```json
{
  "id": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
  "companyId": "0198b3c4-9f21-7a48-8d5e-6c2f91ab0d47",
  "name": "Eli",
  "title": "Staff Engineering Agent",
  "instructions": "Own the engineering outcomes end to end.",
  "avatarKey": "fold-periwinkle",
  "accentColor": "#8BACE1",
  "reportsTo": {
    "id": "0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2",
    "name": "Ada"
  },
  "binding": {
    "id": "0198b3c6-2a11-7c04-8b5e-7f3d9e0a1b2c",
    "agentId": "0198b3c5-1e02-7b93-a4c6-51fd8e2b70a9",
    "runtime": {
      "id": "0198b3c9-04ee-7f27-b6a1-9d3e57c8014f",
      "name": "codex (Dana's MacBook Pro)",
      "customName": null,
      "status": "online",
      "provider": "codex",
      "signIn": null,
      "unusableReason": null
    },
    "createdAt": "2026-08-14T09:12:44.183921Z"
  },
  "provider": "codex",
  "model": "claude-opus-4-6",
  "thinkingLevel": null,
  "maxConcurrentRuns": 3,
  "activeRunCount": 0,
  "runtimeAvailable": true,
  "unavailableReason": null,
  "archivedAt": null,
  "createdAt": "2026-08-14T09:12:44.183921Z",
  "updatedAt": "2026-08-16T11:26:02.998301Z",
  "revision": 5,
  "capabilities": {
    "enabledTools": [],
    "disabledBuiltIns": [],
    "disabledPlugins": [],
    "disabledMcpServers": []
  }
}
```

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON, non-object root, or an unknown field |
| 404 | `agent_not_found` | No such agent, or its direct manager is archived or missing |
| 409 | `agent_revision_conflict` | Revision mismatch outside the replay window |
| 409 | `root_agent_topology_immutable` | The target is the root agent |
| 409 | `agent_reports_to_cycle` | The stored manager chain loops |
| 413 | `request_too_large` | Body over 64 KiB |
| 415 | `unsupported_media_type` | `Content-Type` is not `application/json` |
| 422 | `validation_failed` | `expectedRevision` missing or out of range |
| 500 | `internal_error` | Unexpected |

---

## Realtime Events

All three use the authenticated Company's typed audience. See
[realtime.md](realtime.md).

| Event | Carries | Kind | Audience |
|---|---|---|---|
| `agent.created` | The agent object | Cache | The company |
| `agent.updated` | The complete agent object | Cache | The company |
| `agent.status_changed` | `{agents: [{agentId, runtimeAvailable}]}` — one batched frame per transition | Cache | **The binder** |
| `agent.binding_changed` | `{agentId, binding}` — the caller's binding, or `null` when removed | Cache | **The binder** |

`agent.created` and `agent.updated` carry the public object this file's
endpoints return, as a member who has not set the agent up reads it: they
reach the whole company and are kept as company activity, so no member's
machine is named on them (ADR 0014). `binding` is `null`, `runtimeAvailable`
is `false`, `unavailableReason` is `not_bound` (or `agent_archived`),
`activeRunCount` is `0` and `access.execute` is `false`; `access.configure`
is the acting member's. A client applying them keeps its own values for those
fields. Changed-field facts used by server-side listeners are not serialized.

`agent.status_changed` is **one frame per underlying transition and per
member**: every agent whose computed `runtimeAvailable` changed together for
that member — their runtime going offline flips every agent they run on it
at once — arrives in one delivery to them alone, so a client patches N cache
rows instead of receiving N frames, and nobody learns about another
member's machine. `runtimeAvailable` in the payload is computed with the
same predicate the Agent projection folds into its own field — not
archived, bound, runtime online, usable and signed in — and is why the
agent-setup screen needs no polling. `unavailableReason` belongs to the
authoritative full Agent projection; it is not part of this availability
patch.

`agent.binding_changed` follows a bind, a rebind, an unbind, a runtime
removal that moved or dropped the binding, and a membership change that
revoked it. Patch the agent row's `binding` and `access.execute`, then
refetch its `capabilities` view: plugin and connector availability is the
new machine's.

There is **no agent frame for run activity**. `maxConcurrentRuns` moves only
through `PATCH`, which does raise `agent.updated`; `activeRunCount` moves with
every task-run dispatch and terminal transition, and those raise `run.*` frames
instead — the agent row is untouched. A read's `activeRunCount` is therefore
correct at the moment it was built and drifts from there; the lifecycle frames
carry `0` and are not a source for it. See
[Keeping `activeRunCount` fresh](#keeping-activeruncount-fresh).

## Agent lifecycle

```
                 POST /agents
                      │
                      ▼
                   active ──────────────► archived
                      ▲    /archive           │
                      │                       │
                      └───────────────────────┘
                              /restore

  blocked at /archive:   active direct reports, active runs,
                         or active tasks       → agent_archive_blocked
  blocked at /restore:   direct manager archived or missing
                                               → agent_not_found
  never either:          the root agent  → root_agent_topology_immutable
```

## Custom skills

`GET /companies/{companyId}/agents/{agentId}/skills` lists the Agent's installed custom skills, stored on the server.
`POST .../skill-imports/upload` previews one multipart ZIP/Markdown file;
`POST .../skill-imports/github` previews a full public repository, folder or
SKILL.md file URL. Its request accepts only `url`; separate `ref` and `path`
fields are rejected. `POST .../skills` confirms selected version IDs from
that exact, unexpired preview. `replaceSkillId` plus `expectedRevision` explicitly
updates an existing same-named installation. `PATCH .../skills/{skillId}` changes
`enabled`, and `DELETE` takes `expectedRevision`. `GET .../reference?path=...`
reads a verified installed text file. See the `AgentSkill` schemas in
[agents.yaml](spec/agents.yaml).

The creator or `agents.manage` can configure an Agent; the root Agent requires
`agents.manage`. Confirmation rechecks membership and configuration rights.
Future direct-chat and Task Runs freeze current enabled versions. Existing chat
and Task identities are preserved; queued/active Runs and retries retain their
old versions. A changed skill-set hash starts a fresh provider session while
preserving chat history. Scheduled work carries no custom skills.
