# ADR 0010: Every worker is an Agent: departments are retired and a Task names its assignee

Status: Accepted. Date: 2026-09-15.

Amended by [ADR 0014](0014-private-runtimes-and-agent-bindings.md): a Task's
assignee is always a member, and the Agent that does its work is
`executedBy`. Every worker is still an Agent; what changed is that somebody
is responsible for each Task and runs it on their own computer.

## Context

Zenith Team shipped with a closed vocabulary of six departments. A
department was the configuration surface of the one Agent holding its key:
it carried the persona's appearance and capability weights, named a
workspace scope on disk, decided which Agent a Company Plan Task could be
routed to, and — because the CEO was "the Agent with no department key" —
stood in for the reporting tree in every client that needed to find the CEO.
Every Task also carried a second owner pointer, `department_agent_id`,
beside its assignee.

Founders create Agents themselves and should never have to think in
departments. An earlier change had already made Agent eligibility and
knowledge independent of department identity, and the `zen` CLI redesign
made the leftover visible: `zen task create --department-agent` asked for a concept
the product no longer had a screen for.

## Decision

Departments are removed, not renamed:

- A Task names one assignee — a member or any active Agent — plus its
  milestone type. The assignee is the whole routing rule: Agent-assigned work
  can start Runs, while person-assigned work cannot. There is no owner pointer
  beside the assignee.
- The CEO Agent is the root of the reporting tree (`reportsTo` is null).
  The Run context freezes that as `isRoot`; planning authority and the
  `list_agents` tool follow it.
- Appearance (`avatarKey`, `accentColor`) lives on the Agent. Migration
  000017 copies it from the department configuration before dropping the
  department tables, and it is irreversible like the Goal removal.
- The four starter Agents are provisioned as ordinary reports of the root at
  root creation, from templates. Nothing keys on their names.
- Company Plans name Agents by id (`agent_id`, `format_version` 4);
  `founder` Tasks carry none and are assigned to the founder who applies the
  plan.
- The daemon has one durable scope per Company, `company`. Direct
  Conversations of every Agent work in `company/main`; new Task worktrees
  branch from it. Every Task Run gets the Company main's Memory and
  business context mounted read-only, exactly as before, and a worktree
  retained under an older department scope keeps running there until it
  merges.
- The search route kind `department_task` becomes `task`; the starter
  journey step `activate_departments` becomes `meet_starter_agents`.

## Alternatives rejected

- **Keep departments as optional metadata or templates.** Two vocabularies
  for one thing, and every client would keep deciding "is this the CEO" from
  a null department key.
- **Keep department-scoped workspaces for the Agents that had them.** Two
  workspace models forever; each Task already runs in its own worktree,
  which makes a per-Agent scope redundant.
- **Rename department to team.** The same second concept under a new name.

## Consequences

- Breaking API change: the `departments` routes, the department schemas,
  `Task.departmentAgentId`, the `department_key` filters, bootstrap
  `departments`, and `departmentKey` on search, usage and task-report rows
  are gone; the generated client is regenerated with the spec.
- Per-department capability weights and month-to-date insights are gone;
  the per-Agent usage report remains.
- `RetainedScope` keeps in-flight legacy worktrees working; no data is
  moved on disk.
- The desktop removes the department screens and groups by Agent instead.
