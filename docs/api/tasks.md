---
title: Tasks
summary: The company plan, tasks, dependencies, and the task workspace
---

Work is organized as Company Tasks. Every Task names its assignee — always a
member, responsible for running it on their own computer — who it is
`executedBy` (an agent, or the assignee themself), and its milestone type,
and owns the Conversation where execution is discussed and streamed
([ADR 0014](../adr/0014-private-runtimes-and-agent-bindings.md)).

Every route is company-scoped. Task routes require an active member;
Company Plan application needs `company.plan.apply` (owner, admin), except
that a plan a Task Run raises in its Task chat is approved inside that
Project (see below). A non-member sees `404`.
Task collections return all matching rows and mutations use `expectedRevision`.

## Apply a company plan

```
POST /api/v1/companies/{companyId}/company-plan/apply
```

With only the ordinary bearer credential, this applies a whole Plan document
— projects, tasks and dependencies — in one transaction. Gate:
`company.plan.apply` (owner, admin).
The body has a `base` that is the current plan as the client read it: every
Project the caller reads with its id, key and revision; every Task in them
(including done, cancelled and failed ones) with its id, key and revision;
and every dependency edge with its revision. `conversationId` on a base Task
is optional and ignored by the comparison.

The apply reaches only the Projects the caller may change
([ADR 0017](../adr/0017-project-access-scopes.md)): those listed in `base`,
not marked `readOnly`, where they hold `project.tasks.write`. There, Tasks are
created, updated and deleted by omission as the plan says.

- A Project the caller only reads is left alone with its Tasks, whatever the
  plan says about it. The export marks it `read_only: true` (`readOnly` in the
  artifact's `base` and `desired`), and the Company Plan tools refuse a plan
  that changes it. The apply leaves a Project marked that way alone even if
  the caller may change it by the time they apply: the plan was made without
  changing it.
- A Project the caller cannot read is never reached: leaving its Tasks out
  deletes nothing. Archiving such a key is the same `422 validation_failed` as
  archiving a key nobody has used. Creating a Project under a key that is
  taken, readable or not, is `409 work_plan_conflict`, as `POST /projects`
  answers `project_conflict`; nothing about the hidden Project is read or
  changed.
- Renaming a Project needs `project.settings.update`. Archiving one, or
  leaving it out of the plan while it has open Tasks (which would delete
  them), needs `project.archive`; without it neither the Project nor any of
  its Tasks changes. A Project the plan read as changeable where the caller
  has since lost `project.tasks.write` (or `project.read`) is left alone too
  when the plan would change it; restated as it is, it is left alone without
  a word. Each of these is listed under `skipped`, and the rest of the plan
  applies.
- On another member's open Task the apply keeps the rules of a Task PATCH:
  giving it another agent, or taking it out of `in_review`, needs
  `tasks.assign` or being its assignee, and so does deleting it by omission.
  Otherwise the apply is `403 company_permission_required`. Owners and
  admins hold `tasks.assign`.

Any difference from the persisted plan within that reach — a Project, Task
or edge that appeared, disappeared or changed since the read — is rejected
wholesale as `409 work_plan_conflict`, and so is a plan naming a Project the
caller could not read when they read it but can change now.

An agent uses the same path with both headers:

```
X-Zenith-Team-Run-Token: brt_<64 lowercase hex characters>
Idempotency-Key: <uuid>
```

That branch does not apply immediately. The server proves the token belongs to
a currently running Run in this Company, loads the authoritative Plan,
recomputes the diff/preview itself — the artifact carries no client-authored
diff for it to trust — freezes the canonical action and its SHA-256, and
returns the **bare Approval** with `202`, for first creation and exact
idempotent replay alike (replay skips recomputation). The same key with
another payload is `409 approval_conflict`. There is no `{approval, created}`
wrapper. The preview covers what the member the Run works for may change.
From a Task Run in a Task chat with a Project it covers that Project alone:
every other Project and any new one are left out of the preview and the
frozen action, and whoever approves it needs `project.approvals.resolve` and
`project.tasks.write` there, not `company.plan.apply` (see
[approvals](approvals.md)).

Bodies here are large; the cap is 16 MiB rather than the usual limit.

The synchronous response is the compact mutation receipt:

```json
{
  "created": 13,
  "updated": 4,
  "deleted": 3,
  "skipped": [
    { "entityType": "project", "key": "website", "operation": "archive", "permission": "project.archive" }
  ]
}
```

A desired task names the agent that does its work with `agentId`. In the same
Plan transaction the server checks that the agent is an active, non-archived
agent of the company and writes `executed_by_agent_id`. A task without an
`agentId` is done by its assignee. A task the plan creates belongs to the
member who applied it; a task the plan updates keeps the member it already
has, because a plan changes who does the work, never whose computer it runs
on. No `task_assigned` Inbox item is raised: nothing was handed to anybody.

The Plan document is `formatVersion: 5` and stores a flat `desired.tasks` list.
The frozen `base` contains `tasks` and `dependencies`; each desired Task names
its prerequisites in `dependsOn`. Every desired task also
requires `milestoneType`. Milestone type is one stable
company-building outcome from catalog version 2 (`positioning`, `brand`,
`product`, `launch`, `growth`, `sales_outbound`, `operations`, or `support`).

The document may carry `failed` or `error` only to restate an existing Task
already in that same server-owned state. A Plan cannot manufacture either
outcome.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `company_permission_required` | Member whose role lacks the permission |
| 401 | `run_token_invalid` | Agent branch token is malformed, expired, cross-company, or its Run is no longer running |
| 409 | `approval_conflict` | Agent branch reused its idempotency key with another payload |
| 409 | `work_plan_conflict` | A `base` revision did not match — re-read the plan and re-apply |
| 413 | `request_too_large` | Plan over 16 MiB |
| 422 | `validation_failed` | Malformed plan, unknown task key, or a dependency cycle |

---

## List tasks

```
GET /api/v1/companies/{companyId}/tasks
```

| Parameter | In | Required | Description |
|---|---|---|---|
| `status` | query | no | Comma-separated set, e.g. `todo,in_progress` |
| `priority` | query | no | Comma-separated set |
| `assignee_id` | query | no | Tasks one member is responsible for |
| `executed_by_agent_id` | query | no | Tasks one agent does the work for |
| `dependencies_resolved` | query | no | `true` returns only tasks whose prerequisites are all `done` |

Only tasks of Projects the caller reads are listed ([Project access](#project-access)). A `project_id` the caller cannot read, or that does not exist, is `404 project_not_found`.

**`executed_by_agent_id=…` is how a client reads one agent's board, and
`assignee_id=…` how it reads one member's.** They are different questions: a
task belongs to a member and is worked on by an agent.

Multi-value filters travel as one comma-separated value rather than a repeated
parameter, because a repeated query parameter is `422` everywhere in this API.
A board that shows two columns sends `status=todo,in_progress` in one request
instead of reading the same list twice.

**Inside a Run the list is the Run's own tasks.** `zen task list` in Agent
mode reaches this route through the Run's channel, with the member's session
plus the Run's `X-Zenith-Team-Run-Token`. A Run reads only the tasks its agent
does for the member it works for — its authorizer, on whose computer it runs
(ADR 0014) — so the server replaces `assignee_id` and `executed_by_agent_id`
with those two and keeps every other filter. A token that does not prove a
running Run in this Company, or that is sent with a session other than that
member's, is `401 run_token_invalid`. See
[Reads from inside a Run](#reads-from-inside-a-run).

```json
{
  "items": [
    {
      "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "key": "ENG-14",
      "number": 14,
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "milestoneType": "product",
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "title": "Add keyset pagination to the run list",
      "description": "Replace the hard-wired limit with a cursor.",
      "status": "in_progress",
      "priority": "high",
      "assignee": {
        "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
        "kind": "user",
        "name": "Alex",
        "avatarUrl": null
      },
      "executedBy": {
        "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
        "kind": "agent",
        "name": "Ada",
        "avatarUrl": null
      },
      "execution": { "assigneeReady": true, "unreadyReason": null },
      "creator": {
        "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
        "kind": "user",
        "name": "Alex",
        "avatarUrl": null
      },
      "blockedByCount": 1,
      "dependenciesResolved": true,
      "createdAt": "2026-08-12T08:14:00.113000Z",
      "updatedAt": "2026-08-15T09:41:07.902114Z",
      "completedAt": null,
      "revision": 7,
      "labels": [],
      "startDate": null,
      "dueDate": null
    }
  ]
}
```

`number` is monotonic per company, and `{taskPrefix}-{number}` is the label
every surface displays for the task. `key` is the task's stable identity: a
create without one gets exactly that label as its key, while a Company Plan
keeps the slug the plan chose (see `POST /company-plan/apply`), so agents and
the CLI quote `key` and the CLI also resolves the displayed label.

`assignee` and `creator` embed a display name, so a task list renders without
resolving actor ids — there is no user-resolution endpoint and there does not
need to be.


> **Added.** `dependenciesResolved` and `blockedByCount`.
> `dependenciesResolved` is the predicate the enqueue path itself evaluates, so
> a client disables *Start* instead of firing a request that can only return
> `409 task_dependency_blocked`. It is deliberately not derivable from
> `blockedByCount`: a task with four prerequisites is startable once all four
> are `done`.

> **Removed.** `latestRun` and `blocksCount`. The run chip is a join the
> client makes from the runs it already caches and keeps live from `run.*`
> lifecycle frames — a third copy of the run-status vocabulary embedded per
> task row could only lag those frames. `blocksCount` restated the dependency
> edges the dependency reads below already serve; the blast-radius question is
> answered there, with the tasks themselves attached.

## Create, read, and update a task

```
POST  /api/v1/companies/{companyId}/tasks
{ "projectId": "0198aa00-0000-7000-8000-00000000c001",
  "title": "Add keyset pagination to the run list",
  "milestoneType": "product",
  "priority": "high",
  "executedBy": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent" } }

GET   /api/v1/companies/{companyId}/tasks/{taskId}
PATCH /api/v1/companies/{companyId}/tasks/{taskId}
{ "expectedRevision": 7, "status": "in_review" }
```

`projectId`, `title` and `milestoneType` are required on create; `key`,
`assignee` and `executedBy` are optional. The assignee defaults to the
caller and must be a member — naming somebody else needs `tasks.assign`;
`executedBy` defaults to the assignee and names the agent that does the work
otherwise. A run starts from the create only when the caller is the assignee
and an agent does the work: without their binding of it the call is
`409 agent_binding_required`. The Project is chosen once and never changes (see
[Project ownership](#project-ownership) below). **The call creates the task
and its conversation in one transaction**, with PostgreSQL generating both
identities. Omit `key` and the server mints `{taskPrefix}-{number}` from the
number it allocates in that transaction, so the key a person quotes and the
label on the board are one task; a client that sends a `key` keeps it,
trimmed of surrounding whitespace (a whitespace-only key is `422`). The
`{taskPrefix}-{number}` shape belongs to the server: a chosen key in that
shape is `422`, and a label already held by an older row makes the mint take
the next number. The company-scoped task key is the integrity fence; a
duplicate chosen key is `409 task_conflict`. When the caller is the assignee
and an agent does the work, the create also enqueues that agent's first run
on the caller's binding in the transaction, with a PostgreSQL-generated run
identity, and commits the task `in_progress` under the same admission rule a
task message follows (below): a task created already `done` or `cancelled`
gets no run and keeps its status. A successful create always returns `201`.

**Input files.** Create also takes optional `attachmentIds`: at most 20
unique ids of the creator's `ready` uploads in this company (uploaded
through `/attachments/initiate` and `/complete`), in display order. They
travel on one member message in the task conversation, "Attached N files
for this Task.", written in the create transaction and owned by the
assignment run, so they list among the task's sources and every run of the
task receives them as read-only input files outside its workspace (the
newest 20 files of the task's conversation). That needs the assignment
run, which this create starts only when the caller is the assignee and an
agent does the work: files on a task the assignee does themself, on one
assigned to another member, or on one created `done` or `cancelled` are
`422 validation_failed`, and without the caller's binding of the agent the
create is `409 agent_binding_required` with nothing written. A file that is not the creator's
finished upload in this company, or is already attached elsewhere, is
`409 task_attachment_unavailable`, and nothing is created. The Workflows
launch dialog sends them; the New task dialog has no upload control yet.
Files a colleague attaches to a message in the task chat ride the queued
item (see [Send a message](conversations.md#send-a-message)) and reach the
assignee's next run, the one that carries the message, and every run after
it.

A PATCH that changes `assignee` or `executedBy` of a task that has run is a
handoff and needs `confirmHandoff: true` (the full rules are on the PATCH
operation in [tasks.yaml](spec/tasks.yaml)). Confirming stops the task's live
runs and starts its work over, so a task that was `in_progress` or
`in_review` goes back to `todo`: its earlier runs can no longer be reviewed
or merged, and the new assignee starts it on their own computer. A handoff to
yourself with an agent starts that run at once and lands `in_progress`.

`zen task create` inside a Run reaches this route through the Run's channel,
which sends the member's session plus the Run's `X-Zenith-Team-Run-Token`. When
that header is present the server resolves it first: a token that is
malformed, belongs to another company, names a Run that is no longer
running (cancelled by a handoff or by a person), or a Run with no
authorizing member is `401 run_token_invalid` and nothing is written. A Run
files work only for the member it works for: an `assignee` other than that
member, or a caller who is not that member, is
`403 task_outside_run_scope`, even for an owner. A Run files only in a
Project it may read: one its member reads; for a task Run, only its task
chat's Project (still so after its task is deleted). A Run mentioned in a
channel or group DM uses the access of the member who mentioned the Agent,
like any Run outside a task, whoever else is in the chat. Any other
`projectId`, one the member cannot see included, is the same `403`, before
anything else is checked. In a Project the Run may read, the member's own
check follows: without `project.tasks.write` the answer is
`403 project_permission_required`. A create without the header is the
member's own and does not need one.

`GET .../tasks/{taskId}` names the task by its id, its `key` in any letter
case, or the `{taskPrefix}-{number}` label the board shows; an exact key wins
over another task's label, and a name that is no task is `404`. Keys are
unique only in their exact letter case: when two keys equal the name in
another case, the older task (lower `number`) wins, so one name always names
one task. Sent with a Run's token it reads only the Run's own tasks — see
[Reads from inside a Run](#reads-from-inside-a-run).

### Reads from inside a Run

A Run reads only its own tasks: the ones its
agent does for the member the Run works for. Tagged in a channel and asked to
"run BSC-8", an agent read a colleague's task and acted out its description
on the wrong computer while the task itself never started; the read is now
refused instead. The rule applies to every task read that carries
`X-Zenith-Team-Run-Token` — `zen task get` and `dependencies` in Agent mode,
`zen task list` from a Run outside a task, and the task's workspace and
results, which no Run command reaches — and to nothing a member reads for
themself.

- The list is the Run's own tasks (above). Only a Run outside a task is
  offered `zen task list`; a task Run's channel refuses it with
  `run_scope_denied` (exit code `3`) before it reaches the server.
- `GET .../tasks/{taskId}` and `GET .../tasks/{taskId}/dependencies` of any
  other task are `403 task_outside_run_scope`, even when the member could
  read it. The body carries nothing of the task — no content, label, member
  or agent name — and its `detail` is always the same sentence:

  ```json
  {
    "detail": "Permission denied. You can only access your own Tasks.",
    "code": "task_outside_run_scope",
    "requestId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c"
  }
  ```

- A task Run's own task always qualifies, and the prerequisites of a task
  the Run can read come with it through `.../dependencies` as read-only
  context — the same summaries its prompt already carries. Of the tasks
  waiting on it (`blocks`), only the Run's own come with it.
- `GET .../tasks/{taskId}/workspace` and `.../results` carry the whole task,
  so they answer a Run's token the same way: the Run's own task reads, any
  other is `403 task_outside_run_scope`. The workspace's `dependencies`
  follow the rule above.
- A create sent with the token files work only for the Run's member, and
  from a task Run only in its task's Project (see create, above).
- One Run reads more. The company's root agent plans the
  company, so its Run in its member's direct chat, with its Company planning
  on, reads every task its member reads: the list keeps the caller's own
  filters, any task the member reads answers `200`, and its dependencies
  keep every dependent. The server decides this from the token: the agent
  is the root agent, its `planning` built-in is not switched off, and the
  Run answers into its member's own direct chat, with no task and no
  channel. The same agent tagged in a channel, in a task Run, or with
  planning switched off, and every other agent, reads only its own tasks.
  Project bounds still apply, and a create is unchanged.
- A token that does not prove a running Run in this Company, a Run with no
  authorizing member, or a token sent with any session other than that
  member's is `401 run_token_invalid` before anything is read.
- The reads are made with the member's session, so they follow the member's
  Project access ([ADR 0017](../adr/0017-project-access-scopes.md)): the
  Run's own task in a Project the member no longer reads is
  `404 task_not_found`, and a create needs the member's
  `project.tasks.write` in its Project (`403 project_permission_required`).
- A Run started by a mention in a channel or a direct message between
  people answers into that chat, where every active member reads the answer.
  It reads a task only in a Project **every** active member of the chat
  reads: its own task in any other Project is `403 task_outside_run_scope`
  with the same sentence, the list leaves such tasks out, and a create there
  is refused the same way. The general channel therefore reaches only
  Company-visible Projects.
- A task Run answers into its task chat, whose readers are exactly its
  Project's readers, so it reads and files work only in that Project: the
  member's own task in another Project is `403 task_outside_run_scope`. A
  task Run has no list command (above); should a list request carry its
  token anyway, the server answers only tasks in that Project, as a second
  guard.
- The root agent's Company Plan export (an MCP tool, not the CLI) still
  carries no Run token, until it moves onto the token and the rule above
  for the root agent bounds it: it reads the whole plan, every task in
  full, because the root agent plans the Company.
  No other agent is offered it, and the root agent is not offered it when
  it was mentioned in a channel or a direct message between people: the
  export reads with the member's session and no Run token, so the chat's
  bound could not narrow it. It follows the member's Project access.

`PATCH` returns the one task-write receipt — the updated task **flat**, with
the run the transition enqueued as a sibling `runId` — so the caller never
reads back:

```json
{
  "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
  "key": "ENG-14",
  "number": 14,
  "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
  "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
  "title": "Add keyset pagination to the run list",
  "description": "Replace the hard-wired limit with a cursor.",
  "status": "in_review",
  "priority": "high",
  "assignee": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
  "executedBy": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent", "name": "Ada", "avatarUrl": null },
  "execution": { "assigneeReady": true, "unreadyReason": null },
  "creator": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
  "blockedByCount": 1,
  "dependenciesResolved": true,
  "createdAt": "2026-08-12T08:14:00.113000Z",
  "updatedAt": "2026-08-15T16:20:44.310277Z",
  "revision": 8,
  "runId": null
}
```

> **Changed.** The handler discarded the id of any run the update started. It
> is now on the response as `runId` — `null` when the transition started
> nothing. Whether a run was created is exactly `runId != null`; there is no
> separate flag, because a second mechanism could only agree with the first or
> lie. The database generates that run identity; requests do not supply one.
>
> It is a **UUID string**, generated by PostgreSQL — see
> [runs.md](runs.md). Preserve the full string in links and cache keys.

**Errors**

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | Body is not JSON, is not an object, or carries an unknown field |
| 401 | `run_token_invalid` | (create, read) `X-Zenith-Team-Run-Token` is present and is malformed, cross-company, its Run is no longer running, the Run has no authorizing member, or the session is not that member's |
| 403 | `company_permission_required` | (create) The task is for another member and the caller lacks `tasks.assign`; (PATCH) a handoff, or a move of an `in_review` task to any other status — marking it done without review among them — by someone who is neither the assignee nor a `tasks.assign` holder. The review decides the work the assignee's computer did |
| 403 | `project_permission_required` | The caller reads the task's Project but lacks `project.tasks.write` (create, update), or `project.tasks.execute` for the Run a create or update would start; that second refusal names the agent, never the Project ([Project access](#project-access)) |
| 403 | `task_outside_run_scope` | (read) Sent with a Run's token, the task is not one of the Run's own, or a task Run reads a task outside its task chat's Project; (create) the task would be another member's, a task Run's in another Project, or in a Project the member cannot read. The `detail` is always "Permission denied. You can only access your own Tasks." |
| 404 | `task_not_found` | Unknown task — by id, key or label — or one in another company |
| 409 | `project_member_required` | The assignee (create) or the new assignee (handoff) cannot read the task's Project; the detail names nobody |
| 409 | `project_archived` | The Project is archived: it admits no new Task, and a PATCH may not leave a Task outside `done`/`cancelled` or hand it to a new Agent |
| 409 | `task_conflict` | `expectedRevision` did not match, or (create) a duplicate chosen `key` |
| 409 | `task_attachment_unavailable` | (create) A file in `attachmentIds` is not the creator's finished upload in this company, or is already attached elsewhere; nothing was created. Upload the file again, then create the task |
| 409 | `task_dependency_blocked` | The transition needs an incomplete prerequisite — complete it, do not poll |
| 409 | `task_handoff_confirmation_required` | A PATCH hands off a Task that has run (a new `assignee` or `executedBy`) without `confirmHandoff: true`. The body carries `runLive` beside the envelope: whether confirming cancels a Run that is live right now — see [errors.md](errors.md#conflicts-that-carry-state) |
| 422 | `validation_failed` | A field rule did not hold: `status` set to a server-owned outcome (`failed`, `error`), a blank `key` or `title`, more than 20 labels, a malformed date, a PATCH that names no field to change, or (create) more than 20 or repeated `attachmentIds`, or files on a create that starts no Agent run (no executing agent, an assignee other than the caller, or `done`/`cancelled`). Any move among the other seven statuses is accepted; there is no transition matrix |

---

## Task labels and calendar dates

Create and PATCH accept `labels`, `startDate`, and `dueDate`. Task projections
in list, detail, workspace, write receipts and events return the same fields.
`creator` and `createdAt` remain immutable attribution for Created by / Created on.

```json
{
  "expectedRevision": 8,
  "labels": ["Design", "Beta"],
  "startDate": "2026-09-14",
  "dueDate": "2026-09-25"
}
```

- `labels` is a Task-owned array of at most 20 strings, not a Company label
  catalogue. Values are trimmed, limited to 48 Unicode characters, reject
  control characters, and deduplicate case-insensitively in first-seen order.
  `[]` clears labels; `null` is invalid.
- Dates are optional `YYYY-MM-DD` calendar values (years 0001–9999), not
  timestamps or automatic scheduling triggers. `null` clears a date; omission
  preserves it. The merged start date cannot be after the merged due date.
- These edits use the same Task revision and membership transaction fence as
  other Task writes. Metadata-only changes never start a Run or change status.

Validation failures return the standard validation envelope; stale Task
revisions return `409 task_conflict`. No partial write is committed on failure.

---

## Start a task's execution

```
POST /api/v1/companies/{companyId}/tasks/{taskId}/runs
{ "expectedRevision": 9 }
```

Enqueues a run of the task's `executedBy` agent on the assignee's own
binding of that agent (ADR 0014). Only the task's assignee may start it,
because the run executes on their computer; anyone else is
`403 task_assignee_required`. When the assignee has not set the agent up on
one of their computers, the call is `409 agent_binding_required` and nothing
is written. The response is the same receipt the `PATCH` returns — the
task's post-transition state **flat**, with the PostgreSQL-generated `runId`
beside it, always set here. There is no `{task, runId}` wrapper: one shape
for every task write, so both land in the same cache slot. A matching
revision creates a fresh run and answers `201`; a stale revision answers
`409 task_conflict`.

The Task's `executedBy` must be an agent — a task the assignee does themself
is `409 task_conflict` — and the Task must be currently `backlog`, `todo`,
`blocked`, or `error`; its latest Run, if any, must be terminal, and every
prerequisite must be `done`. The transaction moves the Task to
`in_progress`, advances its execution generation, and freezes that
generation on the new Run. It also posts the task's title and description
(`{title}\n---\n{description}`) in the task's chat as the assignee's
message, carrying the new `runId`; the run answers it. An assignment run —
a create for yourself or a handoff to yourself — posts the same message.

```json
{
  "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
  "key": "ENG-14",
  "number": 14,
  "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
  "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
  "title": "Add keyset pagination to the run list",
  "description": "Replace the hard-wired limit with a cursor.",
  "status": "in_progress",
  "priority": "high",
  "assignee": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
  "executedBy": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent", "name": "Ada", "avatarUrl": null },
  "execution": { "assigneeReady": true, "unreadyReason": null },
  "creator": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
  "blockedByCount": 1,
  "dependenciesResolved": true,
  "createdAt": "2026-08-12T08:14:00.113000Z",
  "updatedAt": "2026-08-15T16:10:02.118221Z",
  "revision": 10,
  "runId": "00000000-0000-0000-0000-0000000012d6"
}
```

The full run projection is delivered by the run API and run lifecycle events;
the command response stays limited to the mutation receipt. The
`expectedRevision` token follows the same optimistic-concurrency contract as
the other guarded resources.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `task_assignee_required` | The caller is not the task's assignee; only they can start it, on their own computer |
| 403 | `project_permission_required` | The assignee lacks `project.tasks.execute` on the task's Project; the short detail names the agent: "Eli didn't start: you can't run tasks here." |
| 404 | `task_not_found` | Unknown task, or one whose Project the caller cannot read |
| 409 | `agent_binding_required` | The assignee has not set the `executedBy` agent up on one of their computers |
| 409 | `task_conflict` | Revision mismatch, the assignee does the task themself, or the task is in a state that cannot execute |
| 409 | `task_dependency_blocked` | A prerequisite is incomplete |

---

## Report a live Task's business failure

```
POST /api/v1/companies/{companyId}/task-failures
X-Zenith-Team-Run-Token: brt_<64 lowercase hex characters>
Idempotency-Key: <uuid>

{ "reason": "The supplier rejected the requested order." }
```

This is the supported seam for `mark_task_failed`. It uses the ordinary bearer
principal plus the live Run token; there is no loopback callback. The token is
the authority for the Run and Task identity, so the body cannot redirect the
failure to a caller-selected `runId` or `taskId`. `reason` is 1..2000
characters.

The idempotency key is scoped to this operation and Run. An exact replay
returns the same `200` response; reusing it with another reason returns
`409 task_conflict`.

```json
{
  "status": "failed",
  "task": {
    "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
    "key": "ENG-14",
    "number": 14,
    "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
    "milestoneType": "product",
    "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
    "title": "Place the supplier order",
    "description": "Order the approved launch inventory.",
    "status": "failed",
    "priority": "high",
    "assignee": {
      "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
      "kind": "user",
      "name": "Alex",
      "avatarUrl": null
    },
    "executedBy": {
      "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
      "kind": "agent",
      "name": "Ada",
      "avatarUrl": null
    },
    "execution": { "assigneeReady": true, "unreadyReason": null },
    "creator": {
      "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
      "kind": "user",
      "name": "Alex",
      "avatarUrl": null
    },
    "blockedByCount": 0,
    "dependenciesResolved": true,
    "createdAt": "2026-08-12T08:14:00.113000Z",
    "updatedAt": "2026-08-15T16:10:02.118221Z",
    "completedAt": "2026-08-15T16:10:02.118221Z",
    "revision": 11,
    "labels": [],
    "startDate": null,
    "dueDate": null
  }
}
```

This marks an Agent-reported business outcome. A technical Run failure uses
Task status `error`; this endpoint never selects it.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `run_token_invalid` | Token is malformed, expired, cross-company, or its Run is no longer running |
| 404 | `company_not_found` | Unknown Company or caller is not a member |
| 404 | `task_not_found` | The token's Task is no longer available |
| 409 | `task_conflict` | Mismatched replay, terminal race, or an invalid Task transition |
| 422 | `validation_failed` | Missing/blank reason, reason over 2000 characters, or invalid idempotency key |

---

## Read a task's dependencies

```
GET /api/v1/companies/{companyId}/tasks/{taskId}/dependencies
```

Both directions, unpaginated — a single task's edge set is small. Each edge
embeds the **other** task: `task` is the prerequisite inside `blockedBy` and
the dependent inside `blocks`, so one wire type serves both directions and a
blocker chip renders from the edge alone. This turns three dependent waves into
one request, with each prerequisite’s identity, status, and assignee included.

```json
{
  "blockedBy": [
    {
      "task": {
        "id": "0197f4c7-3311-7d55-9a26-8b4f0c1e7d90",
        "key": "ENG-9",
        "number": 9,
        "title": "Add the cursor codec",
        "status": "done",
        "assignee": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Alex", "avatarUrl": null },
        "executedBy": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent", "name": "Ada", "avatarUrl": null },
        "revision": 11
      },
      "createdAt": "2026-08-12T08:20:41.550210Z",
      "revision": 1
    }
  ],
  "blocks": []
}
```

> **Changed.** The embedding was behind a `resolve=true` flag, and the flag's
> `false` branch returned the same fields with the task half missing — a shape
> whose absent fields mean "not requested" rather than "unchanged", which is
> the one thing [the merge contract](conventions.md#absent--null--empty)
> forbids. The query that reads the edges already joins the tasks, so the flag
> bought nothing and is gone. Each edge includes its related Task directly.

> **Added.** `assignee` and `executedBy` on the embedded task, and an
> unambiguous `revision` split. "Blocked by ENG-9" is not actionable;
> "blocked by ENG-9, which Ada does for Alex" is. The edge's `revision`
> sits on the edge — that is the one the plan artifact's
> `base.dependencies` needs — and the task's sits inside `task`,
> where a single flattened `revision` could not say which it meant.

`blocks: []` is an authoritative "nothing is waiting on this", not missing
data.

The path takes the task's id, key or label, as `GET .../tasks/{taskId}`
does. Sent with a Run's `X-Zenith-Team-Run-Token`, the task itself must be one of
the Run's own, else `403 task_outside_run_scope`. `blockedBy` is then whole,
but `blocks` holds only the Run's own tasks, so there `[]` means none of
them is waiting on it; see [Reads from inside a Run](#reads-from-inside-a-run).

## Read the company's dependency graph

```
GET /api/v1/companies/{companyId}/task-dependencies
```

Every edge in the company, for rendering a plan view.

It takes no query parameters; any parameter is `422 validation_failed`.

```json
{
  "items": [
    { "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "dependentTaskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "prerequisiteTaskId": "0197f4c7-3311-7d55-9a26-8b4f0c1e7d90",
      "createdAt": "2026-08-12T08:20:41.550210Z", "revision": 1 }
  ],
  "tasks": [
    { "id": "0197f4c7-3311-7d55-9a26-8b4f0c1e7d90", "key": "ENG-9", "number": 9,
      "title": "Add the cursor codec", "status": "done" }
  ]
}
```

> **Changed.** This endpoint read its query parameters directly and therefore
> silently accepted unknown ones, unlike every sibling list. It now rejects
> them with `422` like the rest of the API and returns the complete company
> edge set.

Edges here stay **normalized** — ids in `items`, one stub per endpoint task in
`tasks` — the opposite of the single-task view above, and for the opposite
reason: a plan graph is dense, and embedding a summary on both ends of every
edge would repeat the same task rows across the response.

---

## Read a task's workspace

```
GET /api/v1/companies/{companyId}/tasks/{taskId}/workspace
```

**Everything a task detail screen renders, in one request.** This is the
largest single round-trip reduction in the contract. Every part is read from
one read-only `REPEATABLE READ` snapshot.

“Workspace” here is the Task detail composite, not the company's local working
directory. It returns no cwd, filesystem listing or file contents. Local
placement, exact-run preview and retention are documented in
[workspaces.md](workspaces.md).

### Use Cases

- **A member opening a task** needs the task, its conversation history, the
  agent output inside that history, what is running now, and what is blocking
  it. Before the workspace composite, that meant five independent reads plus
  one transcript read per distinct Run in the returned messages.
- **A client reconnecting** to a task it already had open needs the same
  snapshot to re-sync in one request rather than replaying the mount sequence.

Before: about thirty requests, roughly twenty-six of which were sequential or
N+1. After: one.

| Parameter | In | Required | Description |
|---|---|---|---|
| `message_limit` | query | no | Newest N messages, 1..100, default 50 |

```json
{
  "task": {
    "id": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
    "key": "ENG-14",
    "number": 14,
    "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
    "milestoneType": "product",
    "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
    "title": "Add keyset pagination to the run list",
    "description": "Replace the hard-wired limit with a cursor.",
    "status": "in_progress",
    "priority": "high",
    "assignee": {
      "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
      "kind": "user",
      "name": "Alex",
      "avatarUrl": null
    },
    "executedBy": {
      "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
      "kind": "agent",
      "name": "Ada",
      "avatarUrl": null
    },
    "execution": { "assigneeReady": true, "unreadyReason": null },
    "creator": {
      "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
      "kind": "user",
      "name": "Alex",
      "avatarUrl": null
    },
    "blockedByCount": 1,
    "dependenciesResolved": true,
    "createdAt": "2026-08-12T08:14:00.113000Z",
    "updatedAt": "2026-08-15T16:10:02.118221Z",
    "completedAt": null,
    "revision": 10,
    "labels": [],
    "startDate": null,
    "dueDate": null
  },
  "conversation": {
    "id": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
    "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
    "kind": "task",
    "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
    "title": "Add keyset pagination to the run list",
    "createdAt": "2026-08-12T08:14:00.113000Z",
    "updatedAt": "2026-08-15T16:10:02.118221Z",
    "revision": 9
  },
  "messages": [
    {
      "id": "0197f5a2-88b1-7f04-9e13-5c7d2a4b8e60",
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "seq": 41,
      "type": "agent_text",
      "author": {
        "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
        "kind": "agent",
        "name": "Ada",
        "avatarUrl": null
      },
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "content": "Cursor codec landed; paging the run list next.",
      "createdAt": "2026-08-15T16:02:57.903118Z",
      "attachments": []
    }
  ],
  "earliestAvailableSeq": 1,
  "throughSeq": 41,
  "runTimelines": [
    {
      "runId": "00000000-0000-0000-0000-0000000012d5",
      "throughSeq": 41,
      "hasMore": false,
      "items": [
        {
          "id": "0197f5b0-7781-7a03-9c14-6d2e8f4b1a29",
          "runId": "00000000-0000-0000-0000-0000000012d5",
          "seq": 41,
          "itemKey": "assistant-12",
          "role": "agent",
          "parts": [
            {
              "type": "text",
              "text": "Done."
            }
          ],
          "itemDigest": "67aa798d323d3147d69a964a2b6317d79d78a7147a4eaf4f551bc9a4ecf5251a",
          "createdAt": "2026-08-15T16:02:55.100000Z"
        }
      ]
    }
  ],
  "activeRuns": [
    {
      "id": 4822,
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "agentId": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
      "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "runtimeId": "0197f3d0-4411-7a90-8c22-19be7f3d55a1",
      "status": "running",
      "attempt": 1,
      "maxAttempts": 2,
      "parentRunId": null,
      "triggerMessageId": null,
      "transcriptThroughSeq": 0,
      "error": null,
      "failureReason": null,
      "waitReason": null,
      "createdAt": "2026-08-15T16:10:02.118221Z",
      "updatedAt": "2026-08-15T16:10:04.552013Z",
      "fireAt": null
    }
  ],
  "runQueue": {
    "currentRun": null,
    "items": [],
    "limit": 20
  },
  "dependencies": {
    "blockedBy": [
      {
        "task": {
          "id": "0197f4c7-3311-7d55-9a26-8b4f0c1e7d90",
          "key": "ENG-9",
          "number": 9,
          "title": "Add the cursor codec",
          "status": "done",
          "assignee": {
            "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41",
            "kind": "user",
            "name": "Alex",
            "avatarUrl": null
          },
          "executedBy": {
            "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
            "kind": "agent",
            "name": "Ada",
            "avatarUrl": null
          },
          "revision": 11
        },
        "createdAt": "2026-08-12T08:20:41.550210Z",
        "revision": 1
      }
    ],
    "blocks": []
  }
}
```

Three design points worth stating:

- **`runTimelines` is bounded.** Only the runs referenced by the returned
  message page are included, and each carries at most its newest **20** durable
  transcript rows. `hasMore: true` means older rows exist; use the
  [Conversation transcript](conversations.md#read-the-provider-transcript) as
  the drill-down and group its rows by `runId`. Unbounded embedding would trade
  an N+1 for one enormous response. Each row carries its complete structured
  parts and producing `runId`; the cap, not a thinner item shape, is what makes
  this a preview rather than a replacement for the transcript endpoint. A
  timeline row's `seq` is the **Conversation's**, the same counter `messages`
  above is numbered by, so a client can interleave the two without correlating
  anything.
- **`activeRuns` is scoped to this task**, replacing "fetch 100 company runs
  and filter in the browser".
- **There is no read watermark here.** A task conversation has many readers,
  and per-user read state lives on the conversation row, which one task
  conversation cannot hold for all of them. What tells a member a task needs
  them is the [Inbox](inbox.md), which is per-recipient by construction. Read
  state exists only for a direct chat, which has exactly one reader.
- **Conversation privacy remains authoritative.** An ordinary task
  conversation is visible to the readers of its task's Project (ADR 0017). If an older Task points
  at a direct conversation, the workspace is visible only to that direct
  conversation's creator; another member in the same Company receives the
  same non-disclosing `404 task_not_found` as an unknown Task.

Every object here is the same DTO its own resource returns — `dependencies` is
byte-for-byte what [the dependencies
endpoint](#read-a-tasks-dependencies) returns — so a client stores them in the
same cache slots the individual reads would have filled, and the realtime
events that follow update those slots directly.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `run_token_invalid` | `X-Zenith-Team-Run-Token` is present and does not prove a running Run in this company |
| 403 | `task_outside_run_scope` | Sent with a Run's token, the task is not one of the Run's own ([Reads from inside a Run](#reads-from-inside-a-run)) |
| 404 | `company_not_found` | Unknown company or not a member |
| 404 | `task_not_found` | Unknown task, or one in another company |
| 422 | `validation_failed` | Unknown parameter, or `message_limit` outside 1..100 |

---

## Preview and send Task reports

```
GET  /api/v1/companies/{companyId}/conversations/{conversationId}/task-reports
POST /api/v1/companies/{companyId}/conversations/{conversationId}/task-reports/send
```

Task reports let a user review the oldest pending Task outcomes destined for
the Agent in a direct Conversation, then confirm them as one durable message
and Run. Preview returns at most 100 events and does not reserve or mutate the
queue. `pendingCount` is the complete count, while `truncated` says whether
the preview omitted later events.

The Conversation must be the caller's own direct Conversation with the Agent;
another member's answers `404`. The Run a send starts works for the caller:
it runs on the caller's own setup of the Agent and answers in that
Conversation.

Only outcomes of Tasks in Projects the caller reads are counted, listed and
sent ([ADR 0017](../adr/0017-project-access-scopes.md)). The others stay
queued for the same Agent until someone who reads their Project previews or
sends them. Proactive mode sends for the member who turned it on, under the
same rule. A send with only such outcomes left answers
`task_report_no_pending`.

The send body contains only a caller-generated replay fence:

```json
{ "requestKey": "0197f67a-93f5-71c2-bd80-8ff46fbd20ce" }
```

PostgreSQL generates the batch, Message, and Run identities. A new batch
returns `201`; replaying the same `requestKey` for the same caller,
Conversation, and Company returns the original receipt with `200`:

```json
{
  "batchId": "0197f67b-12a8-7a33-8f04-339e502ab923",
  "messageId": "0197f67b-2bb4-7331-8ea2-3f28b04f61aa",
  "runId": "00000000-0000-0000-0000-0000000012d7",
  "eventCount": 3
}
```

The response deliberately has no `created` flag: the status code carries that
fact. `runId` is an `int64`, like every Zenith Team Run identity.

**Errors**

| Status | Code | When |
|---|---|---|
| 404 | `task_not_found` | The direct Conversation is unknown, inaccessible or another member's |
| 409 | `task_report_agent_busy` | Its recipient Agent already has an active Run |
| 409 | `task_report_no_pending` | No pending Task outcome the caller reads remains to send |
| 409 | `task_conflict` | The replay key names incompatible state |
| 422 | `validation_failed` | The body or `requestKey` is invalid |

---

## Realtime Events

| Event | Scope | Carries | Kind |
|---|---|---|---|
| `task.created` | Project readers | The task object | Cache |
| `task.updated` | Project readers | The complete task object | Cache |
| `task.deleted` | Project readers | `{taskId, revision}`: the row is gone, so there is no object to send | Cache (removal) |
| `task.report_ready` | Project readers | `{taskReportEventId, recipientAgentId, taskId, taskRevision}` | Refetch pending reports |
| `task.report_changed` | Project readers | `{recipientAgentId, taskId, taskRevision}` | Refetch pending reports |

Every task frame goes only to the readers of the task's Project, or to the
whole Company when the Project is Company-visible (see
[Work inside a Project](realtime.md#work-inside-a-project)). A sent report
batch that spans several Projects sends one `task.report_changed` per
Project.

Task create/update events carry the full entity, so a plan view and a
task list stay live with no reads. `blockedByCount` and
`dependenciesResolved` ride on the task entity, so a `task.updated` frame
refreshes the blocker chip. A prerequisite moving onto or off `done` —
through a task update, a merge, or a plan apply — flips
`dependenciesResolved` on every task that depends on it, so each of those
dependents gets its own `task.updated` frame at an advanced `revision`; the
same happens when a dependency edge is added or removed. `execution` is read
live off the assignee's setup of the executing agent and the computer it
names (ADR 0014). When the assignee sets the agent up, moves or removes it,
or that computer comes online, goes offline or signs out, each of their open
tasks the agent executes gets a `task.updated` frame with the **same**
`revision`, because no task row moved. The frame names no computer. Apply
it when its `revision` is at least the one you hold. The run chip moves
with the `run.*` lifecycle frames themselves — a task's *execution* is a
run, see [runs.md](runs.md#realtime-events) — and its discussion is a
conversation, see [conversations.md](conversations.md#realtime-events).

## Task lifecycle

This is the principal workflow; the Run ownership and Task-message rules below
define which automated transitions are allowed.

```
backlog ──► todo ──► in_progress ──► in_review ──► done
                          │              │
                          ├──► failed     │
                          ├──► error      │
                          ▼              │
                       blocked ◄─────────┘
                          │
                          ▼
                      cancelled
```

### Runs and execution generations

Every fresh (parentless) Task Run — whether created by assignment, explicit
execution start, or a Task Conversation message — atomically advances the
Task's execution generation and stores that generation on the Run. An
assignment Run (a create with an Agent assignee, or a `PATCH` that changes
the Agent assignee) is admitted and moves the Task to `in_progress` exactly
as a new Task message would, under the admission table below, keyed on the
status the write lands on — a `PATCH` that also sets `done` or `cancelled`
changes the assignee without a Run; a `PATCH` that re-selects the current
assignee creates none. An explicit execution start uses the narrower rule
above. Automatic
retry children do not advance it: they copy their parent's generation. A retry
is created only while the Task remains `in_progress`, remains assigned to the
parent Run's Agent, still owns that generation, and every prerequisite is
`done`. Creating the retry leaves the Task `in_progress`.

A terminal Run can update its Task only under the same `in_progress`, Agent,
and generation fences. A successful current Run moves the Task to `in_review`.
A technical failure that creates no eligible retry — including a nonretryable
failure or exhausted attempt budget — moves it to `error`; cancellation moves
it to `cancelled`. A stale Run from an older generation or Agent assignment has
no Task effect.

`failed` is instead an Agent-reported business outcome from the exact live Run.
Both `failed` and `error` are server-owned statuses and ordinary create/update
requests cannot select them. `done` and `cancelled` are closed to new messages.
`failed` records the Agent's business outcome, but a later user message is an
explicit resume action: its fresh Run moves the Task back to `in_progress` and
clears `completedAt`.

`dependenciesResolved` exposes the same all-prerequisites-are-`done` predicate
used by explicit execution, Task messages, and automatic retries. An explicit
execution start additionally requires the previous Run to be terminal.

### User messages in a Task Conversation

For a new request key, Task policy is checked before the message may create a
Run or enter the Conversation Run queue:

| Current Task status | New Task message |
|---|---|
| `backlog`, `todo`, `in_review`, `blocked`, `failed`, `error` | Admitted only when every prerequisite is `done`; moves to `in_progress` when its fresh Run materializes. Resuming `failed` also supersedes its pending upward failure report |
| `in_progress` | Admitted only when every prerequisite is `done`; stays `in_progress` |
| `done`, `cancelled` | Rejected |

If another Run is active, an admitted prompt waits in the durable Conversation
Run queue. The Task transition and generation advance happen only when that
prompt materializes, and promotion rechecks current status, Agent assignment,
and dependencies; an ineligible head is removed rather than reopening the
Task.

An exact request-key replay is resolved before current Task admission. It
returns the existing Message/Run receipt or the current queued/removed outcome
for that original send even if the Task later became terminal or blocked. It
does not change Task state or generation. Reusing the key for a different send
is a conflict, not a new message.

## Project ownership

Task creation requires `projectId` naming an unarchived Project in the Company.
The value is immutable: PATCH rejects it, including null. List filters accept
`project_id=<UUID>`. Every Task projection, including mutation receipts, workspace
responses and realtime payloads, carries `projectId`. Archived Projects cannot
admit new Tasks or reopened execution (`409 project_archived`). Company Plan v5
authoring and daemon repository routing are separate implementation steps.

## Project access

A task is visible exactly where its Project is
([ADR 0017](../adr/0017-project-access-scopes.md)): to people holding
`project.read` on it. Everyone else gets the answer a missing task gets —
`404 task_not_found` on a read, the dependencies, the workspace or a write;
an empty list; `404 project_not_found` for a list filtered by that Project
and for a create naming it, as for a Project that does not exist; no hit in
search; no edge in the dependency graph. A `projects.manage` holder who
manages the Project without reading it sees none of its tasks, and creates
none there (`404 project_not_found`).

Every task read carries `access.permissions`: the caller's permissions on the
task's Project, merged exactly like the Project's own (grants, plus the
company-visible default set, plus the management names for `projects.manage`
holders). Clients gate on it and never derive it from a role. `task.created`
and `task.updated` frames reach other people and leave `access` out; a client
keeps what it read and refetches on the frame.

Inside a Project the Project permission is the only check, except that a
handoff still needs `tasks.assign` or being the current assignee (ADR 0014):

| Action | Needs |
|---|---|
| Read, list, search, dependencies, workspace | `project.read` |
| Create, update (any field) | `project.tasks.write` |
| Start a Run (start, a create or update that starts one, or a message in the task's chat) | the assignee's `project.tasks.execute` |
| Assign or hand off to someone else | `tasks.assign` or being the current assignee, and the new assignee reads the Project |

A missing permission on a Project the caller reads is
`403 project_permission_required`. The execute refusal is a few plain words
that name the agent, "Eli didn't start: you can't run tasks here." (or "the
assignee can't run tasks here." for anyone else), so nobody goes to fix a
provider sign-in; it never names the Project and tells nobody to ask someone. An assignee who cannot read the Project
is `409 project_member_required`, naming nobody.

A message in the task's chat is refused the same way when the assignee may
not run tasks there, whoever sends it: every Run the chat starts is the
assignee's, and a colleague's message only waits for such a Run. The detail
tells a colleague that the person the task is assigned to cannot run it,
without naming them.

A task named by its key or label (`GET /tasks/{key}`, `zen task get BSC-8`)
is looked up only among the tasks the caller reads, so a hidden task never
takes the name of a visible one.

The write statements check the permission again themselves, so a grant
removed while a write is in flight refuses the write instead of letting it
through.

An assignee who later loses access keeps the task until someone who can read
the Project and holds `tasks.assign` hands it over. Every write receipt lists
such tasks in `assigneeOutsideProjectTaskIds` (usually `[]`), and every task
read and `task.*` frame carries `assigneeOutsideProject: true` for such a task
in a restricted Project, so a client shows the badge from the task itself and
never works it out from the Project's grants. Both follow the one rule: the
receipt lists exactly the written tasks whose own `assigneeOutsideProject` is
true. A grant change sends no `task.*` frame; on `project.access_changed` a
client reads the Project's tasks again.

A start needs the assignee's `project.tasks.execute` in the task's Project.
When they do not hold it, every read says so before anyone presses Start:
`execution.assigneeReady` is `false` and `execution.unreadyReason` is
`assignee_cannot_run`. It comes after `assignee_inactive` and
`agent_archived` and before `not_bound`, since setting the agent up would
not let the run start. It is read live like the other reasons, and a task
is read only by its Project's readers, so it tells nobody about a Project
they cannot see.
