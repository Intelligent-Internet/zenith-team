---
title: Workspaces and run environments
summary: Server-side Task data, daemon-local working files, current directory bindings, and the planned managed-only contract
---

Zenith Team has two different uses of “workspace”: the server's Task
workspace response composes product data for a screen; the company workspace
is the directory tree on a daemon's machine. A Run environment is the
generated provider configuration around a working directory. None is a new
server filesystem API.

A managed-only release is **Planned**: no direct-workdir input and no GitHub
repository references. The served baseline below still supports a
local-directory override.

## Served boundaries

| Plane | Contract | What it does |
|---|---|---|
| Server, user | `GET /api/v1/companies/{companyId}/tasks/{taskId}/workspace` | Task detail, Conversation and associated structured state; no cwd, directory listing or file bytes |
| Server, owner | Company Resource CRUD in [companies.md](companies.md#list-a-companys-resources) | Binds an existing directory to one daemon; returns a sanitized Resource projection |
| Server, daemon | Claim, prepare-lease, start and terminal callbacks in [daemon.md](daemon.md) | Delivers frozen input and controls Run authority; start accepts no cwd or environment object |
| Machine loopback | Workspace files/assets/memory and Task review below | Reads or accepts files on the executing machine under a local capability |

The existing [OpenAPI domain files](spec/) describe the contracts the server
serves. A future change to an existing operation must land with handler
validation, contract tests, and regenerated client types. Do not mark a
currently served operation `x-zenith-team-status: planned` merely because its
behavior will change, or add local-loopback routes to the server's route index.

## Current placement and resource references

The daemon's `ZENITH_TEAM_WORKSPACES_ROOT` / `--workspaces-root` selects its
managed container (default `~/zenith_team_workspaces`), not a caller-provided cwd.

| Run | Baseline cwd |
|---|---|
| Direct Conversation with matching `local_directory` | Existing directory from that daemon's frozen Resource binding |
| Direct Conversation without that binding | `<root>/<companyId>/company/main`, whichever Agent the Conversation is with |
| Task | `<root>/<companyId>/projects/<projectId>/worktrees/<conversationId>/workdir`, branched from that Project's `main`; a Task Run ignores the `local_directory` binding |
| Schedule occurrence | `<root>/<isolatedId>/workdir`, unique to Conversation and Run |

`main` is a locally initialized Git repository. Task worktrees and merging
into it do not imply support for GitHub, repository URLs, remote refs, or
automatic push/PR creation. Resource type remains exactly `local_directory`;
there is no supported `github_repo` value or `checkoutRef` request field.

The owner-plane Resource reference is:

```json
{
  "resourceType": "local_directory",
  "resourceRef": { "daemonId": "machine-a", "daemonName": "Work laptop" },
  "metadata": {}
}
```

This is an excerpt of the Resource projection, not a create body. The
baseline create body uses `daemonId`, `localPath`, `label`, and optional
`position`. **Public projection redaction does not mean local paths never
reach the server.** `company_resources.local_path` stores the input, and the
enqueue kernel copies it into `runs.context.resources` for daemon claims:

```json
{
  "resources": [
    {
      "id": "0197f5b0-4000-7000-8000-000000000006",
      "resourceType": "local_directory",
      "localDaemonId": "machine-a",
      "localPath": "/Users/example/work/company-files",
      "label": "Company files"
    }
  ]
}
```

That is the **current private daemon context**, not the public `resourceRef`
shape. Runs on other daemons do not use this entry as their cwd. Schedule
Runs omit these bindings. References and directory layout are not OS sandbox
guarantees: current interactive providers can have broader native filesystem
permissions.

## Daemon-local file and review contract

These existing loopback endpoints use **POST**. They require a loopback
peer/Host, no `Origin` header, and the rotated local-preview bearer capability
read by Electron main. They are not exposed under the server's `/api/v1` prefix
and are not authenticated with a renderer's arbitrary path or Resource id.

| Local route | Request identity | Response |
|---|---|---|
| `/v1/workspace-files/preview` | `schemaVersion: 1`, Run UUID `runId`, relative `path` | `{schemaVersion,path,filename,format,content,sizeBytes,truncated}` |
| `/v1/workspace-files/export` | Same | File bytes |
| `/v1/workspace-assets/list` | `schemaVersion: 1`, `companyId`, `scope`, sort/limit and optional query/kinds/cursor | Bounded local asset page |
| `/v1/workspace-assets/preview` | `schemaVersion: 1`, `companyId`, `scope`, relative `path` | Text or base64 image preview with MIME/size metadata |
| `/v1/workspace-assets/export` | Same | File bytes |
| `/v1/workspace-memory/list` | `schemaVersion: 1`, `companyId`, `scope` | Bounded local Memory files |
| `/v1/task-review/prepare` | `schemaVersion: 1`, Run UUID `runId` | `{schemaVersion,runId,status,baseRef,headRef,diff,message}` |
| `/v1/task-review/accept` | `schemaVersion: 1`, company/Task/Run/Runtime identities, `expectedRevision`, `expectedExecutionGeneration` | Reviewed merge result after server claim fencing |

`runId` is the database-generated Run UUID, the same id the server's
`/runs/{runId}` routes use (see `runId` in `_common.yaml`); any other form,
including an old decimal id, is `400 invalid_request`. The local error
envelope is `{schemaVersion,code,reason}`, not the server's
`{detail,code,requestId}`.
`/v1/task-review/accept` answers `403 project_permission_required` when the
server refuses the claim because the member lacks `project.tasks.execute`
on the Task's Project, and `409 task_merge_conflict` for any other claim
failure.
`workspace_unavailable` can mean that a newer Run reused the directory, it
was collected, or the exact retained identity cannot be proven. Do not
substitute another Run's files. Local source shapes are defined in
`server/internal/daemon/local_workspace_http.go` and `local_workspace_catalog.go`.

A Run that executed in place — in the directory a Company Resource binds to
this daemon — is reviewed from the signed receipt the daemon kept in that
Run's environment root (`.local-scratch/<runId>/in-place.json`): `baseRef`
and `headRef` are the short commits HEAD pointed at before and after the
Run, and `diff` is exactly the commits between them, so `main` never appears
as `baseRef` for such a Run. `main_dirty` reports uncommitted changes in the
bound directory, the same rule acceptance applies; a Run whose commit is no
longer in the tree (rewritten history), or whose directory is gone, is
`workspace_unavailable`. The receipt has no retention clock — the commits
stay in the person's repository, and a review may come days later — unlike
a managed worktree, whose record lapses with the worktree. Accepting
records the work already in the tree — there is no merge — and the Task
moves to `done` under the same server claim fencing. A client that gets
`workspace_unavailable` for a Task in review offers the person a repair
path: read the conversation and the project, then mark the
Task done by an ordinary status change or keep it in review — never a
silent done.

Requests are capped at 16 KiB. Text preview is capped at 1 MiB, file export at
100 MiB, image input at 4 MiB (8192 maximum side and 40 million pixels), and
Task review diff at 4 MiB. Relative-path validation rejects traversal,
symlinks, special files and anything under `.git` on every route.
`/v1/workspace-assets/*` additionally hides dot-prefixed names,
dependency/build directories, code-project trees and source-code files, while
`/v1/workspace-files/*` reads any other regular file inside the exact Run cwd,
dotfiles the Run generated included. Directory listing and Memory reads have
separate bounded counts/depths in the local catalog implementation.

These endpoints describe daemon capabilities, not proof that each current
frontend screen wires them. A browser cannot fetch these files from the
server, and a completed Run does not guarantee its cwd is still retained.

## Planned managed-only release

The following is design, not the currently accepted API:

| Concern | Planned contract |
|---|---|
| Placement | Server-owned `context.workspace = {"schemaVersion":1,"mode":"managed"}`; existing frozen execution profile determines main/Task/Schedule placement |
| Runtime support | Validated registration metadata `workspaceContractVersion: 1`; all claim paths gate v1 execution on the exact Runtime capability |
| Resource inputs | No supported external Resource types in v1; no `localPath`, cwd, repository URL/ref or execution-mode input can change a managed Run's files |
| New directory binding | Existing owner POST is retired in a coordinated implementation; normal validation/access checks precede `422 validation_failed` explaining unsupported directory execution |
| Old directory rows | Remain listable/removable during retirement; unlink never deletes source files or retargets an already frozen Run |
| Old execution contexts | Drain already executing Runs; terminalize pending directory-bound contracts with actionable guidance, without silently rewriting target files or retrying into another cwd |
| On-disk resource manifest | Fresh `<runEnvRoot>/context/resources.json` with schema version, company/Conversation/Run ids and `resources: []`; no implicit source discovery |
| Run artifacts | Fresh per-Run generated environment, stable Task cwd; no prior tokens or stale managed config; no generated files in acceptance diff |
| Retention | Terminal status alone cannot delete unmerged authored work; exact disposition proof and retention are required |
| Manual acceptance | Local `reviewToken` binds the displayed diff and base to exact Run/root identity; changed bytes require a fresh review; existing server revision/generation/claim checks still apply |

This design adds no new active OpenAPI route or public DTO. In
particular the empty local manifest is not a Company Resource API response,
and a future Resource type must not be added to client types before its
contract is implemented. Local-directory **reference input**, if introduced
later, requires its own explicit bounded import design; old binding consent
does not authorize it. GitHub references remain deferred.

The existing `waiting_local_directory` status and wait endpoint also serve
contention on managed `main`; removing direct-folder support does not by
itself remove that wire state. Likewise, managed placement does not deliver
OS process confinement or a server-side backup. Existing stored path snapshots and
local authored data must be disclosed until their separate retention and
removal work is implemented.
