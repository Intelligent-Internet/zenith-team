# Zenith Team runtime

These rules apply only to Zenith Studio work inside Zenith Team. They override
shared instructions about setup, tool use, and delivery. The Run's permissions
and approval policy remain authoritative.

The current Agent runs on the user's computer. Zenith Studio supplies remote
media operations through its CLI; local tools own composition and rendering.
This execution mapping takes precedence over the shared video router and every
deeper reference that requires typed web tools or forbids direct local file and
renderer operations. Their creative, model, continuity, and review rules still
apply. Never start another Studio Assistant loop.

## References and tools

Read the immutable Run snapshot with `load_skill_reference`,
`skillKey: "zenith-studio-cli"`, and the linked package-relative path. Load
this runtime and the selected route together; reuse each within the Run.
Use a known key directly with `includeReferences: false`. Request the full inventory
only when linked guides do not provide a needed path. Do not edit the snapshot,
substitute live Agent settings, or use a separately installed skill.

In shared guides, `factory_read_skill` means `load_skill_reference`.
Web-only `factory_*` actions map to the CLI or local tools; the video adapter
provides the full mapping. Keep plans, prompt packets, review notes and source-ID
mappings in the Run workspace. Map web `/workspace/...` paths there.

For a real blocker, finish independent work and use one consolidated
`ask_human` question. After it reports a pending Question, end the Run; the
answer starts a continuation. Preserve the user's existing authorization.

## CLI and account

The Zenith Team daemon manages npm installation. Use its supplied executable
throughout the Run. It uses the account connected for this Company and Agent
through Zenith Team Desktop. The daemon pins that Agent's authentication home
and API origin; credentials stay local. Do not read or copy them. If supplied through PATH,
resolve it with `command -v zenith-studio` (POSIX) or `Get-Command zenith-studio`
(PowerShell) in the first batch. The managed launcher checks setup, account and
Project access for every operation. Separate `--version`, `auth status`,
`--help` and `projects resolve` calls are unnecessary unless the user requests
status or a command reports a relevant error.

Use that resolved executable in all examples and shared references. Do not
install or upgrade the CLI during a Run, add it to Task dependencies, switch
accounts, or read or modify credential files.

Only check Studio authentication when the user's current request needs Studio or
explicitly asks about its connection. Merely enabling the capability, or an old
Studio discussion in this Conversation, is not a reason to check authentication.
Answer unrelated requests with the other available tools.

If `auth status` reports `authenticated: false` or a command reports
`studio_auth_required`, or the upstream CLI returns an authentication error,
call `ask_human` once with title "Connect to Zenith Studio"
and one required single prompt with `id: "studio"`, `prompt: "Connect to Zenith Studio"`
and `toolSetup: "zenith-studio"`, without options or a custom answer. Briefly say
Studio needs connecting, then end the Run after the question succeeds. This
durable question survives the Run ending. Zenith Team verifies this Agent's
connection and resumes the original request automatically; do not ask for
another answer or a Continue click. Do not start a browser login or leave a CLI
process waiting inside the Run. Respect a skipped setup request.

For required model selection, ask an ordinary `ask_human` question with required
single selection and `options` containing the compatible models discovered in
the catalog (each option's `id` is the actual model ID and `label` is its display
name). Omit `toolSetup` and `helpText`. Keep the prompt short; do not list model
names in a free-text prompt or ask the user to type an ID. Preserve job IDs and checkpoints
on continuation. Later authentication errors must not cause paid work to be
resubmitted blindly.

## Execute and review

For images, voice, audio and music, follow `references/zenith-team-media.md`.
For video, follow `references/zenith-team-video.md` and its shared workflow.
A known job starts at its saved checkpoint: no model discovery, project
creation or paid resubmission is needed just to wait, download or deliver it.
Resolve this Conversation's binding once per Run and check the saved Project
matches it before resuming. The managed launcher still checks every operation.

Reuse the selected model's verified capabilities from this request when they
are still in context or the checkpoint, including after a model-selection
answer. Query again when missing, when requirements change, or after a relevant
availability/parameter error. Do not fetch the full catalog again merely to
look up the model the user just selected. Keep only compatible model summaries
and the selected model's full capabilities in context; do not print a complete
MCP envelope or unfiltered CLI catalog when only selected fields are needed.

Batch independent reads. Once the prompt and parameters are decided, run
mechanical CLI steps in one local script with success checks and checkpoints.
Keep the actual media review as a decision point before validation, commit and
`present_output`. Reuse completed work on continuation and report only checks
actually performed; authentication, credit and provider failures never justify
blind resubmission.

## One Project per Conversation

Resolve this Conversation's Project with `zenith-studio --json projects list`.
If none is bound, run `zenith-studio --json projects create --title "Project title"`.
The managed launcher reserves one Project for this Conversation and returns the
same Project on continuations and retries. Use the returned Studio Project ID for
all generation, jobs, assets, and canvas commands. Never adopt IDs from another
Conversation or a shared workspace, even if the brief or title is identical.
The binding lives in Zenith Team and outlasts individual Runs and local worktrees.
If creation succeeded remotely but binding failed, retry the same create command;
it recovers by the reservation marker without creating another paid-work context.
Do not bypass the supplied launcher to create, adopt, or replace a Project.

When generation is already requested, `projects create --title "Creative subject"`
also returns the bound Project unchanged if it exists. Use it directly to
resolve-or-create in one call; a preceding `projects list` is unnecessary.
For read-only questions or resuming a known job, keep using `projects list`.

## Managed media collection

The supplied launcher adds `models list --kind KIND --model "EXACT_ID_OR_DISPLAY_NAME"`
and `team collect PROJECT_ID NODE_ID --output-dir output`. These are Team
commands; no CLI installation or upgrade is needed. The model filter returns
only exact matches, with every upstream capability and availability flag intact.
An empty match is not permission to choose another model.

For one submitted node, `team collect` performs the ordinary authorized
`jobs wait`, `assets download` and `canvas export` steps in one process. Each
step still verifies the live Run's Project and Agent account, including after
a long wait. Keep the process/session handle if the execution tool yields;
resume that process rather than launching a second collector while it runs.
Print the complete execution result so its session handle is not lost.
Use the host's long supported wait (30 seconds for Codex execution/poll calls),
not repeated one-second yields; the CLI already polls the remote job internally.

`jobs collect` is the same managed operation as `team collect`; it never bypasses
the per-step checks. The pinned CLI 0.1.15-beta.0's `generate --checkpoint FILE` creates
a durable intent/receipt, and `jobs collect --checkpoint FILE --output-dir output`
reads its IDs without an agent-written parsing script. Collection verifies its
API, account and execution against the current Run and remote result. A known
node without a CLI receipt can still be collected using its explicit IDs.

The collector writes to a new `output/studio-*` directory inside the current
workspace, so an earlier chat's filenames cannot collide. It reports
an absolute `mediaPath` for inspection, plus workspace-relative
`workspaceRelativePath`, `studioCanvasPath` and `tracePath` for presentation,
IDs, MIME type, size and checksum. Use `workspaceRelativePath` as the final
item target and `studioCanvasPath` for the source graph in `present_output`.
Its checkpoint survives a failed download/export. Resume the failed step using
that checkpoint and an unused path; reuse verified completed downloads/canvases.
Retrying collection never submits generation, but needlessly repeats downloads.
Keep the generation checkpoint as well: it owns the prompt, references,
parameters and idempotency key recorded before the paid request.

`readyForReview` means downloaded, not reviewed or delivered. Inspect the
actual media, validate the workspace, commit and call `present_output`.
The exported mapping assumes this one node is item 0 and its original bytes
are presented. For multiple final items, export one combined canvas with the
actual ordinals. For local transformations, retain source nodes without mapping
them to changed bytes; locally assembled video still requires native publication.

## Deliver files and source canvas

Save final files inside the Run workspace; copy downloads there if needed.
Export the recorded source graph for selected generated media:

```sh
zenith-studio canvas export PROJECT_ID --node NODE_ID --output-item NODE_ID:0 --output output/source-NODE_ID.canvas.json
```

Repeat `--node` for additional source nodes. Use `--output-item NODE_ID:ORDINAL`
only when the file at that zero-based position in the ordered presentation is
that node's actual downloaded media.

If the export path already exists, inspect its Project, selected nodes and output
ordinals. Reuse it only when they match the files being presented; otherwise
export to an unused path and pass that exact path to `studioCanvasPath`.
Never overwrite or delete an existing source canvas just to retry delivery.
On continuation, reuse the recorded Project, completed nodes and downloaded files;
do not repeat paid generation because an export filename is already taken.

For a locally assembled video, follow `references/zenith-team-video.md` to
publish the reviewed final bytes before exporting its canvas and presenting it.

For images, video, voice, audio and music, call the `present_output` MCP tool
with `type: "media"`, a title, summary, and all final items. MP3 and other audio
files are media and can be played in the output card. Use `type: "file"` for
other downloads, such as a custom-skill ZIP, and `type: "text"` for text results.
Exactly one item has role `primary`; use `final` for other requested finished
results and `supporting` for source or intermediate files. For example:

```json
{
  "type": "media",
  "title": "Music",
  "summary": "Reviewed final music track.",
  "items": [{"role": "primary", "label": "Music", "target": {"kind": "workspace_file", "workspaceRelativePath": "output/music.mp3"}}],
  "studioCanvasPath": "output/source-NODE_ID.canvas.json"
}
```

Include `studioCanvasPath` only after a successful export. Zenith Team attaches
the source automatically; do not also list its JSON as a file item.

After `present_output` succeeds, reply with a short result summary. The native
output card provides preview, details, and downloads: do not repeat output file
links, local paths, sandbox links, or Markdown media embeds in chat. This replaces
shared instructions to return clickable artifact paths or inline media.
If presentation fails, resolve the error or report incomplete delivery; do not
claim success or substitute a local download link.

## Custom-skill downloads

Deliver a downloaded custom-skill ZIP through `present_output` as a workspace file.
Downloading does not mutate the immutable Run snapshot or assign a skill to
future Runs. Do not install or extract it into a provider environment. Zenith
Team owns future assignment; do not invent an import UI.
