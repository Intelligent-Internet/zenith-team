---
title: Companies
summary: The tenant — the company row, its members, profile, cross-entity search, resources, and the bootstrap read every screen starts from
---

A company is Zenith Team's tenant: every agent, task, conversation, run and
resource hangs off exactly one, and a user reaches one through an **active
membership** whose role is `owner`, `admin`, or `member`.

> **Changed.** The company object had three envelopes — bare on the read and
> wrappers on create and profile writes. It
> now has one: the object itself, at the top level, everywhere. Search returns
> a bounded `items` collection instead of `{results}`. Member rows and resource rows carry
> a resolved **actor ref** instead of a bare user id, and `resourceRef` carries
> the daemon's name alongside its id. Company responses contain only company
> state; onboarding milestones and computed statistics have their own
> resources. Bounded company, member, and resource collections are
> returned in full. `GET …/bootstrap` is new.

> **Added.** `GET …/statistics` carries `memberCount`, `runtimeCount` and
> `runtimeOnlineCount` without widening every company response. A member row
> carries the member's `email` and
> `accountDisabled`. A resource carries `daemonStatus` and `daemonLastSeenAt`,
> which is what finally says whether anything can run against that directory.
> Search results name their agent instead of shipping a bare id, the per-result
> `route` object is flattened away, and the resources list takes a `daemon_id`
> filter. `GET …/bootstrap` is documented here for the first time.

Any authenticated user may create a company and list the ones they belong to.
Reading a company and its roster needs an active membership; every other
write needs the permission its route names (`company.profile.update`,
`company.resources.manage`, `members.manage`, …), which an owner or an admin
holds. Every `Company` read carries the caller's resolved `permissions`, and
`403 company_permission_required` is the answer when the role lacks one. A
revoked membership is indistinguishable from never having had one. See
[ADR 0012](../adr/0012-roles-assign-permissions-enforce.md).

`400 invalid_request`, `401 authentication_required`, `413 request_too_large`,
`415 unsupported_media_type` and `500 internal_error` are envelope-level and
behave as [conventions.md](conventions.md) describes; the tables below list
only what is specific to this resource.

---

## List the companies you belong to

```
GET /api/v1/companies
```

Every company where the caller holds an active membership, oldest first
(`createdAt`, then `id`). No company scope is involved, so there is nothing to
be denied from. A company whose deletion has started is excluded and never
reappears. The complete active-membership collection is returned; this route
accepts no query parameters.

**`role` is the caller's role in that company, not a property of the company.**
Two members reading the same row get two different values, so a client caching
this object per company must key it by viewer as well.

```json
{
  "items": [
    {
      "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "name": "Northwind Robotics",
      "slug": "northwind-robotics",
      "taskPrefix": "NWR",
      "businessContext": "Warehouse automation for mid-size distributors.",
      "contactEmail": "ops@northwind.example",
      "phoneCountryIso2": "US",
      "phoneNumber": "4155550142",
      "role": "owner",
      "revision": 7,
      "createdAt": "2026-07-12T03:04:05.123456789Z",
      "updatedAt": "2026-08-14T09:21:00Z"
    }
  ]
}
```

> **Changed.** The collection key was `companies`. It is `items`, and the
> complete active-membership set is returned in one response.

**Errors**

| Status | Code | When |
|---|---|---|
| 422 | `validation_failed` | Unknown, repeated or empty query parameter |

---

## Create a company

```
POST /api/v1/companies
{ "name": "Northwind Robotics", "slug": "northwind-robotics", "taskPrefix": "NWR",
  "businessContext": "Warehouse automation for mid-size distributors." }
```

Creates the bare tenant: the company row and the caller's owner membership.

| Parameter | In | Required | Description |
|---|---|---|---|
| `name` | body | yes | 1..120 runes and at most 480 UTF-8 bytes after trimming |
| `slug` | body | yes | `^[a-z0-9]+(?:-[a-z0-9]+)*$`, at most 63 bytes, not reserved |
| `taskPrefix` | body | yes | `^[A-Z][A-Z0-9]{1,9}$` — the prefix in `NWR-42` |
| `businessContext` | body | no | At most 32768 bytes; `""` is accepted |

Reserved slugs: `admin`, `agents`, `api`, `app`, `auth`, `boardly`, `callback`,
`companies`, `dashboard`, `download`, `healthz`, `login`, `logout`, `metrics`,
`new`, `oauth`, `onboarding`, `projects`, `readyz`, `runs`, `settings`,
`signup`, `tasks`, `usage`, `v1`, `ws`, `www`.

PostgreSQL generates the company `id`. The globally unique slug is the
authoritative duplicate boundary; a collision is `409 company_create_conflict`.

No agents and no conversations are seeded; the org is built through
[agents.md](agents.md#create-an-agent) once a daemon registers.

The `201` response is the current company object, identical to the one the list
returns. Its `id` is database-generated:

```json
{
  "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "name": "Northwind Robotics",
  "slug": "northwind-robotics",
  "taskPrefix": "NWR",
  "businessContext": "Warehouse automation for mid-size distributors.",
  "contactEmail": null,
  "phoneCountryIso2": null,
  "phoneNumber": null,
  "role": "owner",
  "revision": 1,
  "createdAt": "2026-08-16T09:41:02.481113204Z",
  "updatedAt": "2026-08-16T09:41:02.481113204Z"
}
```

> **Changed.** The create response is the company itself rather than a wrapper.
> `ceo`, `defaultProject` and
> `initialConversationId` are no longer accepted: they were parsed and dropped,
> and the strict decoder now answers `400 invalid_request` for them like any
> other unknown field. A body `id` is likewise no longer accepted because the
> database owns row identity.

The returned row is the authoritative post-create state; no confirming read is
needed.

**Errors**

| Status | Code | When |
|---|---|---|
| 409 | `company_create_conflict` | The slug is taken |
| 422 | `validation_failed` | Any body field rule above fails |

---

## Read one company

```
GET /api/v1/companies/{companyId}
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |

The response is the company object — the same bytes the list carries in
`items`, and the same DTO `company.updated` carries on the socket.

```json
{
  "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "name": "Northwind Robotics",
  "slug": "northwind-robotics",
  "taskPrefix": "NWR",
  "businessContext": "Warehouse automation for mid-size distributors.",
  "contactEmail": "ops@northwind.example",
  "phoneCountryIso2": "US",
  "phoneNumber": "4155550142",
  "role": "owner",
  "revision": 7,
  "createdAt": "2026-07-12T03:04:05.123456789Z",
  "updatedAt": "2026-08-14T09:21:00Z"
}
```

Membership is enforced by the read itself, so a non-member and a nonexistent
company are the same `404`. A `companyId` that is not a UUID is also
`404 company_not_found`, not `400`: the membership gate parses the segment
before any handler sees it, and a caller must not be able to tell a typo from
someone else's company.

> **Changed.** Unknown query parameters are rejected here like everywhere else.
> This route used to ignore its query string entirely, which made a client's
> stale parameter silently meaningless instead of loud.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown id, malformed id, no active membership, or the company is being deleted |
| 422 | `validation_failed` | Unknown query parameter |

---

## Delete a company

```
DELETE /api/v1/companies/{companyId}
{ "expectedRevision": 7 }
```

A hard delete of the Company row (`company.delete`, owners only, human actor), its PostgreSQL-owned graph, and
the detached Activity, Run usage, Run token, and legacy Run-identity records
that belong to its Runs. Every active membership receives a committed
`auth.company_access_revoked` receipt with reason `company_deleted`, and active
Company socket access is disconnected. Success is `204 No Content` with an
empty body.

`expectedRevision` is required and must match the current Company revision, so
a confirmation made before a concurrent profile edit cannot delete the changed
Company. A successful delete leaves no tombstone; replay returns
`404 company_not_found`.

This operation does not claim control over content held by external providers,
and it does not remotely delete provider-native configuration, authored
workspace files, unmerged work, or other local files on connected computers.
Those files are not a cloud backup of the deleted Company.

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown/malformed Company, no active membership, or replay after success |
| 409 | `company_delete_conflict` | `expectedRevision` did not match; refresh and confirm again |
| 422 | `validation_failed` | `expectedRevision` is outside the supported range |

The `DELETE` requires a JSON body with `Content-Type: application/json`. A
bodiless request is `400`; a missing content type is `415`.

---

## Read company statistics

```
GET /api/v1/companies/{companyId}/statistics
```

The focused-company render aids live here rather than on every company list or
detail row. Any active member may read them.

```json
{
  "memberCount": 4,
  "runtimeCount": 2,
  "runtimeOnlineCount": 1
}
```

All three values are computed at read time and are not company columns:

- `memberCount` counts active memberships.
- `runtimeCount` counts registered runtimes for the company.
- `runtimeOnlineCount` counts online runtimes owned by an active member whose
  account is enabled. A zero means no agent can be created or dispatched now.

The last two values preserve the setup distinction without exposing a redundant
boolean: `runtimeCount == 0` means install/register a runtime;
`runtimeCount > 0 && runtimeOnlineCount == 0` means bring one online. These are
snapshot statistics, not optimistic-concurrency state, so the response has no
`revision`.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown or malformed id, or no active membership |
| 422 | `validation_failed` | Unknown query parameter |

---

## Load a company's bootstrap

```
GET /api/v1/companies/{companyId}/bootstrap
```

**Everything an authenticated owner session needs before it renders anything,
in one request.** Gate: `agents.manage` (owner, admin). Every component comes from one read-only
`REPEATABLE READ` snapshot.

### Use Cases

- **The web shell** (`authenticated-app-shell.tsx`) needs the company for the
  header and task prefix, the inbox badge, and the
  founder checklist.
- **The desktop shell** mounts the same tree and needs the same reads.
- **The home route** opens the founder's chat with the root agent, and needs
  that agent's identity and the conversation id to address it. Without them it
  read the whole agent directory to find the one agent with no manager, then
  listed conversations to find its direct chat.
- **The top chrome** draws the active-task and running-run numbers, and the
  unread badge from `inboxCounts`.
- **A client reconnecting** after a socket drop re-reads its screen state here
  rather than replaying its mount sequence, as
  [realtime.md](realtime.md#catch-up-after-a-disconnect) prescribes.

Before: ten requests across the shell and the home route. After: one.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company to bootstrap |

**This is session-scoped, not route-scoped.** It is read once when a company is
opened, and everything in it is thereafter maintained over the socket. It is
not re-read per route change.

**Each object is the same DTO its own endpoint returns**, so a client writes
each part straight into the cache slot the individual read would have filled,
and the realtime events that follow update those slots directly.

`ceoAgent` is the root agent — the one agent with no manager — and is `null`
until one exists. `ceoConversationId` is the caller's own direct conversation
with it, and `null` means they have not started it yet, which is a different
screen from "loading". It is an **id, not the conversation**: the client reads
the conversation from `GET …/conversations/{conversationId}` when it opens the
panel, and that read is the one the socket keeps fresh.

```json
{
  "company": {
    "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
    "name": "Northwind Robotics",
    "slug": "northwind-robotics",
    "taskPrefix": "NWR",
    "businessContext": "Warehouse automation for mid-size distributors.",
    "contactEmail": "ops@northwind.example",
    "phoneCountryIso2": "US",
    "phoneNumber": "4155550142",
    "role": "owner",
    "revision": 7,
    "createdAt": "2026-07-12T03:04:05.123456789Z",
    "updatedAt": "2026-08-14T09:21:00Z"
  },
  "inboxCounts": {
    "unreadCount": 12,
    "countsVersion": 1786655564512
  },
  "ceoAgent": {
    "id": "0197f5a3-2000-7000-8000-000000000004",
    "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
    "name": "Andrew",
    "title": "Chief Executive Agent",
    "instructions": "Run the company toward the founder's stated goal.",
    "avatarKey": null,
    "accentColor": null,
    "reportsTo": null,
    "runtime": {
      "id": "0198b3c4-0000-7000-8000-00000000000a",
      "name": "claude (Ada's MacBook Pro)",
      "status": "online"
    },
    "provider": "claude",
    "model": "claude-opus-4",
    "thinkingLevel": "medium",
    "maxConcurrentRuns": 3,
    "activeRunCount": 2,
    "runtimeAvailable": true,
    "unavailableReason": null,
    "archivedAt": null,
    "createdAt": "2026-07-12T03:12:44.001002Z",
    "updatedAt": "2026-08-11T18:40:02.774310Z",
    "revision": 6
  },
  "ceoConversationId": "0197f5a2-1000-7000-8000-000000000003",
  "activeTaskCount": 14,
  "queuedRunCount": 3,
  "activeRunCount": 2
}
```

> **There is no second composite for the home route.** An earlier draft had a
> `…/cockpit` alongside this one, and it re-sent `company`
> and the unread count that this response already carries — 413 of its 1,437
> bytes were already on the wire. Two composites read on the same paint that
> both carry `company` mean a single `company.updated` has to patch two cache
> slots or leave one stale, which is the coupling
> [conventions.md](conventions.md#composites) exists to prevent. Its two unique
> parts, `ceoAgent` and `ceoConversationId`, are here instead.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown or malformed id, or no active membership |
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 422 | `validation_failed` | Unknown query parameter |

Its parts stay addressable on their own endpoints — the composite is an
accelerator, not a replacement. The two run counters follow the API-wide
counting vocabulary: `queuedCount` semantics for `queuedRunCount` (`queued`
plus `deferred` — work waiting for a runtime to take it) and `activeCount`
semantics for `activeRunCount` (`dispatched`, `waiting_local_directory`,
`running` — work a runtime has taken).

---

## Update the company profile

```
PATCH /api/v1/companies/{companyId}/profile
{ "expectedRevision": 7, "name": "Northwind Robotics",
  "contactEmail": "ops@northwind.example", "phoneCountryIso2": "US",
  "phoneNumber": "4155550142" }
```

Gate: `company.profile.update` (owner, admin).

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |
| `expectedRevision` | body | yes | Integer 1..9007199254740991; the `revision` the caller last read |
| `name` | body | yes | 1..120 runes and at most 480 UTF-8 bytes after trimming |
| `contactEmail` | body | yes | String or `null`; lowercased, at most 320 bytes, must parse as a bare address with a dotted domain. `""` normalizes to `null` |
| `phoneCountryIso2` | body | yes | String or `null`; supported ISO-3166 alpha-2, uppercased |
| `phoneNumber` | body | yes | String or `null`; 1..15 digits, digits only |

**All four profile fields must be present on every call — despite the `PATCH`
verb this replaces the profile, it does not merge it.** Omitting one is
`422 validation_failed`, not "leave it alone".

`phoneCountryIso2` and `phoneNumber` move together: both strings or both
`null`. One of each is `422`.

The response is the updated company, so no confirming read is needed. A write
that changes nothing returns `200`, bumps no revision, and raises no event.

```json
{
  "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "name": "Northwind Robotics",
  "slug": "northwind-robotics",
  "taskPrefix": "NWR",
  "businessContext": "Warehouse automation for mid-size distributors.",
  "contactEmail": "ops@northwind.example",
  "phoneCountryIso2": "US",
  "phoneNumber": "4155550142",
  "role": "owner",
  "revision": 8,
  "createdAt": "2026-07-12T03:04:05.123456789Z",
  "updatedAt": "2026-08-16T09:41:02.481113204Z"
}
```

On a revision mismatch the `409` body extends the error envelope with the
server's current state, so a client rebases without a refetch — one of the
conflicts that carry state, listed in
[errors.md](errors.md#conflicts-that-carry-state):

```json
{
  "detail": "Company profile changed elsewhere",
  "code": "company_profile_revision_conflict",
  "requestId": "0198c1d7-6b3a-7f42-9e08-5d2a91c4f6b0",
  "currentCompany": {
    "id": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
    "name": "Northwind Robotics GmbH",
    "slug": "northwind-robotics",
    "taskPrefix": "NWR",
    "businessContext": "Warehouse automation for mid-size distributors.",
    "contactEmail": "hello@northwind.example",
    "phoneCountryIso2": "DE",
    "phoneNumber": "15115550111",
    "role": "owner",
    "revision": 9,
    "createdAt": "2026-07-12T03:04:05.123456789Z",
    "updatedAt": "2026-08-16T09:40:11.0029941Z"
  }
}
```

Treat `currentCompany` exactly as if it had come from the read above — it is
the whole company object, not a diff of the fields that moved.

> **Changed.** A profile write that loses a race answers
> `company_profile_revision_conflict` with `currentCompany` attached. Rebase
> on that authoritative projection, re-apply the intent, and write again with
> its new `revision`.

> **Changed.** The `{company}` wrapper is gone. The revision check now runs
> **before** the no-change check: a stale
> `expectedRevision` paired with a payload that happens to match the stored
> values used to return `200` and silently swallow the conflict. A missing or
> wrongly-typed field is `422 validation_failed` like everywhere else, rather
> than this route's private `400`; `400 invalid_request` stays for malformed
> JSON and unknown fields.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed id, or no active membership |
| 409 | `company_profile_revision_conflict` | `expectedRevision` did not match; body carries `currentCompany` |
| 422 | `validation_failed` | A field is missing, of the wrong type, or fails its rule above |

---

## List the active members

```
GET /api/v1/companies/{companyId}/members
```

Active memberships only, oldest first (`createdAt`, then the member's user id).
Revoked members are absent with no tombstone. Gate: **member** — every active
member reads the roster; a non-member gets `404`.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |

**A member row embeds a resolved actor ref, because nothing else in the API can
turn a user id into a name.** There is no `GET /users/{userId}` in the route
table and there never was; the only other identity read is `GET /me`, which
describes the caller. A roster that ships ids alone cannot be rendered at all.

`actor.id` is the user id — the value `member.removed` names on the socket, and
the value to compare against `GET /me` to find the caller's own row. The ref is
`{id, kind, name, avatarUrl}` — the one `ActorRef` wire type every actor slot
in the contract carries, with `avatarUrl` explicitly `null` when the actor has
no avatar.

`email` sits beside the ref rather than inside it. An actor ref is the shape a
message author and a Task creator also carry, and those appear on screens where
a member's address has no business being; this roster is the one place it
does, and every active member may read it.

```json
{
  "items": [
    {
      "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "actor": {
        "id": "0197f58e-2b41-7a90-9f3d-0c1a6e77b501",
        "kind": "user",
        "name": "Ada Okafor",
        "avatarUrl": null
      },
      "email": "ada@northwind.example",
      "role": "owner",
      "accountDisabled": false,
      "createdAt": "2026-07-12T03:04:05.123456789Z",
      "updatedAt": "2026-07-12T03:04:05.123456789Z",
      "revision": 1
    },
    {
      "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "actor": {
        "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
        "kind": "user",
        "name": "Duong Phung",
        "avatarUrl": null
      },
      "email": "duong@northwind.example",
      "role": "member",
      "accountDisabled": true,
      "createdAt": "2026-07-19T14:22:40.551002Z",
      "updatedAt": "2026-08-02T08:05:12.310447Z",
      "revision": 4
    }
  ]
}
```

> **Added.** `email` is the only thing that distinguishes two members with the
> same display name, and display names are free text with no uniqueness rule at
> all. An owner administering the roster — deciding whom to remove — was being
> asked to tell two "Alex"es apart from a UUID.
>
> `accountDisabled` reports `users.disabled_at`, and it is not cosmetic:
> **disabling an account does not revoke the membership**, so a disabled user
> keeps an active row on this roster. It also excludes that user's runtimes
> from `runtimeOnlineCount` on the statistics endpoint, explaining why a full
> roster may still have no runtime available.

> **Changed.** `userId` is replaced by `actor`, the same ref shape Tasks,
> tasks, messages and activity entries use. The collection key is `items`, and
> the complete active roster is returned in one response.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed id, or no active membership |
| 422 | `validation_failed` | Unknown query parameter |

---

## Search inside a company

```
POST /api/v1/companies/{companyId}/search
{ "query": "release plan", "kinds": ["task", "conversation", "message"],
  "includeArchived": false, "limit": 20 }
```

One cross-entity search over tasks, conversations and messages. Gate:
**member**. It is a `POST` because the query travels in the body, not because
it writes anything. Task hits and messages in task chats come only from
Projects the caller reads ([ADR 0017](../adr/0017-project-access-scopes.md));
a task in any other Project is never a hit.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company to search |
| `query` | body | yes | Whitespace-collapsed; at least 2 runes, at most 256 bytes, and at least one letter or digit |
| `kinds` | body | no | Subset of `task`, `conversation`, `message`, no duplicates. Absent or `null` means all three; `[]` is `422` |
| `includeArchived` | body | no | Default `false` |
| `limit` | body | no | 1..50, default 20. A top-result cap; search has no continuation cursor |

**`items` is a heterogeneous union — branch on `kind` before reading any other
field.** `kind` is present on every element and is one of `task`,
`conversation`, `message`.

| `kind` | Fields |
|---|---|
| `task` | `id`, `key`, `number`, `title`, `status`, `priority`, `agent`, `conversationId`, `routeKind`, `updatedAt` |
| `conversation` | `id`, `title`, `agent`, `routeKind`, `updatedAt` |
| `message` | `id`, `seq`, `conversationId`, `conversationKind`, `taskId`, `agent`, `key`, `number`, `title`, `snippet`, `author`, `routeKind`, `createdAt` |

`routeKind` is how a client navigates to the hit, and it is a *different* enum
from the result `kind`: `task` for task hits, `direct_conversation`,
and `task_conversation` for a message inside a task conversation. Navigation
takes `routeKind` plus the conversation — which is `conversationId` on a task
or message hit and `id` on a conversation hit, because a conversation hit *is*
its conversation.

`conversationKind` is `task` or `direct`. `snippet` is whitespace-collapsed,
capped at 240 runes, and windowed around the first query token. `author` is the
same actor ref the roster carries; a deleted author resolves to
`name: "Unknown"`. `agent` is the agent the hit belongs to — the
assignee for a task, the counterpart for a conversation, the conversation's
agent for a message — and is `null` only where the row survived the filters
without one. `seq` is the message's position in its conversation; a client
focuses the hit by passing it alongside `id`.

Matching is not uniform. Tasks and conversations match the whole collapsed
query as a case-insensitive **substring** — over `key`, the displayed
`{taskPrefix}-{number}` label, `title`, `description`, `status` and
`priority` for a task, and over the conversation title plus the
agent's name and title for a conversation — so a multi-word
query only hits where those words appear contiguously. Messages use a full-text
query built from at most the first **12 distinct alphanumeric tokens**, each
matched as a prefix, with every token required.

Ranking interleaves the kinds: each kind is ranked independently and hits are
taken round-robin, so a flood of message hits cannot bury the one matching
task.

```json
{
  "items": [
    {
      "kind": "task",
      "id": "0197f5a1-77c0-7b31-8a2e-9b1c4d5e6f70",
      "key": "NWR-42",
      "number": 42,
      "title": "Draft the release plan",
      "status": "in_progress",
      "priority": "high",
      "agent": {
        "id": "0197f5a3-2000-7000-8000-000000000002",
        "name": "Eli"
      },
      "conversationId": "0197f5a2-1000-7000-8000-000000000001",
      "routeKind": "task",
      "updatedAt": "2026-08-14T09:21:00Z"
    },
    {
      "kind": "conversation",
      "id": "0197f5a2-1000-7000-8000-000000000003",
      "title": "CEO",
      "agent": {
        "id": "0197f5a3-2000-7000-8000-000000000004",
        "name": "Andrew"
      },
      "routeKind": "direct_conversation",
      "updatedAt": "2026-08-13T18:02:11Z"
    },
    {
      "kind": "message",
      "id": "0197f5a4-3000-7000-8000-000000000005",
      "seq": 41,
      "conversationId": "0197f5a2-1000-7000-8000-000000000003",
      "conversationKind": "direct",
      "taskId": null,
      "agent": {
        "id": "0197f5a3-2000-7000-8000-000000000004",
        "name": "Andrew"
      },
      "key": null,
      "number": null,
      "title": "CEO",
      "snippet": "the release plan is approved for next Tuesday",
      "author": {
        "id": "0197f5a3-2000-7000-8000-000000000004",
        "kind": "agent",
        "name": "Andrew",
        "avatarUrl": null
      },
      "routeKind": "direct_conversation",
      "createdAt": "2026-08-13T18:02:11Z"
    }
  ]
}
```

> **Changed.** The response collection key is `items`, and `limit` is the
> bounded top-result set rather than a page size. Every element names its entity
> `id`; a message result used to call it `messageId` and carry no `id`, so the
> union disagreed with itself about identity. An absent task is `null` — the
> query used to substitute the all-zeros UUID, which is not omitted and which
> clients followed as if it were a real task. `author` is the actor ref rather
> than `{kind, id, label}`.

> **Changed.** The `route` object is flattened into `routeKind` and the ids
> beside it. `route` was not a grouping of anything: on a message hit it
> repeated `conversationId`, `taskId` and `agentId` verbatim from the element
> that contained it, so every message result shipped the same values twice
> with nothing to say which copy was authoritative. `route.kind`
> carried the only information the object owned, and it is `routeKind` now. On
> a task hit `route.taskId` was the element's own `id`, and on a conversation
> hit `route.conversationId` was the element's own `id`; both are gone rather
> than restated.

> **Added.** `agent` replaces the bare `agentId` with the `{id, name}` ref the
> rest of the contract uses. A search palette renders one line per hit —
> "NWR-42 · Draft the release plan · Eli" — and the name was the one part of
> that line the response could not supply. There is no single-agent read a
> palette could fan out to, so the alternative was loading the whole agent
> directory before showing any results. The join is already in the query: the
> assignee agent is what makes a task visible to search in the first place.
> `agentId` is not kept alongside it; `agent.id` is the same value.

What search will *not* find, which surprises most clients:

- **Tasks** not assigned to an active, non-archived agent are invisible.
- **Conversations** are limited to `direct` ones the caller created themselves,
  whose agent still exists and is not archived.
- **Messages** come only from active work: an open task conversation (status
  not `done`/`cancelled`, assigned to an active agent) or one of the
  caller's own unarchived direct conversations. At most **two messages per
  conversation** are returned.
- **`hasFile`** counts only a file the caller may open. A Project file of a
  Project they do not read is left out of the message for them, so it does
  not make the message a file match (ADR 0017).
- **Run detail is never searched.** A conversation is one stream that also holds
  the run's reasoning and tool output; the `message` kind matches only the two
  chat types, `user_message` and `agent_text`. The fence is written into the
  partial index the query is planned against, so search cannot surface something
  the caller could not already read in the chat.
- `includeArchived` affects the `conversation` kind only. Archived task
  conversations still surface message hits regardless of the flag, an archived
  direct conversation surfaces none even when it is `true`, and it does nothing
  for the `task` kind.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `company_not_found` | Unknown or malformed id, or no active membership |
| 422 | `validation_failed` | `query` too short or too long, `kinds` empty / unknown / duplicated, or `limit` outside 1..50 |

The body cap on this route is 4 KiB rather than the usual limit.

---

## List a company's resources

```
GET /api/v1/companies/{companyId}/resources
```

A company resource is a typed handle on something outside Zenith Team that agents
work against. There is exactly one type — `local_directory`, a directory on a
machine where a daemon runs. The whole subtree needs `company.resources.manage`. Ordered by
`position`, then `createdAt`, then `id`.

Binding a directory currently makes non-Schedule Runs on the matching daemon
execute in that folder. Without a binding, the daemon selects managed main
or a Task worktree; neither directory layout is an OS sandbox guarantee.
See [Bind a directory](#bind-a-directory-to-this-company).

The [managed-only workspace spec](../specs/workspaces/PRD.md) plans to retire
this execution override. It does not change the served API in this revision.
GitHub/repository references are not supported. The current behavior and
transition are separated in [workspaces.md](workspaces.md).

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |
| `daemon_id` | query | no | Return only the resources registered by this daemon |

`resourceRef` is the public projection of the handle. For `local_directory` it
names the daemon and nothing else — **the filesystem path is stored but
omitted from public Resource responses and events.** The private daemon claim
context does carry frozen `localPath`; see [the precise boundary](workspaces.md#current-placement-and-resource-references).
`metadata` is `{}` for this type.

`daemonStatus` and `daemonLastSeenAt` are derived from the runtimes that daemon
registered in *this* company:

| `daemonStatus` | Means |
|---|---|
| `online` | At least one runtime this daemon registered here is online. Runs against this directory can start |
| `offline` | The daemon registered runtimes here and none is online. `daemonLastSeenAt` says when it was last heard from |
| `unregistered` | No runtime for this daemon exists in this company at all — it never registered one, or its runtimes were collected |

`daemonLastSeenAt` is the newest heartbeat across those runtimes, and is `null`
while the daemon is `unregistered` or has never heartbeat. A daemon is marked
offline once its heartbeat falls behind the staleness threshold, so a
transition to `offline` lags the machine going away by that window.

**Neither field is writable and neither moves `revision`.** They describe the
machine's liveness, not the row, so they can change under a resource nobody has
edited and a client must not treat a difference in them as row revision state.
`daemon.roster_changed` is the scoped signal that the runtime roster moved; reconnect
also converges the relevant REST reads. `resource.updated` fires only when the
resource row itself moves.

```json
{
  "items": [
    {
      "id": "0197f5b0-4000-7000-8000-000000000006",
      "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
      "resourceType": "local_directory",
      "resourceRef": { "daemonId": "mbp-14-duong", "daemonName": "MacBook Pro" },
      "daemonStatus": "online",
      "daemonLastSeenAt": "2026-08-16T09:40:51.220913Z",
      "label": "Repository",
      "position": 2,
      "metadata": {},
      "createdBy": {
        "id": "0197f58e-2b41-7a90-9f3d-0c1a6e77b501",
        "kind": "user",
        "name": "Ada Okafor",
        "avatarUrl": null
      },
      "createdAt": "2026-07-12T03:04:05.123456789Z",
      "updatedAt": "2026-07-12T03:04:05.123456789Z",
      "revision": 1
    }
  ]
}
```

> **Added.** `daemonStatus` and `daemonLastSeenAt` are the answer to the only
> question this list is ever read to answer: *can anything actually run against
> this directory right now?* A `local_directory` resource is a path on one
> machine, and it is inert whenever that machine is not connected — but the row
> looked identical either way, so the settings screen rendered "Repository" with
> the same weight whether the laptop was awake or had been shut for a week.
> Reaching the same conclusion meant listing the company's runtimes and joining
> them to `resourceRef.daemonId` client-side, which is a join across two
> management endpoints to compute one chip.
>
> `unregistered` is a third state on purpose. It is not "offline for a while" —
> it means nothing in this company has ever claimed that daemon, so the row is
> permanently dead and the fix is to re-add the directory, not to wait.
>
> `daemon_id` filters the list to one machine. The desktop app knows its own
> daemon id and wants exactly its own row; the storage rule is one local
> directory per daemon per company, so this avoids reading the full collection
> and filtering on the client.

> **Changed.** `resourceRef` carried a bare `daemonId` with nothing in the API
> able to resolve it, so the row rendered as a machine id; it now carries
> `daemonName`, the device name the daemon registered with, falling back to
> `daemonId` when it registered none. `createdByUserId` is replaced by
> `createdBy`, the same actor ref the roster carries. The collection key is
> `items`, and all matching resources are returned in one response.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed company id, or no active membership |
| 422 | `validation_failed` | Unknown query parameter or an empty `daemon_id` |

---

## Bind a directory to this company

```
POST /api/v1/companies/{companyId}/resources
{ "daemonId": "mbp-14-duong",
  "localPath": "/Users/alex/code/acme",
  "label": "Repository",
  "position": 2 }
```

Gate: `company.resources.manage` (owner, admin). Non-Schedule Runs claimed by that machine's daemon then
execute **in place** in that directory, overriding managed direct-chat main
or the Task worktree. Schedule Runs always remain isolated.

**A company holds one directory per machine.** The daemon resolves its own row
by `daemonId`; a second row for the same machine would leave it choosing which
of your folders to write into, so the second bind is
`409 company_resource_conflict`. A client should hide the affordance once a
machine is bound rather than let a user discover the limit through an error.

PostgreSQL generates the row `id`. Repeating the same `daemonId`, `localPath`,
`label`, and `position` returns the existing row with `200`. Changing any of
those canonical fields for a daemon that is already bound is a conflict.

> **`localPath` is not checked against a filesystem here.** The server cannot
> see the caller's disk. A binding client needs local validation before
> calling; the daemon validates during preparation — on
> the machine that will actually execute there, which is the check that
> matters. A path that stops existing later fails the run, not the bind.

This is a desktop-shaped workflow because a native picker can select local
files. The HTTP authorization is owner access, not a browser/desktop check;
an authorized API client can submit the request. The current client does not
provide a complete folder-binding flow, and the managed-only target adds no
such flow.

```json
{
  "id": "0197f5b0-4000-7000-8000-000000000006",
  "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "resourceType": "local_directory",
  "resourceRef": { "daemonId": "mbp-14-duong", "daemonName": "MacBook Pro" },
  "label": "Repository",
  "position": 2,
  "metadata": {},
  "createdBy": {
    "id": "0197f58e-2b41-7a90-9f3d-0c1a6e77b501",
    "kind": "user",
    "name": "Duong Phung",
    "avatarUrl": null
  },
  "daemonStatus": "online",
  "daemonLastSeenAt": "2026-08-16T09:41:00.001200Z",
  "createdAt": "2026-08-16T09:12:44.512883Z",
  "updatedAt": "2026-08-16T09:12:44.512883Z",
  "revision": 1
}
```

Note the response does **not** echo `localPath` — the path is stored and never
serialized, on any endpoint or event, so a company owner cannot read a
colleague's disk layout back out of the API.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Malformed JSON or an unknown field |
| 401 | `authentication_required` | No credential |
| 403 | `company_permission_required` | Member whose role lacks the permission |
| 404 | `company_not_found` | Unknown company or not a member |
| 409 | `company_resource_conflict` | This daemon already has a directory bound with different canonical fields |
| 413 | `request_too_large` | Body over cap |
| 415 | `unsupported_media_type` | Not `application/json` |
| 422 | `validation_failed` | Empty `daemonId`/`localPath`/`label`, or a negative `position` |

---

## Read one resource

```
GET /api/v1/companies/{companyId}/resources/{resourceId}
```

Gate: `company.resources.manage` (owner, admin). Returns the resource object bare — the same bytes the list
carries in `items` and `resource.updated` carries on the socket.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |
| `resourceId` | path | yes | Resource id; a malformed id is `404`, not `400` |

```json
{
  "id": "0197f5b0-4000-7000-8000-000000000006",
  "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "resourceType": "local_directory",
  "resourceRef": { "daemonId": "mbp-14-duong", "daemonName": "MacBook Pro" },
  "daemonStatus": "online",
  "daemonLastSeenAt": "2026-08-16T09:40:51.220913Z",
  "label": "Repository",
  "position": 2,
  "metadata": {},
  "createdBy": {
    "id": "0197f58e-2b41-7a90-9f3d-0c1a6e77b501",
    "kind": "user",
    "name": "Ada Okafor",
    "avatarUrl": null
  },
  "createdAt": "2026-07-12T03:04:05.123456789Z",
  "updatedAt": "2026-07-12T03:04:05.123456789Z",
  "revision": 1
}
```

> **Changed.** A company the caller cannot see is `404 company_not_found` here,
> the same code every other company-scoped route uses. This subtree used to
> answer `company_resource_not_found` even when the *company* was what was
> missing, which told a client to refresh the wrong list.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed company id, or no active membership |
| 404 | `company_resource_not_found` | Unknown or malformed resource id, or a resource in another company |

---

## Update a resource

```
PATCH /api/v1/companies/{companyId}/resources/{resourceId}
{ "expectedRevision": 1, "label": "Monorepo", "position": 0 }
```

A true partial update: send `expectedRevision` plus **at least one** of
`label` and `position`. Omitted fields keep their stored value. Gate: `company.resources.manage` (owner, admin).

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |
| `resourceId` | path | yes | Resource id |
| `expectedRevision` | body | yes | Integer 1..9007199254740991 |
| `label` | body | no | 1..120 runes after trimming |
| `position` | body | no | Integer 0..1000000 |

Explicit `null` is rejected for `label` and `position`: absent means
"unchanged", and there is no way to clear either — a resource without a label
or a position is not a thing the ordering can render.

The response is the full updated resource, with `revision` incremented. A write
whose values already match returns `200`, does not bump `revision`, and emits no
event.

```json
{
  "id": "0197f5b0-4000-7000-8000-000000000006",
  "companyId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
  "resourceType": "local_directory",
  "resourceRef": { "daemonId": "mbp-14-duong", "daemonName": "MacBook Pro" },
  "daemonStatus": "offline",
  "daemonLastSeenAt": "2026-08-15T22:14:03.881204Z",
  "label": "Monorepo",
  "position": 0,
  "metadata": {},
  "createdBy": {
    "id": "0197f58e-2b41-7a90-9f3d-0c1a6e77b501",
    "kind": "user",
    "name": "Ada Okafor",
    "avatarUrl": null
  },
  "createdAt": "2026-07-12T03:04:05.123456789Z",
  "updatedAt": "2026-08-16T09:41:02.481113204Z",
  "revision": 2
}
```

> **Changed.** `metadata` is no longer an input. Its only recognized key,
> `checkoutRef`, was rejected with `422` on a `local_directory` resource and
> `local_directory` is the only type, so the field was contract surface for a
> resource type that does not exist. It stays in the response as `{}` because
> the column is real. The one-step replay allowance is also gone: a mismatched
> `expectedRevision` is a conflict, full stop, because "the stored values
> already match what you asked for" is not the same statement as "your write
> won".

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed company id, or no active membership |
| 404 | `company_resource_not_found` | Unknown resource, or one in another company |
| 409 | `company_resource_conflict` | `expectedRevision` did not match — re-read, re-apply, write again |
| 422 | `validation_failed` | `expectedRevision` out of range, neither `label` nor `position` supplied, `label` empty or too long, or `position` out of range |

The body cap on the two resource mutations is 32 KiB.

---

## Delete a resource

```
DELETE /api/v1/companies/{companyId}/resources/{resourceId}
{ "expectedRevision": 3 }
```

A hard delete — no tombstone is kept. Gate: `company.resources.manage` (owner, admin). Success is
`204 No Content` with an empty body; there is no row left to return, which is
the one case [conventions.md](conventions.md#mutation-receipts) exempts from the
receipt rule.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | Company id |
| `resourceId` | path | yes | Resource id |
| `expectedRevision` | body | yes | Integer 1..9007199254740991 |

**This `DELETE` requires a JSON body** with `Content-Type: application/json`,
because the revision guard has nowhere else to travel. A bodiless request is
`400`; a missing content type is `415`.

Deletes are not idempotent on replay: with no tombstone, repeating a successful
delete is `404 company_resource_not_found` — the same answer as for a resource
that never existed. The `resource.deleted` event carries
`expectedRevision + 1` as the revision of the deletion, so a client can order
it against a snapshot it already holds.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Active member whose role lacks the permission |
| 404 | `company_not_found` | Unknown or malformed company id, or no active membership |
| 404 | `company_resource_not_found` | Unknown resource, one in another company, or already deleted |
| 409 | `company_resource_conflict` | `expectedRevision` did not match the stored revision |
| 422 | `validation_failed` | `expectedRevision` missing or out of range |

---

## Realtime Events

Every entity payload below is the DTO this file defines, byte for byte. Routing
uses typed audiences and is not serialized; see
[realtime.md](realtime.md#scopes).

| Event | Scope | Carries | Kind |
|---|---|---|---|
| `company.created` | Company-or-user | The company object | Cache |
| `company.updated` | company | The complete company object | Cache |
| `member.added` | company or Company-or-user | The member row, actor ref embedded | Cache |
| `member.updated` | company | The complete member row | Cache |
| `member.removed` | company | `{userId, revision}` | Cache (removal) |
| `resource.created` | company | The resource object | Cache |
| `resource.updated` | company | The complete resource object | Cache |
| `resource.deleted` | company | `{resourceId, revision}` | Cache (removal) |
| `auth.company_access_changed` | user | `{companyId, role, reason}` | Cache |
| `auth.company_access_revoked` | user | `{companyId, reason}` | Cache — drop the company |

Lifecycle events carry complete public projections. Changed-field facts used
by activity and other in-process listeners never cross the WebSocket. Clients
compare `revision` to suppress an echo they already applied.

Derived resource fields such as `daemonStatus` and `daemonLastSeenAt` do not
move `revision`. Company statistics are a separate projection; refresh them
after a membership change or a runtime going online or offline rather than
treating them as company revision state.

`member.removed` names the bare `userId` rather than an actor ref: the row is
gone, and a removal only needs to identify which cached row to drop.
`member.added` and `member.updated` do carry the ref, which is what lets a
roster stay renderable without a read.

A membership grant or reactivation routes `member.added` through the narrow
Company-or-user union so the affected user can discover access before they
have a socket indexed in that Company. Updates to an existing membership use
the Company audience. `company.created` uses the same union for the same
reason: at creation the company room is empty, so the frame must reach the
creator's user audience or reach no one.

The two `auth.*` events use the affected user's typed audience, never the
Company audience — a revoked member must learn about it, and the remaining
members must not.

## Company and membership lifecycle

```
company row
   ∅ ──POST /companies──► live ──owner DELETE /companies/{companyId}──► gone
                            │
        profile PATCH bumps ┘
        revision, emits company.updated

membership
   ∅ ──grant──► active ──revoke──► revoked ──reactivate──► active
                  │                   │
   role change ───┘                   └─ absent from the roster, no tombstone,
   (owner | admin | member)              indistinguishable from never having been a member
```

Company deletion cascades every membership row and sends each formerly active
member `auth.company_access_revoked` with reason `company_deleted`.

Each membership transition bumps the membership row's `revision`, the
concurrency token every membership mutation takes as `expectedRevision`.
Roles assign, permissions enforce: an owner holds every permission, an
admin holds every permission except `company.delete`,
`members.owners.manage` and `results.publish`, and a
member holds `conversations.discuss` and `plugins.manage` (installing
plugins and connecting apps on their own computer, D28). A company keeps at least one active
owner; nobody is invited as owner.

## Change a member's role

`PATCH /api/v1/companies/{companyId}/members/{userId}` with
`{expectedRevision, role}` returns the updated `ActiveMember`. Needs
`members.manage`; when the target is an owner or the new role is `owner`,
also `members.owners.manage` (owners only). A member never changes their own
role here — leaving and transferring ownership are the two self-directed
moves — and the last active owner is never demoted (`409 team_last_owner`).
The same role is a no-op that returns the row and emits nothing. A Run
acting through its proof is refused with `403 human_actor_required`.

The company receives `member.updated`; the member receives
`auth.company_access_changed` with their new `role` and `permissions`.

## Transfer ownership

`POST /api/v1/companies/{companyId}/members/{userId}/transfer-ownership`
with `{expectedRevision}` (the target's revision) makes the target `owner`
and the caller `admin` in one transaction and returns
`{items: [newOwner, formerOwner]}`. Needs `members.owners.manage`. The
target must be an active member who is not already an owner; a disabled
account is refused.

## Company research

`GET /companies/{companyId}/initialization` returns the founder's research
progress: `{phase, status, runId?, conversationId?, message?, pagesWritten,
overviewPageId?}`. `phase` is `not_started` until the research Run exists,
`research` while it works or after it failed, and `ready` once it completed;
`status` keeps the Run vocabulary (`idle`, `awaiting_model`, `queued`,
`running`, `waiting_runtime`, `completed`, `failed`, ...). `pagesWritten` counts
the Company Wiki pages the research has saved over every attempt (automatic
retries and the founder's retries), and `overviewPageId` is its
`company-overview` hub page once written, so a client can show progress and
link to the result.

`POST` starts the research and does nothing else. It first reserves a receipt in
`awaiting_model` so sign-in can resume even before model discovery completes;
starting research names a completed `modelDiscoveryId` from the CEO's bound
runtime, and the initial request also names that founder's CEO
`conversationId` (`initialization_conflict` otherwise). Only advertised Luna is
accepted. Reasoning is disabled where supported, otherwise the lowest known
advertised level is used, or the runtime default when none is known. No saved
Agent model is changed. Repeating the request reuses the existing Run; only a
failed Run, after its automatic retries are exhausted, can be restarted, with
`retry: true`, which starts a new research Run and archives the founder's
failure notice about the previous one. The new Run updates the pages earlier
attempts wrote instead of writing copies.

The client calls POST once and then moves on: the Run works in the background
and writes linked Wiki pages with `wiki_write` ([Agent writes](wiki.md#agent-writes)).
When the Run reaches its final state the server, in the same transaction,
sets the receipt's `completed_at` and writes the founder's Inbox notice. It is
an ordinary `run_completed` or `run_failed` item with its own copy ("Your
company wiki is ready" with the page count, or "Company research could not
finish") and a deep link into the Wiki: `/wiki/pages/<overviewPageId>`, or
`/wiki` when no overview page exists and for a failure. An automatic retry
of a failed attempt notifies nothing; only the final attempt does.

`Company.initializationPending` is true only while the founder's receipt exists
and the research has not been started (no research Run yet); it is false while
the research runs and after it ends. Until the research is started, only the
founder's CEO conversation refuses new sends with
`conversation_initialization_pending`; once the Run exists a message queues
behind it. A bounded background reconciler is only a recovery: it completes a
receipt whose research Run is final but whose terminal path could not (rechecking
the founder's current membership), releases the conversation's preserved queue,
and never starts discovery, a Run or a retry. Both endpoints require
`agents.manage` and a human actor; another member cannot read or start the
founder's research.
