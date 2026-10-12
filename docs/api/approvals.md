---
title: Approvals and questions
summary: Human-in-the-loop — permission approvals, mid-run questions, and how a run resumes
---

Two things an agent can ask a human for mid-work, and they are different:

- **An approval** — the agent wants to do something consequential and needs a
  decision before it proceeds. The human answers *approve* or *reject*.
- **A question** — the agent needs information it cannot derive. The human
  answers with content.

Both are company-scoped and gated on **member**; a non-member gets `404`, never
`403`. Questions and Company Plan approvals are raised by agent tools. A
Commerce manifest submission instead creates its exact publish Approval in the
same transaction as the immutable manifest. Humans resolve either kind over the
client plane.

Both resources are served. Raising one requires the ordinary bearer principal
plus `X-Boardly-Run-Token`; the token is supplemental proof of a currently
running Run and never becomes a second user principal.

## The mechanic that matters: durable human input does not park a run

Normally, **when work needs an approval or a question, its run ends.** It does
not park or poll. The outcome reaches the agent later as a **new run** with the
result injected into its context.

A loopback sandbox Commerce activation is the narrow exception. Manifest
submission creates the Approval before the daemon can register the local public
signing key, while the matching private key exists only inside the supervised
`zen commerce dev` lease. The authoring Task Run therefore persists the
returned proof, starts that wrapper, and stays active through owner publication
and the operator payment test. It must not hand off an Approval while the local
signing key is missing. Non-loopback manifests use the ordinary end-and-resume
flow below.

```
agent calls a gated tool with its live Run token and UUID Idempotency-Key
  └─► server persists a pending approval/question (or manifest + Approval)
        └─► ordinary/non-loopback flow ends its run normally  (run → completed)
              └─► [ hours pass; the human is asleep ]
                    └─► human approves
                          └─► server executes the frozen action
                                └─► server enqueues a NEW run carrying the outcome
```

The Approval, decision, continuation receipt, manifest, and public signing-key
projection remain durable PostgreSQL state. The loopback private key and signer
lease are deliberately ephemeral and reconstructible; if their Task Run exits,
a new active Run of the same Task must restart `zen commerce dev` before
directing the owner to an unpublished Approval or testing the Site.

Three consequences the contract has to carry:

1. **The receipt is durable and replay-safe.** Direct Approval and Question
   creation returns the same row on an exact replay. Commerce manifest submit
   returns the same manifest plus its `approvalId`. Reusing an operation key
   with another payload is `409`.
2. **The action is frozen at request time.** What executes on approval is
   exactly what the human was shown — not what the agent would ask for now.
3. **Expiry wakes the agent too.** A human who never answers still unblocks the
   workflow when the request expires.

Proactive Mode adds one deliberately narrow branch. When a Company Plan apply
is raised from the exact active direct Conversation selected by an active owner,
and that Conversation still targets the active root Agent, the request
transaction executes the frozen action and stores the Approval as `approved`
immediately. It creates no Inbox item and no continuation Run
(`continuationRunId` stays `null`). Exact idempotent replay returns that same
receipt without preparing or executing the action again. Any authority drift
falls closed to the ordinary pending-human path above.

---

## Approvals

### List approvals

```
GET /api/v1/companies/{companyId}/approvals
```

Newest first by `(createdAt, id)`. The cursor names the oldest row already
held; the next page seeks strictly before it with
`(created_at, id) < (cursor_created_at, cursor_id)`.

| Parameter | In | Required | Description |
|---|---|---|---|
| `limit` | query | no | 1..100, default 50 |
| `cursor` | query | no | Opaque cursor |
| `status` | query | no | **Comma-separated** set of statuses — `status=pending,expired`. One value; repeating the parameter is `422`, like every repeated parameter in the API. Defaults to `pending` |
| `agent_id` | query | no | Requests raised by one agent |
| `task_id` | query | no | Requests raised while working one task |
| `view` | query | no | `decide`: only the Approvals the caller may decide. The only value |

Without `view` the list holds every Approval the caller reads: their own
direct ones, and any raised in a Task chat they read. `view=decide` keeps the
ones they may decide, by the rule the decision routes apply (ADR 0017): a
Company Plan Approval raised in a Task chat with a Project needs
`project.approvals.resolve` on that Project, and the Company role is not
consulted; every other Approval needs the Company `approvals.resolve`. The
Company Approvals page and its pending counts read this view, so a member who
decides in one Project sees and counts that Project's Approvals there, and a
reader who may not decide them does not. The client shows the page's
approvals tab to holders of `approvals.resolve` and to anyone whose
`view=decide` list is not empty. Every place the app tells someone a
request waits on them ("Needs your input", "Needs your response",
Respond, the top bar menus) follows the same rule: it reads this view,
or, inside a Task chat, the Task's `project.approvals.resolve`. Whether a
Task waits on a request at all reads the list without `view`.

```json
{
  "items": [
    {
      "id": "0197f6a0-1122-7c33-8d44-5e6f7a8b9c01",
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "kind": "company_plan_apply",
      "subjectId": null,
      "status": "pending",
      "title": "Apply Company Plan",
      "summary": "1 authoritative Company Plan changes",
      "previewMarkdown": "- Create task \"ship_mvp\"",
      "presentation": {
        "kind": "company_plan",
        "companySlug": "northstar",
        "draftDigest": "5f1c0e3a9b7d4c2e8f6a1b0d3c5e7f9a2b4d6e8f0a1c3e5b7d9f1a3c5e7b9d1f",
        "totalChanges": 1,
        "changes": [
          { "entityType": "task", "operation": "create", "key": "ship_mvp" }
        ],
        "plan": {
          "formatVersion": 5,
          "company": "northstar",
          "projects": [
            { "key": "core", "title": "Core product", "description": "", "archived": false }
          ],
          "tasks": [
            {
              "key": "ship_mvp",
              "projectKey": "core",
              "agentId": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3",
              "milestoneType": "product",
              "title": "Ship the MVP",
              "description": "",
              "status": "todo",
              "priority": "high",
              "dependsOn": []
            }
          ]
        }
      },
      "requestedByAgent": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent", "name": "Ada", "avatarUrl": null },
      "originRunId": "00000000-0000-0000-0000-0000000012d5",
      "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "expiresAt": "2026-08-16T10:41:02.481113Z",
      "decisionNote": null,
      "decidedBy": null,
      "execution": null,
      "continuationRunId": null,
      "createdAt": "2026-08-15T10:41:02.481113Z",
      "updatedAt": "2026-08-15T10:41:02.481113Z",
      "revision": 1
    }
  ],
  "nextCursor": null
}
```

| Field | Type | Notes |
|---|---|---|
| `kind` | string | Stable operation key: `company_plan_apply` or `commerce_manifest_publish` |
| `subjectId` | UUID or null | Exact domain resource covered by the Approval. Commerce uses the immutable manifest ID; Company Plan uses `null` |
| `previewMarkdown` | string | The authoritative server-computed diff. Rendered as Markdown, never as HTML |
| `presentation` | object or null | Structured Company Plan review projection (`kind: company_plan`, `changes`, `plan`); `null` for other Approval kinds |
| `originRunId` | UUID string | The Run that raised it, identified by UUID. That run is already terminal by the time anyone sees this |
| `expiresAt` | string | Every approval has a TTL. There is no unbounded pending state |

A terminal `status` **is** the decision — `approved` and `rejected` say what
the human decided, `cancelled` and `expired` say no one did. There is no
separate `decision` field, and no `ruleKey` beside `kind`: both would restate
a field already on the row. The resolver policy is server-side authorization
state, applied on every resolution and reported through
`403 approval_resolver_forbidden`; it is not a wire field and is never agent
input.

**Who sees and decides a task-origin Approval** ([ADR 0017](../adr/0017-project-access-scopes.md)).
An Approval raised by a Task Run lives in that Task's chat and follows the
chat's Project, also after the Task is deleted:

- It is listed and readable only by the Project's readers; anyone else gets
  `404 approval_not_found`. A direct-origin Approval stays its origin
  person's.
- Deciding it (approve, reject, cancel) needs `project.approvals.resolve` on
  that Project, and the Company role is not consulted: a member holding it
  may reject or cancel, an owner or admin who only reads the Project gets
  `403 project_permission_required`.
- Approving also runs the Company Plan apply as the decider, and that apply
  reaches the Task chat's Project alone: the preview and the frozen action
  leave out every other Project and any new one. So approving needs
  `project.tasks.write` on that Project too (`403
  project_permission_required`), and no Company permission: a member with
  both approves. Archiving the Project in the plan also needs
  `project.archive`, and renaming it `project.settings.update`; without
  them the apply skips that change, lists it under `skipped` in the
  execution result, and applies the rest.
- It is addressed to the Project's readers who may approve it: they hold
  `project.approvals.resolve` and `project.tasks.write`.
  When there are none, the raise answers `409 no_eligible_resolver`,
  nothing is stored, and the raising Run fails with `failureReason`
  `no_eligible_resolver`: nobody could ever approve it.
- A direct-origin Approval, or one from a Task chat with no Project (its Task
  was deleted before Project access existed), keeps the Company
  `approvals.resolve` rule. A Commerce publication Approval keeps its own
  owner route; it is addressed to the Company `approvals.resolve` holders who
  read the Task chat's Project, since nobody else can open it.
- Approvals pending when Project access was introduced were addressed again
  by its migration: each gained an item for the Project's readers holding
  `project.approvals.resolve` (then its owners), other people's copies were
  archived, and one nobody could decide went to every active owner, who sees
  it after joining the Project. Pending Questions were treated the same way.

### Read one approval

```
GET /api/v1/companies/{companyId}/approvals/{approvalId}
```

Returns the same complete object and, once approved, an `execution` object
reporting what actually happened.

```json
{
  "id": "0197f6a0-1122-7c33-8d44-5e6f7a8b9c01",
  "status": "approved",
  "decidedBy": { "id": "0197f58e-9a04-7b12-8c77-2d5e6f108b41", "kind": "user", "name": "Duong", "avatarUrl": null },
  "decisionNote": "Fine — those are all build output.",
  "execution": {
    "status": "executed",
    "skippedReason": null,
    "result": { "created": 0, "updated": 0, "deleted": 0 }
  },
  "continuationRunId": "0197f6a1-3344-7d55-9e66-7f8091a2b3c4",
  "revision": 3
}
```

`execution.status` is `executed` or `skipped`.
`continuationRunId` is the new run that carried the outcome back to the agent —
a Run UUID string, stamped onto the approval by the very transaction that creates
that run. It is `null` in the response to the decision itself, and stays `null` for
as long as the continuation is pending or deferred to a later attempt; it stays
`null` permanently when the continuation is abandoned, because then no run was
ever created. A non-null value always names a run readable through the runs
API. It arrives on a re-sent `approval.resolved` at a higher `revision`, or on
a re-read of this approval. (`run.queued` fires for the new run, but that
event carries no reference back to the approval, so it cannot be used to
correlate; the approval is the source of truth.)

### Approve an approval

The generic decision routes below are for `company_plan_apply`. A
`commerce_manifest_publish` Approval is shown as **Review & publish payment
setup** and is resolved only by
`POST /api/v1/companies/{companyId}/commerce/manifests/{manifestId}/approve-publish`.
Successful catalog activation closes that Approval and automatically opens the
continuation receipt for the originating Task; the user never needs to type
“done”.

```
POST /api/v1/companies/{companyId}/approvals/{approvalId}/approve
{ "expectedRevision": 1, "decisionNote": "Fine — those are all build output." }
```

Resolves the request and, for an actionable `kind`, **executes the frozen
action synchronously** before responding — so the response reports the real
outcome rather than an intent.

Three guarantees the contract makes about that execution:

- **One database commit.** Resolution CAS, the Company Plan mutation, Inbox
  closure, and continuation intent share one transaction. A lost race is
  `409 approval_conflict`; an infrastructure error rolls the request back to
  pending. This is not a promise of exactly-once external side effects.
- **Exactly what was shown.** The server persists the canonical action and its
  SHA-256 digest when the request is raised; approval executes that frozen
  action rather than accepting new agent-supplied arguments.
- **Not if the world moved.** If the action's target changed since the request
  was raised, the effect is **skipped**, not applied, and `execution.status` is
  `skipped` with a reason. A human deciding at 9am about a state from midnight
  must not silently overwrite the intervening eight hours. A Company Plan the
  current state refuses outright (see Errors below) is not skipped: the
  approve fails and the Approval stays pending.

### Reject an approval

```
POST /api/v1/companies/{companyId}/approvals/{approvalId}/reject
{ "expectedRevision": 1, "decisionNote": "Not on production." }
```

No action executes. The agent is woken with the rejection and the note, so it
can adjust rather than retry.

### Cancel an approval

```
POST /api/v1/companies/{companyId}/approvals/{approvalId}/cancel
{ "expectedRevision": 1 }
```

Withdraws the request without a decision. Needs the same permission as a
decision (below).

**Errors** (all four operations)

| Status | Code | When |
|---|---|---|
| 403 | `approval_resolver_forbidden` | The caller does not satisfy the request's resolver policy |
| 403 | `project_permission_required` | A task-origin Approval: the caller reads its Project but lacks `project.approvals.resolve`, or, to approve a Company Plan raised there, `project.tasks.write` |
| 403 | `company_permission_required` | Approve only: the Company Plan would give another member's open Task another agent, take it out of review, or delete it, and the caller is neither its assignee nor a `tasks.assign` holder (as a Task PATCH answers) |
| 404 | `approval_not_found` | Unknown approval, one in another company, or a task-origin one whose Project the caller cannot read |
| 404 | `work_plan_not_found` | Approve only: the Company Plan names a Company entity that no longer exists |
| 409 | `approval_conflict` | Already resolved, or `expectedRevision` did not match. Re-read; do not retry blindly |
| 409 | `work_plan_conflict` | Approve only: the current state refuses the Company Plan, for example a new Project under a key that is taken, or a finished Task the plan would change |
| 410 | `approval_expired` | The TTL elapsed. No action executes; the lifecycle worker resumes the agent with expiry context |
| 422 | `validation_failed` | Missing `expectedRevision`, or a note over its length bound; on approve, also a Company Plan the server refuses as invalid (for example a Task moved to another Project) |

The three Company Plan refusals, and `company_permission_required`, roll the
whole approve back: the Approval stays pending for someone else, or for a
later decision. A plan whose base moved is not a refusal: it is approved with
`execution.status: skipped` (above).

The Run token only raises requests. Human resolution uses the ordinary user
principal and the persisted resolver policy.

### How the gates are performing

```
GET /api/v1/companies/{companyId}/approvals/stats?since=2026-07-17T00:00:00Z
```

Grouped by `kind`. **A gate approved every time without exception is a
candidate for policy, and a gate that mostly expires is blocking work while
looking healthy** — neither is visible from a list of individual requests.

The counts are exactly the Approvals `view=decide` lists, whatever their
status (ADR 0017), and nobody is refused. A Company Plan Approval raised in a
Task chat with a Project counts when the caller holds
`project.approvals.resolve` there. Any other Approval of a Task chat counts
for holders of the Company `approvals.resolve` who read that chat. An Approval
raised outside a Task counts only for the member who raised it, when they hold
`approvals.resolve`, because only they may decide it. A Project the caller
cannot read adds nothing, and a member who decides nowhere gets zeros.

| Parameter | In | Required | Description |
|---|---|---|---|
| `companyId` | path | yes | |
| `since` | query | no | Requests raised at or after this instant. Defaults to 30 days ago |
| `agent_id` | query | no | Restrict to one agent's requests |

```json
{
  "since": "2026-07-17T00:00:00.000000Z",
  "requested": 10,
  "approved": 6,
  "rejected": 1,
  "expired": 2,
  "cancelled": 1,
  "items": [
    {
      "kind": "company_plan_apply",
      "requested": 10,
      "approved": 6,
      "rejected": 1,
      "expired": 2,
      "cancelled": 1
    }
  ]
}
```

Current rules are `company_plan_apply` and `commerce_manifest_publish`; the
grouped shape remains stable as server-owned approval rules evolve.

**Errors**

| Status | Code | When |
|---|---|---|
| 401 | `authentication_required` | No credential |
| 404 | `company_not_found` | Unknown company or not a member |
| 422 | `validation_failed` | Unknown parameter, or an unparsable `since` |

> **Changed.** This route answered `403 approval_resolver_forbidden` to
> anyone without the Company `approvals.resolve` (owners and admins; the old
> text said owners only), and counted every Approval in the Company, including
> Task Approvals in Projects the caller could not read and other members'
> direct Approvals. It now answers every member with the counts of what they
> may decide.

---

## Questions

A question asks a human for content rather than a verdict. It carries one or
more prompts and, optionally, choices.

### Ask a question

```
POST /api/v1/companies/{companyId}/questions
X-Boardly-Run-Token: brt_<64 lowercase hex characters>
Idempotency-Key: <uuid>
{
  "title": "Which environment should this deploy to?",
  "prompts": [
    {
      "id": "env",
      "prompt": "Which environment?",
      "helpText": "Staging redeploys on every merge; production does not.",
      "selectionMode": "single",
      "required": true,
      "allowCustomAnswer": false,
      "options": [
        { "id": "staging", "label": "Staging" },
        { "id": "production", "label": "Production" }
      ]
    }
  ]
}
```

There must be 1..10 prompts. Prompt and option identifiers are unique within
their scope. A prompt with no options is free text and uses `single`; a choice
prompt has 2..20 options. The response is the bare Question — `201` on first
creation, `200` on an exact idempotent replay; the status code is the whole
created-signal, and there is no `{question, created}` wrapper. Direct-
conversation questions are creator-private and answerable only by their origin
user. A task question is read by the readers of the Task chat it was asked in
and answered by those who also hold `project.approvals.resolve` on its Project
(ADR 0017); when nobody does, the ask answers `409 no_eligible_resolver`,
stores nothing, and the asking Run fails with `failureReason`
`no_eligible_resolver`.

### List questions

```
GET /api/v1/companies/{companyId}/questions
```

Same parameters and newest-first `(createdAt, id)` seek rule as approvals.
`view=decide` keeps the Questions the caller may answer: one raised in a Task
chat with a Project needs `project.approvals.resolve` there; the caller's own
direct Question and one of a Task chat with no Project need only reading it.
The Questions tab and its pending count read this view. Every "Needs your
input" in the app follows the same rule: it reads this view, or, inside a
Task chat, the Task's `project.approvals.resolve`.

```json
{
  "items": [
    {
      "id": "0197f6b0-2233-7e44-9a55-6b7c8d9e0f12",
      "companyId": "0197f4a1-9d33-7c55-b0a2-51c7e8d90f42",
      "status": "pending",
      "title": "Which environment should this deploy to?",
      "askedByAgent": { "id": "0197f4a2-1b77-7d09-8e31-6c0f2a44b7d3", "kind": "agent", "name": "Ada", "avatarUrl": null },
      "originRunId": "00000000-0000-0000-0000-0000000012d5",
      "taskId": "0197f4c8-55e0-7b12-a9d4-2f3b81c6e990",
      "conversationId": "0197f4c9-0a12-7e88-b3f7-77d2e5a01c64",
      "prompts": [
        {
          "id": "env",
          "prompt": "Which environment?",
          "helpText": "Staging redeploys on every merge; production does not.",
          "selectionMode": "single",
          "required": true,
          "options": [
            { "id": "staging", "label": "Staging" },
            { "id": "production", "label": "Production" }
          ]
        }
      ],
      "expiresAt": "2026-08-16T10:41:02.481113Z",
      "answers": null,
      "answeredBy": null,
      "continuationRunId": null,
      "createdAt": "2026-08-15T10:41:02.481113Z",
      "updatedAt": "2026-08-15T10:41:02.481113Z",
      "revision": 1
    }
  ],
  "nextCursor": null
}
```

`selectionMode` is `single` or `multi`. A prompt with no `options` is free
text.

### Read one question

```
GET /api/v1/companies/{companyId}/questions/{questionId}
```

Returns the same complete object as the list, with `answers` and
`answeredBy` filled in once the question is resolved. The visibility rule is
the list's: a task question is readable by the readers of the Task chat it was
asked in, so it follows that chat's Project (ADR 0017), a direct-conversation
question only by its origin user. Anything else is
`404 question_not_found`, including an id in another company.

### Answer a question

```
POST /api/v1/companies/{companyId}/questions/{questionId}/answer
{
  "expectedRevision": 1,
  "answers": [ { "promptId": "env", "optionIds": ["staging"], "text": null } ]
}
```

Every `required` prompt must be answered. The agent is woken with the answers
in context.

### Withdraw a question

```
POST /api/v1/companies/{companyId}/questions/{questionId}/withdraw
{ "expectedRevision": 1 }
```

Answering or withdrawing a task-origin question needs `project.approvals.resolve`
on the Project of the Task chat it was asked in; a reader without it gets `403
project_permission_required` (ADR 0017). A task chat with no Project (its Task
was deleted before Project access existed) keeps the Company rule: anyone may
answer, withdrawal needs `approvals.resolve`. For direct-origin questions, the
origin user may withdraw their private question.

**Errors**

| Status | Code | When |
|---|---|---|
| 403 | `question_resolver_forbidden` | Caller does not satisfy the question's resolver policy |
| 403 | `project_permission_required` | A task-origin question: the caller reads its Project but lacks `project.approvals.resolve` |
| 404 | `question_not_found` | Unknown question, one in another company, or a task-origin one whose Project the caller cannot read |
| 409 | `question_conflict` | Already answered, or `expectedRevision` did not match |
| 410 | `question_expired` | The TTL elapsed |
| 422 | `validation_failed` | A required prompt unanswered, an unknown `promptId` or `optionId`, or multiple options on a `single` prompt |

---

## Expiry

Every request has an `expiresAt`. A worker runs on startup and on a schedule.
It expires bounded `SKIP LOCKED` batches, closes their Inbox items in the same
transaction, and drains durable continuation receipts.

Every terminal transaction opens a continuation receipt carrying the prompt.
It names no Run: the reconciler inserts the Run, stamps its id onto the owning
request, and marks the receipt delivered in one transaction, so a restart
cannot leave only one side committed and `continuationRunId` can never name a
Run that does not exist. A terminal or deleted task, an archived agent, and an
agent the conversation's owner has not bound to a computer of theirs (ADR 0014)
abandon the receipt durably; an existing pending task Run defers it. Both leave
`continuationRunId` null, because in neither case was a Run created.
Infrastructure errors roll back for a later retry.

The abandonment records why on the receipt (`agent_not_bound`,
`agent_archived`, `task_unavailable`, `task_closed`, `task_failed`,
`task_dependency_blocked`, `project_archived`, `conversation_unavailable`,
`owner_unavailable`, `task_assignee_required`, `channel_invocation_denied`,
`project_access_revoked`) and logs it after commit. `project_access_revoked`
means the member it would have run for no longer holds `project.tasks.execute`
on the Task's Project ([ADR 0017](../adr/0017-project-access-scopes.md)).
When the cause is one the member the continuation would have run for can act
on — `agent_not_bound`, `agent_archived`, `task_failed`,
`task_dependency_blocked`, `project_archived`, `task_assignee_required`,
`project_access_revoked` — the same transaction also raises a `run_failed`
[inbox](inbox.md) item to them, saying why and what to do. A handoff
(`task_unavailable`), a task a member closed (`task_closed`: `done` or
`cancelled`) or an archived conversation is a member's own decision and
raises none; neither does a channel tag, whose channel shows it blocked, nor
an owner who is no longer an active member.
Delivery and abandonment each advance the request's `revision` and re-send
`approval.resolved` / `question.resolved`, so a client holding the decided
request learns the `continuationRunId`, or that the continuation is settled
without one. Both also move `updatedAt`, so `updatedAt` is when the request
last changed — possibly a settled continuation long after the decision — and
not when it was decided or answered. A deferral changes nothing a client reads
and sends nothing.
The daemon wake after commit is best-effort and does not promise exactly-once
external execution.

**Expiry wakes the agent.** An expired request is not a silent dead end — the
agent is re-run and told the request expired, so it can proceed differently or
stop deliberately.

---

## Realtime Events

| Event | Scope | Carries | Kind |
|---|---|---|---|
| `approval.requested` | Project readers or origin user | Complete `Approval` snapshot | Cache |
| `approval.resolved` | Project readers or origin user | Complete `Approval` snapshot | Cache |
| `approval.expired` | Project readers or origin user | Complete `Approval` snapshot | Cache |
| `question.asked` | Project readers or origin user | Complete `Question` snapshot | Cache |
| `question.resolved` | Project readers or origin user | Complete `Question` snapshot | Cache |
| `question.expired` | Project readers or origin user | Complete `Question` snapshot | Cache |

A snapshot from a task chat, or one naming a Task, goes to the readers of that
chat's Project (the whole Company when the Project is Company-visible; see
[Work inside a Project](realtime.md#work-inside-a-project)). A
direct-conversation snapshot goes only to its origin user; it never reaches
the Company.

The decision arrives as `*.resolved`, or as `*.expired` when the request
times out. Once its continuation is delivered or abandoned, one more
`*.resolved` re-sends the request at a higher `revision`, with the system as
actor — also after an expiry. Apply each like any cache frame: replace the held
snapshot when the revision is newer.

A pending request also raises an [inbox](inbox.md) item, which is what makes it
survive a closed tab.

## Lifecycle

```
                    ┌──► approved ──► (executed | skipped)
pending ────────────┼──► rejected
   │                └──► cancelled / withdrawn
   │
   └──► expired      (TTL elapsed, target gone, or work already done)

                     every terminal state wakes the origin agent
                     with a new run carrying the outcome
```

There is no `waiting_for_human` run status, and there will not be one. The run
that raised the request has already ended; the run that carries the answer has
not started yet.
