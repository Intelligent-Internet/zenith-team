# Presenting Artifacts

Use the `present_output` MCP tool when it is available in this Run. It saves
completed results in the chat and, for Task Runs, under the Task. Call it after
validation, and after the commit where the Run commits, before ending the Run.
It returns `{"outputId":"..."}`.
Identical presentations in the same Run reuse the saved Output.

## Select final Task results

For a Task Run, delivery history and the selected result are separate. After all
final deliveries, read the current Task revision with `zen task get`, then call
`finalize_task_results` with `expectedRevision` and the ordered `outputIds` returned
by `present_output` in this Run. The first is primary. This selects evidence; it
never applies suggestions, grants approval or changes the assigned review policy.
If the Task revision changed, reread it before choosing the selection again.
End the Run normally after finalization. Required review or unapplied required
suggestions keep the Task in Needs review. Do not call this in direct or scheduled
chats, which have no Task result selection.

## Native Page captures

Call `page_preview` with the stable `pageId` and optional `changeId`, inspect the
resolved content and issues, then pass its exact sequence and fingerprint:

```json
{
  "summary": "Proposed research findings, awaiting acceptance.",
  "page": {
    "pageId": "<page UUID>",
    "changeId": "<proposal UUID>",
    "sequence": 7,
    "fingerprint": "<exact preview fingerprint>"
  }
}
```

Include `changeId` for a new Page creation proposal or suggested edits. Omit it
only to deliver the current shared content. The Page title is used automatically;
omit `items`, `app`, and Studio inputs. Delivery retains an immutable capture and
canonical export without applying suggestions, approving work, or completing a
Task. A new Page appears in the shared library only after human acceptance.

## Text, media, and file Artifacts

```json
{
  "type": "text",
  "title": "MVP scope",
  "summary": "Scope and user flows for the first release.",
  "items": [
    {"role": "primary", "label": "Scope", "target": {"kind": "workspace_file", "workspaceRelativePath": "docs/mvp-scope.md"}},
    {"role": "final", "label": "User flows", "target": {"kind": "workspace_file", "workspaceRelativePath": "docs/user-flows.md"}},
    {"role": "supporting", "label": "Sources", "target": {"kind": "workspace_file", "workspaceRelativePath": "research/source-notes.md"}}
  ],
  "submissionNotes": "Checked the scope against the brief."
}
```

Choose `media` for image, audio, or video work, `text` for readable text, and
`file` for general downloads (the default). All file paths are relative to the
root of this Run's workspace, regardless of the shell's current directory.
Each Artifact has 1–32 ordered items and exactly one `primary`. Use `final` for
additional requested results and `supporting` for source or intermediate files.
The daemon snapshots and uploads the files; later local edits do not change them.

`studioCanvasPath` names the workspace-relative JSON emitted by
`zenith-studio canvas export`. The tool validates and attaches that source graph
automatically; do not also include it as an item. Leave one of the 32 item slots
for the graph. Never fabricate a Studio graph.

## App Artifacts

```json
{
  "type": "app",
  "title": "Operations console",
  "summary": "Local API and web preview.",
  "app": {
    "commands": [
      {"command": "go", "args": ["run", "./cmd/api"], "workingDirectory": "api", "port": 8080},
      {"command": "npm", "args": ["run", "dev"], "workingDirectory": "web", "port": 5173}
    ]
  }
}
```

Supply 1–8 structured commands. Each names an executable, optional `args`, an
optional workspace-relative `workingDirectory`, and a unique HTTP `port` from
1024 through 65535. The first command supplies the primary preview. Omit `items`
and `studioCanvasPath` for apps. Do not start the servers yourself: the daemon
starts and supervises them when the person opens the Artifact. Commands must
honor `HOST=127.0.0.1` and their declared `PORT`. This saves a local preview; it
does not publish an app publicly.

## Reading attached Artifacts

An attached `artifactId` identifies an immutable saved Output, not a local
path. Call `artifact_inspect` with that ID to see its title, file items and
pinned `assetVersionId`s. Call `artifact_fetch` with `artifactId` and an
`itemId` from inspection (omit `itemId` for the primary file). It downloads
only that item and returns `localPath`, `assetVersionId`, `contentType`,
`sizeBytes` and `sha256`. Read the absolute local path with your file tools.

The daemon writes into `.zenith/artifacts/<runId>/` in this Run's workspace.
It rechecks current access before using a cached copy and downloads again
if the local bytes changed. Never infer access from an ID or cached path.
A revoked share or ended Run can refuse a subsequent fetch. Saved file
versions remain the same even if someone edits the original workspace file.

Native submitted Pages include pinned canonical export files where available;
inspect their file items and document destinations. An app with no file items
cannot be fetched as a file; inspect its URL/destination instead. These tools
do not start another machine's local preview or copy its workspace.
