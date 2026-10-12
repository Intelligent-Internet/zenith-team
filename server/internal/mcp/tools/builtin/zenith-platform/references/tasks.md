# Your Task

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Identity

```
zen run context
```

Prints `runId`, `companyId`, `agentId`, `agentName`, `taskId`, `taskKey`,
`projectId`, `projectKey`, `conversationId`, `conversationKind` (`task` or
`direct`) and `commands`.

## Task

```
zen task get [<task-id|key|label>]
zen task dependencies [<task-id|key|label>]
zen task fail --reason "<evidence, max 2000 chars>"
```

`task get` returns the Task object: `id`, `key`, `title`, `description`,
`status`, `priority`, `labels`, `assignee`, `executedBy`, `blockedByCount`,
`dependenciesResolved`, `revision`. The `assignee` is always a member: the
person responsible, on whose computer the Task runs. `executedBy` is the
Agent that does the work, or the assignee again when they do it themselves.
Say a Task is executed by an Agent; never call an Agent its assignee.
`task fail` is final for this Run: it
records a business failure proven by this exact Run and ends the Task's
current execution generation. Use it only after a reasonable attempt.

## Your own Tasks only

A Task runs only as its own Task Run, which its assignee starts on their own
computer with the Agent the Task names. You can read only your own Tasks:
the ones you do for the member this Run works for. The CEO in its direct
chat, with Company planning on, is the one exception (see
`references/company.md`).

- `zen task get` and `zen task dependencies` work in every Run that has
  `zen`, planning or not. Outside a Task Run they need a Task name; in a
  Task Run, with no name they read this Task, and naming another of your
  own Tasks in this Task's Project reads it too.
- `zen task dependencies` lists the Task's prerequisites as read-only
  summaries and, of the Tasks waiting on it, only your own.
- `zen task list` shows just your own Tasks. It is offered only outside a
  Task Run, and not when your Company planning is switched off.

Any other Task is refused, by id, key or label alike, in a Task Run and
outside one. `zen task get` and `zen task dependencies` fail with
`task_outside_run_scope` (exit code `3`) and print one fixed sentence:

```
zen: Permission denied. You can only access your own Tasks. (code: task_outside_run_scope) [request …]
```

With `--output json` the same refusal is one JSON object on stderr:
`{"error":{"code":"task_outside_run_scope","message":"Permission denied. You can only access your own Tasks.","status":403,"exitCode":3,"requestId":"…"}}`.

When a read is refused, tell the person, briefly and professionally, that
you cannot access that Task and that only its assignee can start it, and
do nothing more with that Task. The refusal ends only that read: the rest
of the request goes on, and in a Task Run you carry on with your own Task.
Outside a Task's own Task Run you never do its work, your own Tasks
included: do not run its commands, create its files, ask its questions or
request its approvals, and never report it as started or done.
When a chat or channel asks you to run, execute or continue a Task, say that
only its assignee can start it on their computer, and offer what you can do
in the conversation instead.

When someone mentioned you in a channel or a group chat, your answer goes to
everyone there. Zenith Team shows you the Projects and Tasks that the person
who asked can see. Share only what the request needs; the person who asked
is responsible for what you share there. A refused read there gets the same
answer as anywhere else.

Name a Task by its id, its label (`ENG-12`) or a key that is one plain word.
A name with a slash, a percent sign, a `?` or `#`, a space, or a lone `.` or
`..` is refused before it is sent (exit code `5`); use the Task's id or
label instead.

## Follow-up work

```
zen task create --title "<title>" --milestone <milestone_type>
                [--description "<text>"] [--key <stable_slug>]
                [--executed-by-agent <agent-id> | --executed-by-self]
                [--project <project-id|key>]
```

Use it only when `zen run context` lists it. Creates a Task for the member
this Run works for: they are its assignee and it runs on their computer;
`--assignee-user` is refused inside a Run. Your own Agent does the work
unless you name another one from `zen agent list`, or `--executed-by-self`
for work the member does themselves. It starts right away when the member
has that Agent set up; when they have not, the create is refused with
`agent_binding_required` and nothing is created. In a Task Run the new Task
lands in this Task's Project, the only one the Run can look up, so leave
`--project` out; in a direct Conversation `--project` is required. The
server holds a Run to the same rules: a Task for anyone else, or from a Task
Run in another Project, is refused with `task_outside_run_scope` and nothing
is created.

File a follow-up only for a distinct next piece of work the member would
want, and at most once for it. Never file one to continue, retry or hand off
the work of your own Task: finish it here, or end it with `zen task fail`.
A new Task your own Agent does starts another Run at once, and that Run can
file Tasks too.

Creating is not deduplicated. Without `--key` the server mints a new label
every time, so repeating the command after a network error makes a second
Task; pass a stable `--key` when you may retry, and a repeat is refused as a
conflict (exit code `6`). A Company Plan — several Tasks, dependencies or new
Projects — still goes through the reviewed plan flow, not this command.
