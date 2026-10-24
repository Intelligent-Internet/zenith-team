# ADR 0021: Artifact identities and immutable snapshots

Status: Implementation in progress

This is a fresh-install design. No identity backfill, compatibility aliases or
conversion through Markdown exports is supported. This record supersedes ADR
0019's Project wrapper identity and separate document/version checkpoint model.
It also supersedes that record's Output-derived live-document authority.

## Ownership boundaries

An Artifact identifies ongoing work. A Page is a document-backed Artifact and
uses that same ID. Its blocks are local document nodes, addressed by Artifact ID
and block ID. A placement identifies where work appears; it never changes the
work's owner or grants access to it.

A Company Wiki Entry is a separate Artifact type, owned by the Company knowledge
domain. It shares the block/Yjs engine with Pages, but retains Wiki publication
permissions, collections, unpublished drafts and a published-snapshot pointer.
It has no Project owner. Project deletion removes Project placements and reviews,
not Company knowledge. Project surfaces read published Wiki snapshots only,
including search, history, live embeds and immutable captures.

The current storage cutover replaces `project_artifacts` with `artifacts`.
The root carries the content type and Project or standalone ownership. The typed
`wiki_pages` payload uses a same-ID composite type foreign key; the
`company_wiki_pages` view selects the Wiki type explicitly. Absence of a Project
or Output is not a test for Wiki identity.

## Saved states

Three identities answer different questions:

- The document sequence identifies an acknowledged collaborative state and its
  automatic change history.
- An Artifact Snapshot is immutable evidence. `artifact_snapshots` owns the ID,
  Artifact, number, title, theme, source sequence and schema version.
  `document_snapshots` is the same-ID document payload. A document checkpoint
  does not create a second Project version wrapper.
- A Run Delivery records a submission and its provenance. The delivery identity
  remains distinct from the ongoing work and its snapshots.

Snapshot numbering belongs to the Artifact. Re-registering an existing document
snapshot reuses its identity. Creating a checkpoint freezes a new saved state.
Snapshot metadata and payload are written in the same transaction. Normal CRDT
saves continue to use document history rather than creating deliveries.

A typed Artifact cannot switch between Page content and file/application content.
Importing an Output's Markdown creates or reopens its canonical Page with its own
identity; it does not append editable content to the source delivery's history.
The import link remains explicit provenance.

## Collaboration and suggestions

Native blocks and Yjs remain the authoritative editing engine. New drafts start
in the native schema, with no legacy Markdown-state upgrade. Interactive
composition source is text content inside a `uiDocument` node in that same CRDT,
so targeted edits and comment anchors retain unaffected text identities.
Incomplete source is a valid editing draft; publication requires complete source.
Preview interaction state stays local to the viewer.

Suggestions are privately persisted for the member who requested the Agent work.
Page editors do not receive another member's proposal feed or review authority.
The browser reviews text suggestions in isolated CRDT clones that follow live
Page updates; speculative edits never enter the shared document's sync provider.
Leaving the Page retains the proposal. Accept means merging the proposed text
operations into the latest Page, not applying exactly the preview previously
seen. Acceptance of a selection consisting entirely of stored text CRDT updates
ignores a changed review fingerprint, while still validating current access,
proposal decisions, grouping, applicability and the resulting document under the
write transaction. Structural selections and other review actions retain the
fingerprint check. Already-reflected edits resolve without reinsertion. A deleted
or recreated target is not resurrected or relocated: the proposal remains
available to reject or revise, with no document changes applied.

Suggestions retain their immutable base and per-operation CRDT updates. Accept
applies selected updates to the current document atomically. Reject records the
review; revise records feedback and requests another contribution from the
originating Agent. There is no manual merge action. Structural guards still
protect operations that cannot safely apply after the addressed structure has
changed. Task-result selection retains its separate requirement to apply the
required suggestions; accepting a delivery does not replace that operation.

## Live Page access

Page document reads, CRDT saves, block-ID reservation, checkpoints, history and
cursor presence use `/companies/{companyId}/pages/{pageId}/document`.
The browser keys recovery and collaboration by Page identity. The former
Output-based document endpoints and version-based write/history endpoints are
removed. Page discussions, private suggestions and temporary Agent previews also
resolve directly by Page ID, including standalone Pages. Live threads have no
foreign key to a delivery or snapshot. Snapshot discussions remain distinct.

Live access resolves through `artifact_access`: Project permissions for
Project-owned work, Company knowledge rules for Wiki, and ownership or explicit
Artifact grants for standalone work. Writes lock the Company, acting membership,
Project when applicable, and Artifact before the document. Importing requires
source access; reopening an existing import also requires live Artifact access.
Losing access to, or deleting, the import source does not revoke or delete its
independently owned Page. Source deletion clears the provenance pointer.

Artifact grants, sharing service, access API and standalone Page discovery use
Artifact authority. The sharing dialog manages explicit person grants and Company
read visibility, guarded by an access revision. Project and Wiki access remains
inherited. Existing Output sharing does not grant access to a standalone Page.

## Retained files and app launch configurations

Ordinary file/media/app submissions now create a stable Artifact and an immutable
`retained_snapshots` payload. Snapshot-owned items retain exact Asset Versions
with restricting foreign keys. Deleting a Run delivery removes its
`delivery_snapshots` links, not the Artifact, snapshots or files. Re-submitting
with an explicit `artifactId` saves another snapshot of that work. A new identity
is created when that field is omitted; titles and Run IDs never infer identity.

`present_output` and the HTTP presentation response return separate `outputId`
(delivery), `artifactId` and `snapshotId` values for these submissions. Task Canvas
automatic registration places that identity once. Re-delivery does not create a
wrapper or move an authored card. Current content, exact snapshot reads and file
downloads use Artifact authority independently of delivery sharing. Project-owned
content follows Project permissions; standalone content follows Artifact grants.

An app snapshot currently retains structured launch commands, not executable
source. It must not be presented as a portable application bundle. Immutable
Page capture still refuses an embedded live app.

## Deliveries, discovery and references

Native Page captures are Artifact snapshots with a same-ID presentation payload.
`delivery_snapshots` links each Run submission to its exact capture; a capture can
appear in several deliveries. Capture reads expose those delivery IDs separately.
The evidence retains proposal/base state independently of accepted Page content.
Deleting a delivery does not delete its capture or the live Page.

Discovery lists readable Page, Whiteboard, file, media and app identities.
Company and Conversation work lists collapse deliveries into their accessible live
identity; exact Run views retain delivery records. Restricted or archived live
work does not hide separately readable delivery evidence. Pending Page creations
stay out of ordinary discovery.

Page reference blocks select `live` content or an exact `pinned` snapshot. Canvas
placements select an exact snapshot or the live Page draft. Neither follows an
Artifact's accepted-version pointer. Task result selection and Wiki publication
retain their own domain-specific review and publication rules.

## Immutable interactive capture

The viewer captures acknowledged inline source, local state, refs, memo values,
instance identity, theme, viewport width and renderer version. The server checks
that source against the retained document, bounds and validates the JSON, and
retains it with the capture. Included components without a mounted viewer use
explicit initial-state defaults. Declared data observations are resolved under
source authority and retained with their language/catalog versions.

Replay uses the retained state and disables effects and host actions. Unknown
renderer versions or malformed state are unavailable rather than rendered as a
live component. Non-JSON state is rejected explicitly. Capture does not retain
remote asset bytes or executable app bundles. Exports retain inline source and
state as evidence; they do not silently drop the composition.

Agent completion and queue promotion fence membership, Project access and
Artifact grants. Existing Conversations precede Artifact and thread locks; a
review retries if a first Conversation appeared while it waited for the Page.
Standalone document Runs cannot use Project-wide reads to enumerate other Pages.

## Remaining cutover

These parts of the target are not implemented by the identity/snapshot migrations:

- Whiteboard identity, live access, file authority, chat sharing and discovery now
  use Artifacts. Finish immutable public publication and ownership recovery;
  the former Output public-link path does not publish independent Boards.
- Complete manual Canvas registration and Studio source ownership. Ordinary
  file/media/app submissions and native Page deliveries already link exact
  snapshots. Retained executable app bundles remain separate from saved launch
  configurations.
- Complete the broader Conversation-sharing and ownership-lifecycle cutover.
  Standalone Page comments, suggestions and previews already use Page authority.

These broader boundaries and the regression-test pass are deferred. The scoped
Page delivery, discovery, standalone review, explicit-reference and interactive
capture implementation is complete; it has not yet passed the deferred test gate.
`outputs` continues to identify Run deliveries, not the live work identity.
