# ADR 0017: Project access scopes: per-person permission grants on a Project

Status: Accepted. Date: 2026-09-28.

Supersedes an earlier product limit that ruled out private Projects and
Project-specific roles, and the line of
[ADR 0012](0012-roles-assign-permissions-enforce.md) that calls Projects
"ownership containers, not membership scopes". ADR 0012's rejection of a
policy engine stands. Builds on
[ADR 0014](0014-private-runtimes-and-agent-bindings.md): a Run works for one
member, its authorizer, on that member's own computer.

## Context

A Company member could read every Project, every Task on every board, every
Task chat and every Project file. The only per-object access in the product
was on Artifacts (owner, person grants, conversation grants, visibility) and
on chats (membership). Customers asked for enterprise access control: a
person should reach only certain Projects, boards, shared files and chats,
and hold only some permissions on each. The concrete rule: in a chat, a
person, or an Agent working for them, may read a shared file only
when they have permission on Project A and the file is one of Project A's
files.

Authorization engines were studied and fact-checked (relationship engines,
policy engines, in-process matchers). Every one keeps grants in a second
store or in memory, so grants and content would no longer commit together,
and none returns a SQL predicate for the keyset-paginated lists every
Zenith Team screen reads. Zenith Team already decides Artifact access inside
the read statement and already sends opaque per-person invalidations. What
was missing was a general model and one place for the rule.

## Decision

1. **Roles stay `owner | admin | member`, Company-wide, and nothing else.**
   There are no Project roles and no named bundles in the API or the schema.
   Access on a Project is a set of permissions granted to one person, the
   same shape as `output_grants`. Team › Project access offers four access
   levels (Can view tasks, Can edit and run tasks, Can approve tasks, Can
   manage project) as shortcuts of that view only: picking one stores its
   permissions as the person's rows, and a set that matches no level reads
   as Custom access, edited one permission at a time. This replaced an
   earlier per-permission matrix on the Project page.
2. **A closed Project permission catalog** lives in
   `internal/auth/project_permission.go`, mirrored by a CHECK and pinned by a
   test: `project.read`, `project.tasks.write`, `project.tasks.execute`,
   `project.files.manage`, `project.approvals.resolve`,
   `project.results.review`, `project.visibility.update`,
   `project.settings.update`, `project.grants.manage`, `project.archive`.
   `project.read` is stored whenever any other permission is granted, and
   the schema refuses any other row without it (`project_grants_read_fkey`).
3. **Projects are restricted by default.** A new Project starts `restricted`
   and its creator holds every permission. A Project can be opened to the
   Company (`company`), which gives every active member a floor of
   `{read, tasks.write, tasks.execute}`; explicit grants add on top, and
   removing someone needs `restricted` first. Restricting a Project again
   leaves each person exactly their explicit grant. The app does not offer
   opening a Project to the Company: people are added one by one, or all at once with Select all, each at Can edit and
   run tasks unless changed. The API keeps the setting, and a Project
   already open to everyone shows an Everyone chip on Team › Project access
   that limits it to the people added.
4. **Management is not content.** Company `projects.manage` (owners, admins)
   grants settings, grants and archive on every Project, including one the
   holder cannot read, and nothing else. It never grants reading, files,
   deciding or visibility. Beyond a Project's metadata and grants it reads
   one thing: the access report, which names the members with open Tasks
   in a restricted Project, or who ran work there in the last 30 days, but
   cannot read it (never the Task, chat or Run). A holder who needs content grants themselves
   permission; **Join** grants exactly `project.read`. Every grant change,
   self-grants included, is an Activity row readable by the Project's
   readers and the Company's `projects.manage` holders. Visibility is not
   bridged: only someone who can see a Project decides who else sees it.
5. **Inside a Project the Project permission is the only check.** Task
   work, Task chats, Project files, Task-origin approvals and questions and
   Task deliverable reviews ask the Project catalog, never the Company role.
   One ADR 0014 rule is layered on top: handing a Task to another member
   still needs `tasks.assign` or being the current assignee, and the new
   assignee must be able to read the Project.
   A Company Plan works the same way. An apply changes only the Projects
   where its applier holds `project.tasks.write` and that the plan's reader
   could change when they read it. Archiving a Project, or leaving it out
   of a plan while it has open Tasks, also needs `project.archive`, and
   renaming it `project.settings.update`; without them that Project is left
   as it is and reported under `skipped` while the rest applies. So is a
   Project the plan would change where the applier lost
   `project.tasks.write` or `project.read` since the plan was read. A
   Project the applier cannot read is never reached, so leaving its Tasks
   out of a plan deletes nothing. On another member's open Task a plan keeps the ADR 0014 rules
   of a Task PATCH: giving it another executing Agent, taking it out of
   review or deleting it needs `tasks.assign` or being its assignee. A plan
   raised in a Task chat reaches that chat's Project alone, and approving
   it needs `project.approvals.resolve` and `project.tasks.write` there and
   no Company permission. A plan a person applies themself, or one raised
   outside a Task chat, is still a Company action under
   `company.plan.apply`.
6. **Invisible means absent.** An object a person cannot read answers 404 on
   every surface: REST, lists, counts, search, inbox, activity, realtime
   frames and downloads. 403 `project_permission_required` only when the
   Project is visible to the caller.
7. **The rule is stated once, in the database, inside the read.** Views
   `project_access`, `task_access`, `conversation_readers` and
   `attachment_access`, and one `LANGUAGE sql STABLE` function
   `project_permission_held`, carry it. Lists read a view in the same
   statement, one lookup per row. Task writes check the permission under
   the Project lock, or through the function where a chat lock is already
   held, and the Task write statements call the function themselves;
   decisions (approve, answer, review) run under the Company lock that every
   access change also takes. A shape test fails when a query that binds an
   actor reads a guarded table without a view.
8. **A Project file is visible only to that Project's readers, wherever it
   was shared.** A file with an active `project_files` row is absent from a
   chat message for anybody who cannot read the Project, in the list and in
   the realtime frame. A chat upload that is in no Project follows the
   chat's members. Once a file is retired from every Project it is a plain
   chat upload again, which can widen who sees it.
9. **Agents hold no permissions of their own.** An Agent stays a Company
   template (ADR 0013, ADR 0014). Every Run works for one member and the
   server checks that member's Project permissions at enqueue, claim,
   download and every Run-proof read. Restricting a person restricts every
   Agent working for them.
10. **A chat bounds what an Agent may read.** A Run started by a mention in a
    channel or a group direct message may read Project A only when every
    active human member of that chat can read A. Otherwise the server refuses
    with the fixed "own Tasks only" sentence and names nothing. A Task chat's
    audience is exactly its Project's readers, so a Task Run reads and files
    work only in that Project, not even the member's own Tasks elsewhere; the
    general channel works only on Company-visible Projects. A Huddle's voice
    assistant is bounded the same way by the Huddle chat, whose members hear
    it: its Project list, Task search and work status cover only Projects
    every active member of that chat can read, and nothing while the chat
    has no member.
11. **Losing access stops the work.** A grant or visibility change that
    removes `project.read` or `project.tasks.execute` from a person cancels
    their queued and live Runs in that Project (`project_access_revoked`),
    deletes their Run tokens and stops frames reaching them; losing
    `project.read` or `project.tasks.write` also removes the messages they
    queued in its Task chats. Leaving the Company deletes every grant, and
    their Runs end with their runtimes (`runtime_owner_inactive`). A Company
    rejoin restores no old grant.
12. **Artifacts keep their own sharing,** with one change: an Artifact made
    by a Task in a restricted Project is Company-visible only to that
    Project's readers. Person and chat grants keep working, so an Artifact
    shared into a channel stays visible to the whole channel while a Project
    file shared there does not.

## Alternatives and tradeoffs

- **Adopt an authorization engine.** Relationship engines keep grants in
  their own tables and cap or page their list calls, so every list would
  still need our own SQL; grants and content would commit separately; a
  second service or an undocumented embedded mode would need operating. A
  policy sidecar that compiles filters avoids the second store but puts the
  most security-critical predicate behind a young planner and a network hop.
  Rejected, as in ADR 0012.
- **Project roles (a lead, a contributor, a viewer).** The industry's usual
  shape binds named bundles at a scope. New roles are ruled out: the
  Company has three and permissions are granted directly. The risk is grant
  sprawl across many Projects; the Access page's levels and "copy access
  from a person" mitigate it without storing a level, and Company-defined
  presets remain a later one-table change.
- **Open by default, members backfilled.** Zero disruption, but it keeps
  every existing Project readable by everyone until someone restricts it.
  Rejected in favour of restricted by default (below).
- **Let Company owners read every Project.** Simpler for small teams, but it
  turns a role into content access and makes a restricted Project mean less
  than a private channel. Rejected: management is not content.
- **Let an Agent use the caller's access in a shared chat.** Simpler, but an
  Agent could summarise a restricted Project into a chat whose other members
  cannot read it. Rejected in favour of the audience bound.

## Consequences

Existing Projects become `restricted`; every active owner holds every
permission on each, and nobody else holds any. Tasks assigned to other
members become invisible to their assignees until an owner grants them
(assignments are kept); admins lose deciding, reviewing and file management
until they are granted or grant themselves. The migration itself cancels
every Run in a Task chat that had not ended and whose authorizer can no
longer run work in that Project, or whose Task chat has no Project
(`project_access_revoked`, tokens deleted, the Task taking the cancelled
Run's end state), and addresses pending Task-chat Approvals and Questions
again to the Project's deciders (every active owner when there is none),
archiving the other copies. It also removes the messages and prompts queued
in Task chats by people who may no longer post there, freeing their files
and archiving the notices about them. It writes no frames or Activity for
these.
The access report (`GET /projects/access-report`) lists the members left
outside restricted Projects where they hold open Tasks or recently ran
work, and the Managed projects view is where owners repair access.

Accepted residual risks: a file retired from every Project becomes readable
by the chat it was shared in; Task chats whose Task was deleted before this
change (no Project recorded) stay Company-readable. The CHECK
`conversations_task_project_check` admits a missing Project only on a chat
whose Task is gone and that predates the migration, so a live Task's chat
cannot lose its Project; the pocket stays that size because no statement
writes `project_id` back to NULL and no insert backdates `created_at`. Task
reports keep flowing Agent to Agent and are filtered when delivered to a
person; an Agent process still runs beside its member's own daemon
credential, as before, and every route a Run proof reaches is bounded on the
server. A realtime frame's audience is read in the producing transaction
without a Project lock, so a revocation that commits in that window can
still deliver that one frame, content included, to the person who just lost
access; `project.access_changed` makes their client refetch but cannot take
it back. In a Task Run the root Agent's `company_plan_export` still reads
every Project its member reads, while the Approval it raises shows only the
Task's Project to that Project's readers; the Agent could repeat other
Projects into the Task chat. The export reads with the member's own session,
so this is what the member could show there themself, not a widened read.
Analytics keeps Company-wide totals for `usage.read` holders and names
only the Projects the viewer reads, so the totals minus the named rows give
the combined tokens, cost and Run counts of the Projects the viewer does not
read; when only one such Project has Runs in the window, those are its
numbers, though never its name, Tasks or chats.
Groups, guests, Company-defined presets, grant expiry, an access history
table, SSO and SCIM remain later work; each is an added arm or table, not a
rewrite.

## Later changes

- Groups: a named set of members in one Company that holds Project grants
  like a person does.
- `project.results.review` was removed from the catalog, because nothing checked it once result review was
  retired. The catalog now has nine permissions.
