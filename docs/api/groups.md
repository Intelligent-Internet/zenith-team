# Groups

A Group is a named set of Team members in one Company that can be given a
Project in one step. Groups are flat and hold people only: no Agent, no
Group inside a Group. A Group never acts and never starts a Run; it only
holds Project grants, and every member holds them too. A person's access on
a Project is the union of their own grant, each of their Groups, the
Company-visible floor and the management permissions of their role, so
leaving a Group never takes what their own grant gives. See
[OpenAPI](spec/groups.yaml) and Access TECH Part D.

| Method | Path under `/api/v1/companies/{companyId}` | Purpose | Needs |
| --- | --- | --- | --- |
| GET | `/groups` | List the Groups. | an active membership |
| POST | `/groups` | Create a Group with `name` and optional `description`. | `members.manage` |
| GET | `/groups/{groupId}` | Read one Group with its members. | an active membership |
| PATCH | `/groups/{groupId}` | Rename or redescribe with `expectedRevision`. | `members.manage` |
| DELETE | `/groups/{groupId}` | Delete with `expectedRevision`. | `members.manage` |
| POST | `/groups/{groupId}/members` | Add and remove members in one change. | `members.manage` |
| POST | `/groups/{groupId}/preview` | What a change of members, or a delete, would do. | `members.manage` |

What a Group holds on a Project is set on the Project:
`PUT|DELETE /projects/{projectId}/group-grants/{groupId}` and its preview
([projects.md](projects.md#group-grants)).

Every Group route is a person's: a request carrying a Run proof is refused
with `403 human_actor_required`. Owners and admins hold `members.manage`;
everyone else reads.

## A Group

```json
{
  "id": "…",
  "name": "Finance",
  "description": "Payroll and month-end close",
  "memberCount": 2,
  "revision": 5,
  "createdAt": "2026-10-06T09:00:00Z",
  "updatedAt": "2026-10-06T10:00:00Z",
  "projects": [
    { "projectId": "…", "permissions": ["project.read"] }
  ],
  "members": [
    { "userId": "…", "addedBy": "…", "addedAt": "2026-10-06T10:00:00Z" }
  ]
}
```

`GET /groups` answers `{items}` with the same fields but `members`.
`projects` lists what the Group holds on the Projects you can see: those you
read, or every Project when your role holds `projects.manage`. `memberCount`
counts its active Team members. `revision` is what a change sends back as
`expectedRevision`.

## Names

A name is 1 to 80 characters after trimming and unique in the Company,
ignoring case; a description is at most 500 characters. A name in use is
`409 group_conflict`; text out of bounds is `422 validation_failed`. A
Company holds at most 100 Groups (`409 group_limit_reached`).

## Changing members

`POST /groups/{groupId}/members` is the one way to change who is in a Group:

```json
{ "addUserIds": ["…"], "removeUserIds": ["…"], "expectedRevision": 5 }
```

At most 500 ids in all; an id in both lists is `422 validation_failed`. Only
active Team members with an enabled account may join (`409
group_member_required`; an Agent's id included, and the answer names
nobody). Adding someone already in, or removing someone not in, changes
nothing for them, and a change that adds or removes nobody writes nothing. A
Group holds at most 500 members (`409 group_limit_reached`). A stale
`expectedRevision` is `409 group_conflict`. The answer is the Group as `GET`
answers it.

On every Project the Group holds, the change moves access exactly as a
person grant change would:

- The Project's access revision advances, and `project.access_changed` goes
  to its readers before and after, the `projects.manage` holders and you.
- The Project gets one `project_grant.group_member_added` Activity row for
  the people added and one `project_grant.group_member_removed` row for the
  people taken out, each `{projectId, projectTitle, groupId, groupName,
  userIds, count}` naming every person whose stored access there moved. The
  title and the name are as they were at the change, so the row still reads
  after the Group is deleted. Someone whose own grant already
  holds everything the Group gives is not named; a side where nobody's
  access moved writes no row. What each person now holds is on the Project's
  access list, not in the row.
- Whoever loses `project.read` or `project.tasks.execute` there has their
  Runs there ended (`project_access_revoked`); whoever loses read or
  `project.tasks.write` has their queued messages there removed.
- When an addition lets people read a restricted Project they did not read,
  an admin adding themself included, every person who manages that
  Project's access (by their own grant, a Group or their role, so owners and
  admins too) gets one `project_access_granted` Inbox item for that Project,
  except you and, when only one person can now see it, that person
  ([inbox.md](inbox.md)).

"No laundering": you must manage access on every Project the Group holds,
and when you do only through your own grant, the Group's set there must sit
inside yours. Otherwise the change, or a delete, is `403
group_grant_permission_required`, which names no Project.

## Preview

`POST /groups/{groupId}/preview` takes the members body without
`expectedRevision`, or `{"addUserIds": [], "removeUserIds": [], "delete":
true}`, and answers what the save would do, without writing or locking:

```json
{
  "revision": 5,
  "projects": [
    {
      "projectId": "…",
      "people": [
        { "userId": "…", "before": [], "after": ["project.read"], "runsStopping": 0 },
        { "userId": "…", "before": ["project.read", "project.tasks.execute"], "after": [], "runsStopping": 1 }
      ]
    }
  ],
  "peopleAffected": 2,
  "runsStopping": 1,
  "openedForCaller": [],
  "projectsHidden": false
}
```

Each Project lists the people whose access there moves, with what they hold
before and after (the management permissions of a role left out) and how
many of their Runs there stop. `openedForCaller` lists the restricted
Projects the change lets you read. A Project you cannot see is left out with
its people and Runs, and `projectsHidden` says so. Save with `revision`; when
the Group changed in between, the save is `409 group_conflict` and nothing
is applied. The preview refuses what the save would refuse.

## Deleting

`DELETE /groups/{groupId}` takes `{expectedRevision}` and answers `204`.
Deleting is the only way to retire a Group. Every member loses what only this
Group gave them; each Project it held records the Group's grant going
(`project_grant.group_changed` with `after: []`), and the Runs and queued
messages of whoever lost access there end as above.

## Leaving the Company

A member who leaves the Company, is removed or deletes their account leaves
every Group; each Group's revision advances. A rejoin restores none.

## Activity and frames

Group rows (`group.created`, `group.updated`, `group.deleted`,
`group.member_added`, `group.member_removed`, payload `{groupId, name,
userId?}`) name no Project, and every active member reads them, as they read
the Team page. `group.changed {groupId, revision}` and `group.deleted
{groupId}` go to the whole Company; refetch `GET /groups`
([realtime.md](realtime.md)).
