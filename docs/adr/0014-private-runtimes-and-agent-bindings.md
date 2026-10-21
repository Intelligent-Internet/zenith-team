# ADR 0014: Runtimes are private; Agents are templates each member binds to their own computer

Status: Accepted. Date: 2026-09-22.

Supersedes [ADR 0011](0011-hosted-agents-and-invisible-private-runtimes.md)
and decisions 2–4 of [ADR 0007](0007-runtime-visibility-and-execution-consent.md).
Amends [ADR 0010](0010-agents-only.md) (a Task's assignee is a member; the
Agent is `executedBy`), [ADR 0012](0012-roles-assign-permissions-enforce.md)
(`runtimes.manage` is deleted, `tasks.assign` is added) and
[ADR 0013](0013-members-create-agents.md) (creating an Agent no longer places
it on a machine for the whole Company).

## Context

An Agent was fused to one machine: `agents.runtime_id` was `NOT NULL`, a Run
copied that runtime at enqueue, and hosting consent (ADR 0011) let any member
start work on the machine of whoever had placed the Agent there. A Task named
one assignee that was either a member or an Agent; an Agent assignee started
a Run on the Agent's machine, and a member assignee could not run at all.
Reassigning a Task did not stop the machine already working on it: the old
Run kept going and its outcome was dropped by the generation fence.

Three things were wrong with that.

Somebody else's computer ran your work. A member could start an Agent on a
colleague's laptop — files, provider sign-in, CPU and all — because that
colleague had once placed an Agent there. The rule people expect is the
opposite: a computer is its owner's, and nothing runs on it unless its owner
asked for it.

An Agent could only exist in one place. Two members could not use the same
Agent on their own machines, and the capabilities an Agent inherited were one
machine's: if the GitHub plugin was installed on the machine the Agent lived
on, everyone got it, and if it was not, nobody did.

A Task had one column for two different questions. "Who is responsible for
this" and "who does the work" are not the same answer, and squeezing both
into `assignee` made the second one impossible to express: a Task could not
say "mine, done by Lyra".

## Decision

1. **A runtime is always private.** `runtimes.visibility` and its revision
   are gone, with the visibility route, the dialog and the indicator. A
   runtime belongs to the member who registered it: nobody else sees it in a
   roster, reads it by id, or can start anything on it. `runtimes.manage`
   leaves the permission catalog — being the registrant is the permission,
   and a runtime nobody else can see needs no role to protect it. Shared
   payloads keep the same rule: a Run, its queue slot, its results, a
   channel tag's status and the activity that records them name the runtime
   only to its registrant (`null` for everyone else), and the Company-wide
   `agent.created`, `agent.updated` and `run.*` frames name no machine at
   all.
2. **An Agent is a template.** Instructions, built-in skills, capability
   policy, provider, model, thinking level, appearance and reporting line
   belong to the Company. `agents.runtime_id` and `agents.runtime_bound_by_user_id`
   are gone; `agents.provider` takes their place and is immutable — an Agent
   changes provider by being duplicated.
3. **A binding is how one member runs one Agent.** `agent_runtime_bindings`
   holds `(agent_id, user_id, runtime_id, provider)` with `UNIQUE (agent_id,
   user_id)`: a member sets an Agent up on one of their own runtimes of the
   template's provider. The provider is carried on the row and fenced by
   composite foreign keys to `agents(company_id, id, provider)` and
   `runtimes(company_id, id, provider)`, so a cross-provider binding is
   unrepresentable rather than merely refused.
4. **A Run executes on the authorizer's binding.** The enqueue kernel
   resolves `runtime_id` from the binding of (executing Agent, authorizing
   member); no binding is `409 agent_binding_required`. The claim recheck
   requires `execution_authorized_by_user_id = runtimes.owner_user_id`, an
   enabled and active owner, and the binding still present. The daemon plane
   adds the same fence on claim, heartbeat, every runtime RPC, every run RPC
   (checked against the run's runtime) and the control socket's runtime
   scope: a daemon speaks only for the runtimes its own member registered. A
   provider session is resumed only on the runtime that made it; a Run on
   another runtime gets just the watermark.
5. **A Task has an assignee and an executor.** `tasks.assignee_user_id` is
   `NOT NULL` and always a member: the person responsible, on whose computer
   the work runs. `tasks.executed_by_agent_id` is the Agent that does the
   work, or NULL when the assignee does it themself. Human work can still
   start an assisted Task chat: `execution.agent`
   names the creating Agent, or the CEO for a member-created Task, while
   `executedBy` remains the human. This uses the same assignee-owned binding,
   capacity and generation fences as other Task Runs. Only the assignee
   starts a Task Run (`403 task_assignee_required` for anyone else), and a
   Run starts automatically only when the acting member is the assignee.
   Its completion and failure notices go to the assignee as well, the member
   whose computer ran it, whoever wrote the messages it read.
6. **Capabilities are per viewer.** Plugins, connectors and availability are
   read off the viewer's own binding, so the same Agent can have the GitHub
   plugin for one member and not for another. Built-in platform skills and
   instructions are the template's and identical for everyone.
7. **Moving a Task that has run is a confirmed handoff.** Changing the
   assignee or the executing Agent of a Task with `execution_generation > 0`
   needs `confirmHandoff: true`, else `409 task_handoff_confirmation_required`,
   whose `runLive` says whether a Run is live right now. Confirming cancels
   every live Run (`task_reassigned`), advances both the execution and the
   workspace generation, and tells the member who lost the Task and the one
   who gained it — unless that member made the change, since nobody needs
   telling about their own move. A move that only changes the executing
   Agent keeps the assignee, who is told when it stopped their run. The
   "Ready to review" notices of the Task's earlier Runs close, since nobody
   can merge those Runs any more. For the same reason a confirmed handoff
   moves a Task that was `in_progress` or `in_review` back to `todo`:
   nothing of it runs and nothing is left to review, so its
   new holder starts it fresh instead of seeing work that looks live. A
   handoff to yourself with an Agent starts its Run at once and lands
   `in_progress`. Nothing moves files: whatever was never
   merged stays on the computer that made it. A Company Plan apply that gives
   a Task that has run another executing Agent is the same handoff; the
   owner or admin applying the plan, or approving it, is its confirmation.
8. **The workspace generation is the Task's identity on a machine.** The
   daemon writes it into the signed worktree sidecar (version 4) and puts
   each generation in its own root, `worktrees/<conversationId>-g<N>` past
   the first (0, the plain root). A Task handed back to a computer that
   already ran it therefore starts from that machine's Project main instead
   of continuing a stale worktree. The older roots are marked `superseded`
   — as is the root of a Run a handoff cancelled — and are never continued
   or garbage-collected until their disposition is recorded.
9. **Reassignment is one permission.** `tasks.assign` (owner, admin) or the
   current assignee may hand a Task over. A member creates Tasks for
   themselves without it.
10. **A colleague's message in a Task chat is queued, not refused.** The
    words become a run-queue item, the assignee gets a `task_run_requested`
    nudge, promotion skips items the assignee did not write, and the
    assignee's next Run materializes them in order and freezes them into its
    conversation snapshot. Only an active member's words are carried. The
    nudge closes once its message stops waiting: a Run carries it, it is
    deleted or removed, or a handoff gives the Task to another member.
11. **Removing a runtime is the registrant's, and names only their own
    agents.** The body is `{targetRuntimeId: uuid | null}`: move this
    member's bindings to another of their same-provider runtimes and
    retarget their queued Runs, or drop them and cancel those Runs
    (`runtime_removed`). The old `expectedAgentIds` plan and its two error
    codes are gone.
12. **Continuations run under the Conversation's owner.** An Approval or
    Question continuation is authorized by the Task's current assignee, or a
    direct Conversation's creator; the member who decided is recorded but
    never becomes the authorizer. A retry resolves the binding of (agent,
    parent authorizer) at retry time; no binding, no retry. A continuation
    the owner has no binding for is abandoned with reason `agent_not_bound`,
    and the owner gets a `run_failed` Inbox item telling them to set the
    Agent up on their computer and ask again — never silence while they are
    still a member.
13. **Revocation takes the member's bindings with it.** Losing membership
    deletes that member's bindings, removes the prompts and Task messages
    they left queued, and cancels their Runs. Their Tasks stay theirs,
    unready with `assignee_inactive`, until a `tasks.assign` holder hands
    them over.
14. **A Run reads only its own Tasks.** A Run
    works for its authorizer, and its own Tasks are the ones its Agent does
    for that member. A Task read sent with the Run's proof — `zen task
    list`, `get` and `dependencies` over the Run channel — sees only those:
    the list is forced onto that Agent and member, and any other Task is
    `403 task_outside_run_scope`, even one the member could read, with the
    one fixed detail "Permission denied. You can only access your own
    Tasks." and nothing of the Task — no content, label, member or Agent
    name. A readable Task's prerequisites stay readable as its
    dependencies; of the Tasks waiting on it, only the Run's own do. A
    `zen task create` sent with the proof files work only for that member,
    and from a Task Run only in its Task's Project; any other create is the
    same 403. It closes the gap a live channel Run fell through: asked
    to run a Task by its label, an Agent read a colleague's Task and acted
    out its question on the tagger's computer while the Task never started. The
    Run proof now carries the authorizer, so the server, not the daemon's
    path policy, decides the scope, and ADR 0009's "the server is
    unchanged" no longer holds for Task reads. The same scope fences the
    Task's workspace and results, which carry the whole Task though no Run
    command reaches them. The daemon admits a Task read by name to every
    Run with a channel, a Task Run included, so every refusal is the
    server's sentence; and it refuses any `%` in a channel path, since
    the server routes on the decoded path and an encoded `/` would pass a
    one-segment policy row onto another route. One read of other members'
    Tasks remains, and it is not a Run-proof read: the root Agent plans the
    Company, so its `company_plan_export` MCP tool reads the whole plan,
    every Task in full, with the member's session alone. No other Agent is
    offered that tool; the root Agent's own `zen task` reads are scoped
    like any Run's.
15. **Stopping a Run and closing a review are the work's owner's.**
    Stopping a Run acts on the computer it runs on, so only
    its authorizer or a `tasks.assign` holder may cancel it; any other
    member who can read it gets `403 company_permission_required` and the
    client shows them no Stop button. Moving a Task out of `in_review` —
    marking it done without review among other moves — decides the work
    that computer did, so it is likewise the assignee's or a `tasks.assign`
    holder's, and the client offers the review only to them.

## Alternatives and tradeoffs

- **Keep hosting consent and add per-member overrides:** the model stays "my
  machine may run your work, with conditions", which is the thing this
  decision rules out. Rejected.
- **One binding per Agent, owned by the Company:** simpler, but then two
  members cannot run the same Agent, which is the case that motivated the
  change.
- **Derive the binding's provider instead of storing it:** possible with a
  trigger or a service check, but the composite foreign keys make a
  cross-provider row impossible at rest, which no amount of Go can promise.
- **Let anyone start a Task and pick a machine:** it reintroduces running
  work on someone else's computer through the back door.
- **Refuse a colleague's message in a Task chat:** `conversation_items.run_id`
  is `NOT NULL`, so a message cannot exist without a Run; refusing would
  lose the words. Queuing keeps them and hands them to the person who can
  act on them.

## Consequences

Members set Agents up on their own computers, and the Agents page reads
"Not set up" until they do. A Task names its member and, separately, the
Agent that works for them; taking one from a colleague asks first and starts
the work over from the project's main branch. Nobody's machine appears in
anybody else's roster, so a support question about "whose computer is this
on" is answered by the person, not by the product.

An Artifact a Task Run presents stays the Task creator's, who reviews and
shares it. The Run's authorizer, the assignee whose computer ran it, gets a
`view` grant on it when they are not the creator, so nobody runs a Task and
then cannot see its result.

Agents whose registrant has left the Company become templates nobody has set
up yet, which is the honest state.
