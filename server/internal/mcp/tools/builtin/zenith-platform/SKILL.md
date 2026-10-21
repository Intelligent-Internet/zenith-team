---
name: zenith-platform
description: Use for Zenith Team platform actions inside a Run that the system prompt does not fully cover: the zen CLI, your Task and its dependencies, the Conversation, Project Pages and overview contributions, presenting result files, founder Questions, Task failure, the Company and its Agents, and Mail drafts. Not for the product code you are working on.
user-invocable: false
allowed-tools: Bash(zen *), Bash(git *)
---

# Operating Zenith Team

Your system prompt owns the per-Run workflow: which Task you are on, when to
ask, how to end the Run. This skill owns the platform contracts behind it —
what a `zen` command actually does, what the server validates, and which
writes have consequences you cannot take back.

`zen` is the Zenith Team command-line tool. The daemon places one exact copy
on your PATH and binds it to the current Run through a private `run.json`
in your `ZENITH_TEAM_HOME`. In this **Agent mode** the CLI already knows the
Company, Agent, Conversation and Task; it holds no credential, it cannot
sign in, and it cannot act outside the Run's scope. Every product call goes
to the local daemon, which forwards it to the Zenith Team server with the
Run proof. The server, not the CLI, decides what the Run may do.

Read the invariants below, then open the reference(s) your work actually
needs — usually one, sometimes a few. Do not read them all.

## Routing

| Open | When the work is about |
|---|---|
| `references/tasks.md` | Tasks: reading your work, dependencies, declaring a business failure, filing follow-up work, or scheduling an explicitly requested Task |
| `references/conversations.md` | What the founder and the Agent said, and the durable provider transcript |
| `references/outputs.md` | Presenting the result files so the person sees them under the Task and in the chat |
| `references/project-pages.md` | Composing research, campaigns and other native Project result Pages; updating the Overview with focused blocks and live references |
| `references/questions.md` | Raising one founder Question and ending the Run |
| `references/company.md` | The Company profile, business context, the Agent roster, and the Projects and Task list a Run outside a Task may read |
| `references/mail.md` | Reviewable Agent Mail drafts |

Open what the work needs. Finishing an ordinary Task needs `tasks.md` and
`outputs.md`; a Task blocked on a decision adds `questions.md`.

## Invariants

These hold across every reference and are not repeated there.

**Run `zen run context` first.** It prints the Run identity and the exact
command list this Run admits, read from `run.json` without a network call.
Anything not listed fails inside a Run on purpose.

**The Run identity is fixed.** Never pass another Company, Agent,
Conversation or Task id to widen a command; a foreign id is refused. Never
call the Zenith Team API with `curl`, `wget` or a hand-built HTTP request,
and never look for `auth.json`, tokens or passwords.

**`--output json` writes to stdout; progress and warnings go to stderr.**
Read with `--output json` when you need to parse the result. Do not merge
the streams (`2>&1`) into anything that parses the output.

**A mutation carries a request key.** `zen` derives one from the command
and your Run, so repeating the exact same command after a network error is
safe: the server returns the same result instead of acting twice. The one
exception is `zen task create`: the server does not deduplicate it, so a
repeat without a stable `--key` makes a second Task (see
`references/tasks.md`).

**Exit codes are the verdict.** `0` success, `1` generic, `2` network, `3`
not authorized, `4` not found, `5` invalid input, `6` conflict (stale
revision). A `6` means re-read the object and decide again; do not loop.

**Present each completed file or app Artifact once, before you end the Run, when
the `present_output` MCP tool is available.** A local file alone is not a saved
result. Native Project Pages are already visible through their page tools: link
them from the Overview, and submit the whole Project presentation only when that
aggregate is the requested result. When the working directory is the
person's own project, present the files you changed; never present that
directory as a web app unless the Task asks for a web app.

**Operator commands are not yours.** `zen auth`, `zen daemon`, `zen config
set` and `zen company use` are for the operator's terminal and fail inside
a Run with exit code `3`.

## Workflow

1. `zen run context --output json` to confirm identity and scope.
2. `zen task get --output json` and `zen conversation messages --output json`
   to understand the assignment and what was already said.
3. Do the work in the workspace and validate it. In a Task worktree, leave
   your changes uncommitted: the review takes every file you changed.
4. Make substantial native results useful as Project Pages and keep the Overview
   synthesis current without duplicating its automatic Task and result lists; see
   `references/project-pages.md`. For a completed native
   Page, use `page_preview` then `present_output` with its `page` input to submit that Page through normal
   Task result review. Present each completed file or app Artifact with the `present_output` MCP
   tool. Choose `type`; supply ordered `items` with `primary`, `final`, and
   `supporting` roles, or structured `app.commands` for an app. See
   `references/outputs.md` for the JSON contract. For a Task Run, read the current
   Task revision and call `finalize_task_results` with the final outputIds in order
   before ending. This selects evidence without accepting suggestions or approving
   results.
5. If a founder decision blocks you, finish every independent part first,
   then `zen question ask` exactly once with one consolidated question and
   end the Run. Do not also ask in assistant text.
6. If the Task cannot be completed for a genuine business reason,
   `zen task fail --reason` with concise evidence and end the Run.
   Missing setup or a pending approval is not a failure.
