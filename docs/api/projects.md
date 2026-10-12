# Projects

Projects belong to a Company. Who may do what on a Project is a set of
permissions granted to each person on that Project
([ADR 0017](../adr/0017-project-access-scopes.md)); Company roles stay
`owner | admin | member` and never open a Project's content on their own. All
writes recheck current membership and Project access inside the transaction.
See [OpenAPI](spec/projects.yaml).

| Method | Path under `/api/v1/companies/{companyId}` | Purpose | Needs |
| --- | --- | --- | --- |
| GET | `/projects?include_archived=false` | List the Projects you read; set `include_archived=true` to include archives. | an active membership |
| GET | `/projects?view=manage` | The Managed projects view: Projects you manage but cannot read, metadata only; `include_archived=true` includes archives here too (the app sends it, so Activity and Team › Project access name them). | `projects.manage` |
| POST | `/projects` | Create with immutable `key`, `title`, optional `description` and `files`. | `projects.manage` |
| GET | `/projects/access-report` | People outside restricted Projects they were working in. | `projects.manage` |
| GET | `/projects/{projectId}` | Read one Project, including an archived one. | visible |
| PATCH | `/projects/{projectId}` | Edit `title`/`description` with `expectedRevision`, and `visibility` with `expectedAccessRevision` too. | `project.settings.update` / `project.visibility.update` |
| POST | `/projects/{projectId}/archive` | Archive with `expectedRevision`. | `project.archive` |
| PUT | `/projects/{projectId}/files` | Replace the logo and documents with `expectedRevision`. | `project.files.manage` |
| GET | `/projects/{projectId}/grants` | List the people and Groups with access, and where each person's comes from. | visible |
| PUT | `/projects/{projectId}/grants/{userId}` | Set one person's permissions. | `project.grants.manage` |
| DELETE | `/projects/{projectId}/grants/{userId}` | Remove one person's permissions. | `project.grants.manage` |
| POST | `/projects/{projectId}/grants/{userId}/preview` | What a person's grant change or removal would do. | `project.grants.manage` |
| PUT | `/projects/{projectId}/group-grants/{groupId}` | Set one Group's permissions. | `project.grants.manage` |
| DELETE | `/projects/{projectId}/group-grants/{groupId}` | Remove one Group's permissions. | `project.grants.manage` |
| POST | `/projects/{projectId}/group-grants/{groupId}/preview` | What a Group grant change would do. | `project.grants.manage` |
| POST | `/projects/{projectId}/join` | Grant yourself `project.read`. | `projects.manage` |

## Permissions and visibility

The Project catalog is closed: `project.read`, `project.tasks.write`,
`project.tasks.execute`, `project.files.manage`, `project.approvals.resolve`,
`project.visibility.update`, `project.settings.update`,
`project.grants.manage`, `project.archive`. `project.read` is always stored
beside any other permission. A name outside the catalog is refused with
`422 project_permission_unknown`, whose `detail` names it, including
`project.results.review`, which was removed on 2026-10-08 because nothing
checked it; every grant that held it lost that one permission. A desktop
built before then still sends it in its Approve and Manage presets and is
told which permission it was.

A Project is `restricted` (the default) or `company`. On a restricted Project
only people with a grant read it. On a Company-visible Project every active
member reads it and holds `project.read`, `project.tasks.write` and
`project.tasks.execute` without a grant. A grant there only adds on top of
that floor, and removing a person is refused while the Project stays
Company-visible. Making the Project restricted again leaves each person
exactly their stored grant.

Company `projects.manage` holders (owners, admins) hold
`project.settings.update`, `project.grants.manage` and `project.archive` on
every Project, including one they cannot read. That is management, not
content: without a grant they see the Project's title, description,
visibility and their `access`, and no files. Files, deciding and visibility
always need an explicit grant. Join grants exactly `project.read`; anything
more is an explicit grant, and every grant change, your own included, is
recorded in Activity.

A new Project starts restricted, and its creator holds every permission.

Every Project carries the caller's own `access`:

```json
{
  "visibility": "restricted",
  "access": {
    "permissions": ["project.archive", "project.grants.manage", "project.read", "project.settings.update"],
    "management": true,
    "revision": 4
  }
}
```

`access.permissions` is the server's verdict; gate the UI on it and never
derive it from a role. `access.revision` is the Project's access revision:
send it as `expectedRevision` on grant changes and Join, and as
`expectedAccessRevision` with a visibility change. `files` is absent
when you manage the Project without reading it.

## Answers

- A Project you cannot see answers `404 project_not_found`, exactly like a
  missing one, on every route under `/projects/{projectId}`.
- A visible Project without the permission answers `403
  project_permission_required`. On the routes that need one fixed Project
  permission (archive, files, and a grant's PUT and DELETE) it lands before
  the body is read. Reading the grant list needs only a visible Project, and
  Join checks no Project permission: it answers `403
  company_permission_required` first, before the body is read.
  PATCH needs a different permission per field (`project.settings.update`
  for title and description, `project.visibility.update` for visibility), so
  it reads the body first: a visible caller who sends a malformed body gets
  `400 invalid_request` or `422 validation_failed`, and a well-formed body
  without the permission gets the `403` before any revision is compared. The
  `404` for a Project you cannot see still comes before the body is read.
- Creating a Project, the manage view, the access report and Join check the
  Company permission first (`403 company_permission_required`).
- Grants, visibility, files, archive, Join, the manage view and the access
  report are never reachable with a Run proof (`403 human_actor_required`).
- Only `GET /projects` takes query parameters. Every other Project route,
  the access report included, answers any query string with `422
  validation_failed` ([conventions.md](conventions.md)); it is checked after
  Company membership and before the Project checks above.

## Reads from inside a Run

`zen project list` and `zen project get` reach `GET /projects` and `GET
/projects/{projectId}` through a Run's channel, with the member's session
and the Run's `X-Boardly-Run-Token`. Only a Run outside a task is offered
them (and not when its agent's Company planning is off). A task Run is
offered no Project list and no read of another Project: its channel refuses
those with `run_scope_denied` (exit code `3`) before they reach the server.
The one Project read a task Run may send is of its own task's Project: the
lookup `zen task create` makes, which also lets `zen project get` with that
Project's key or id through, while the agent's Work coordination is on.
A token that does not prove a running
Run in this Company, a Run with no authorizing member, or a token sent with
any session other than that member's is `401 run_token_invalid`. `GET
/projects/{projectId}` first checks that the caller can see the Project at
all, so a Project they cannot see stays `404 project_not_found` whatever the
token. Then:

- A Run reads a Project only as its member's **reader**. A Project the
  member only manages through `projects.manage` (the metadata view above) is
  `404 project_not_found` to the Run and absent from its list: management is
  not content, and a Run never manages.
- A task Run answers into its task chat, which only its Project's readers
  read, so the server bounds its reads to that Project: `GET` of it answers
  as usual and of any other Project `403 task_outside_run_scope`. The
  channel never sends a task Run's list, so the same bound on `GET
  /projects` (any other Project left out) is only a second guard.
- A Run started by a mention in a channel or a direct message between
  people uses the access of the person who mentioned the Agent, like any
  Run outside a task (D31): it lists and reads the Projects that person
  reads, whoever else is in the chat. Everyone in the chat reads its
  answer, so that person is responsible for what it shares. A Project
  that person cannot read is `404 project_not_found` and left out of the
  list, as for them.
- Where the server refuses a Run with `403 task_outside_run_scope`, the
  body carries the fixed sentence "Permission denied. You can only access
  your own Tasks.", which names nothing. `zen project get` prints that
  sentence as it stands, like `zen task get`, with no request line in
  front.

## Editing a Project

`PATCH` carries `title`, `description` and `visibility`; title and
description need `project.settings.update`, visibility needs
`project.visibility.update`. `expectedRevision` is the Project `revision`. A
visibility change is also an access change, so it names the `access.revision`
it was decided on in `expectedAccessRevision`, sent with `visibility` and only
then (`409 project_grant_revision_conflict` when it moved). `PATCH` no longer
carries `files`: a body naming them is refused as `400 invalid_request`.
Replace files with `PUT /projects/{projectId}/files` and `{logoId,
documentIds, expectedRevision}` (`409 project_conflict` when the Project
`revision` moved); an unchanged selection writes nothing. A files change
advances the Project `revision`.

Making a Company-visible Project restricted cancels the Runs of members who
held it only through the floor (`run.cancelled`, reason
`project_access_revoked`).

## Grants

`GET /projects/{projectId}/grants` lists every person with a stored source on
the Project (their own grant, a [Group](groups.md) they are in, or both) and
every Group holding a grant there:

```json
{
  "items": [
    {
      "userId": "…",
      "granted": ["project.read", "project.tasks.write"],
      "effective": ["project.read", "project.tasks.execute", "project.tasks.write"],
      "sources": [
        { "via": "grant", "permissions": ["project.read", "project.tasks.write"] },
        { "via": "group", "groupId": "…", "permissions": ["project.read", "project.tasks.execute"] }
      ],
      "grantedBy": "…",
      "updatedAt": "2026-09-28T10:00:00Z"
    }
  ],
  "groups": [
    {
      "groupId": "…",
      "name": "Finance",
      "granted": ["project.read", "project.tasks.execute"],
      "grantedBy": "…",
      "updatedAt": "2026-09-28T09:00:00Z",
      "memberCount": 4
    }
  ]
}
```

`granted` is the person's own stored rows, the rows the person grant editor
changes; it is `[]` (and `grantedBy` is `null`) for someone who reaches the
Project only through a Group. `effective` is the union of every source:
their rows, what their Groups hold on the Project, the Company-visible floor
and the management permissions of the person's role. `sources` lists them
one by one, in that order: `grant`, `group` (with `groupId`), `company`,
`management`. `updatedAt` is the latest change of any stored source. People
who read only through the floor are not listed: on a Company-visible
Project, say "everyone in the Company" and disable Remove.

`PUT /projects/{projectId}/grants/{userId}` takes `{permissions,
expectedRevision}` and makes the person's rows exactly that set. A non-empty
set without `project.read` is accepted and `project.read` is added; an empty
set removes the person. It answers `{grant, revision}`: the person's entry
after the change, as the list states it (`null` when no stored source
remains), and the new access revision.
Refusals: `409 project_grant_revision_conflict` for a stale
`expectedRevision`, `409 project_member_required` for someone who is not an
active member, `409 project_visibility_floor` for an empty set on a
Company-visible Project, `422 project_permission_unknown` for a permission
outside the catalog (the `detail` names it), `422 validation_failed` for a
malformed `userId`. On a Company-visible Project the set only adds on top of
the floor. Sending the set a person already holds changes nothing: no
revision, no Activity, no frame, and `grantedBy` and `updatedAt` stay as they
were.

`DELETE /projects/{projectId}/grants/{userId}` takes `{expectedRevision}` and
removes every row the person holds; it is refused on a Company-visible
Project (`409 project_visibility_floor`): make it restricted first. Removing
someone who holds no rows, a non-member included, changes nothing and answers
`{grant: null, revision}`. Access adds up: someone a Group still gives the
Project keeps it, and the answer is their entry with `granted: []` and
`sources` naming the Group.

**The self-grant cap.** Someone who manages a Project's access only through
their own grant of `project.grants.manage` (their `access.management` is
false) can give only what they hold there. A `PUT` whose set holds a
permission missing from the caller's `access.permissions` is refused with
`403 project_grant_exceeds_own`. So is a `PUT` or `DELETE` on a person whose
own stored grant (`granted`) holds one: they cannot change someone who holds
more. Only that stored grant counts, not what the person also holds through
their Company role (`effective`). On a Project open to the Company the
caller's set includes what everyone holds there. Sending the set the person
already holds is checked the same way. The answer names no permission.
Owners and admins manage every Project through the Company permission
`projects.manage` (`access.management` is true) and are not capped, and Join
is not checked.

A grant change that takes `project.read` or `project.tasks.execute` from a
person cancels their queued and running Runs in the Project (reason
`project_access_revoked`); their Tasks stay assigned to them until someone
holding `tasks.assign` who reads the Project hands them over. A person who
keeps `project.read` is told in their Inbox (`run_failed`, one item per
stopped Run, never naming the Project); a person who lost read is not.
When a member leaves the Company or deletes their account, every grant they
held is deleted and their Group memberships end; each Project they reached,
by their own grant or through a Group, advances its access revision and
records one `project_grant.changed` row with what they held there from every
stored source and nothing after. A rejoin restores none of it.

`POST /projects/{projectId}/grants/{userId}/preview` takes `{permissions}`
(an empty set previews a removal) and answers `{revision, runsStopping,
losesProject}` without writing: how many of the person's Runs in the Project
the change would stop, counted as the Group preview counts them (they lose
`project.read` or `project.tasks.execute`), and whether they would no longer
read the Project. What their Groups give, the Company-visible floor and
their role stay as they are. `revision` is the access revision to save with.
The app asks before a save only when `runsStopping` is above zero or
`losesProject` is true. It needs `project.grants.manage` and refuses what the
save refuses before it writes: the self-grant cap (`403
project_grant_exceeds_own`), a permission outside the catalog (`422
project_permission_unknown`) and a removal on a Company-visible Project (`409
project_visibility_floor`); whether the person is an active member is left to
the save.

`POST /projects/{projectId}/join` takes `{expectedRevision}` and grants the
caller exactly `project.read`. A caller who already reads the Project changes
nothing.

### Group grants

A [Group](groups.md) holds permissions on a Project as one person does, from
the same catalog and with the same levels, and every member holds them too.
`PUT /projects/{projectId}/group-grants/{groupId}` takes `{permissions,
expectedRevision}` and answers `{grant, revision}`, `grant` being the Group's
entry of the list's `groups` (`null` when it holds nothing). The rules are a
person grant's: `project.read` is added to a non-empty set, an empty set or a
`DELETE` (`{expectedRevision}`) removes the Group, both are refused on a
Company-visible Project (`409 project_visibility_floor`), sending the stored
set changes nothing, an archived Project stays manageable, and the self-grant
cap applies with the Group's stored set as the grant changed (`403
project_grant_exceeds_own`). A Group not in the Company, or a malformed
`groupId`, is `404 group_not_found`; a Project holds at most 50 Group grants
(`409 group_limit_reached`). A change advances the access revision, is
recorded as `project_grant.group_changed` (the Group's set before and after),
and ends the Runs of members who lose read or run access there. It sends no
Inbox notice.

`POST /projects/{projectId}/group-grants/{groupId}/preview` takes
`{permissions}` and answers what the save would do without writing: the
members whose access moves, before and after, the Runs that stop, and
whether it opens the Project to you (the shape of the Group preview, with
`revision` the Project's access revision to save with). It refuses what the
save would refuse, a permission outside the catalog included (`422
project_permission_unknown`).

## Access report

`GET /projects/access-report` shows owners and admins who was left outside
work they were doing. For each restricted Project that is not archived it
lists the active members who cannot read it but are the assignee of one of
its open Tasks (`task_assignee`) or authorized a Run in one of its task chats
that was active since `since`, the last 30 days (`run_authorizer`). A Run
counts from its start or its last change, so one queued long ago and
cancelled when access was lost (by a restriction or by the upgrade below)
still counts:

```json
{
  "since": "2026-08-30T10:00:00Z",
  "items": [
    {
      "projectId": "…",
      "key": "website",
      "title": "Build website",
      "people": [{ "userId": "…", "reasons": ["run_authorizer", "task_assignee"] }]
    }
  ]
}
```

Projects are sorted by key and people by id; a Project where nobody is
outside their work is left out, and `items` is empty when there is none. The
report names people and Projects only, never a Task, chat or Run. It does
tell a manager something the Project list and the grants do not: who has open
work, or ran work, in a Project that manager may not read. That is the one
thing a manager learns about a Project beyond its metadata and grants.
Give someone access with
`PUT /projects/{projectId}/grants/{userId}`, or hand their Tasks to someone
who reads the Project.

## Upgrading to Project access

The migration that introduced Project access made every existing Project
restricted and gave every active owner every permission on it, nobody else
anything. In the same migration:

- Every Run in a task chat that had not ended (queued, deferred, dispatched,
  waiting for its folder or running) and whose member may no longer run
  tasks there was cancelled with `error` `project_access_revoked`, and its
  Task took the cancelled Run's end state. So was every such Run in a task
  chat whose Task was deleted before the upgrade.
- Pending Approvals and Questions raised in a task chat were addressed again
  to the people who may now decide them (the Project's readers holding
  `project.approvals.resolve`, at that point its owners); other people's
  copies were archived. A request nobody could decide went to every active
  owner, who sees it after joining the Project.
- Messages and prompts queued in a task chat by someone who may no longer
  post there were removed (reason `project_access_revoked`), their files
  were freed and the notice they had sent the assignee was archived.

The access report lists the people this left outside their work.

## Keys, archive and events

Keys use lowercase snake_case, 1–64 ASCII characters, unique per Company even
after archive. Task creation requires Project identity, and no update can move
a Task. Project UUIDs identify future managed repository paths.

Archive refuses Tasks outside `done`/`cancelled`, nonterminal Task Runs and pending
or executing merges. It preserves Tasks/files and prevents new or reopened work.
Archive is final: no route reopens a Project.
Stale revisions and duplicate keys return `project_conflict`; archive blockers
return `project_open_tasks`; changes to an archive's settings, visibility or
files return `project_archived`. Access stays manageable on an archive:
granting, changing or removing a person's permissions and Join still work, so
an owner can take someone off an archived Project or let a reader into its
history.

The `project.created`, `project.updated` and `project.archived` realtime
events publish after commit to the Project's readers and the members holding
`projects.manage` (the whole Company for a Company-visible Project). They
carry the Project's identity and settings only, never its files or anyone's
`access`, so clients refetch the Project rather than apply the frame. Their
Activity rows are written in the same transaction, name the Project in
`project_id` and carry `{id, key, title, changed}`, never files or anyone's
access; the feed shows them only to the Project's readers. Grant, Join and
visibility changes are `project_grant` rows (`project_grant.changed`,
`project_grant.joined`, `project_grant.visibility_changed`), filterable with
`entity_type=project_grant`, shown to the Project's readers and the members
holding `projects.manage`. `project.access_changed`
(`{projectId, revision}`) reaches the people whose view of the Project changed;
refetch the Project, and drop it on a 404. See [Realtime](realtime.md). Task
snapshots include `projectId`.

This API does not provision a local or remote repository. YAML v5 and daemon
Project routing remain subsequent implementation steps.
