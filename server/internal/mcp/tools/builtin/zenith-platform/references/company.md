# The Company, its Agents and Projects

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Company and Agents

```
zen company get
zen agent list
zen agent get <agent-id>
zen project list                        (outside a Task Run)
zen project get <project-id>            (outside a Task Run; a Task Run: its own only)
zen task list [--status a,b]            (outside a Task Run)
zen task get <task-id|key|label>
zen task dependencies <task-id|key|label>
```

Outside a Task Run you read every Project the member this Run works for
can read, but of the Tasks only your own: the ones you do for that member.
When someone mentioned you in a channel or a group chat, you read the
Projects the person who asked can read; everyone there reads your answer,
so share only what the request needs. `zen task list` shows your own Tasks,
and reading any other Task is refused with "Permission denied.
You can only access your own Tasks." (see `references/tasks.md`). When your
Company planning is switched off, the Project reads and `zen task list` are
not offered; `zen task get` and `zen task dependencies` still are. The CEO
is the one exception: it plans the Company, so in its direct chat, while its
Company planning is on, its Company Plan export and its `zen task` reads show
every Task the member reads. Mentioned in a channel or group chat, or in a
Task Run, it reads only its own Tasks like any Agent and is not offered the
export. Nothing it reads makes another Task its to carry out. No other Agent
is offered the export. Every
Task belongs to exactly one Project, chosen at creation and never moved; a
Company Plan names the Project each Task lives in, and, when your Run lists
it, `zen task create --project` names it for one Task (see
`references/tasks.md`). The CEO outside a Task is not offered that command:
its proposed work goes through the reviewed Company Plan.

Changing the roster (`zen agent create|copy|update|archive|restore`) is
operator work that no Run channel admits; ask the founder for it instead.
