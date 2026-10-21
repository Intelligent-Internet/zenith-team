# ADR 0019: Artifact document ownership and editing authority

Status: Accepted

An artifact's supported Markdown can be edited regardless of Project placement.
This change targets a fresh database; it does not backfill existing installations.

## Content and identity

An Output and its uploaded Asset Versions remain immutable evidence of a Run.
S3 holds the original files. Opening a Markdown item for editing creates one
canonical document for that Output item, recorded in `output_documents`.
PostgreSQL owns its block content, CRDT state, block identities and revisions.
The source file is never overwritten by human edits.

An Output owns its document page through `wiki_pages.source_output_id`. A Project
artifact version is a placement or review checkpoint, not the document's owner.
Deleting a placement or its Project cannot delete a standalone Output's document.
The schema pins the Company, Output, item and page together with foreign keys,
and uniqueness prevents two canonical documents for one item. Opens serialize
under the Output lock, recheck access after downloading the original, and reuse
an existing document. Project alternatives remain explicit separate pages.

The existing document engine handles both entry points. There is no second
editor, merge algorithm, block-ID allocator, draft cache or document store.
Working edits save automatically after PostgreSQL acknowledgement. A review
checkpoint freezes saved blocks and derived Markdown; repeating a checkpoint at
the same saved sequence returns the same Output document revision. Stale
checkpoint requests cannot label another editor's changes as the requested save.

Original-file downloads return the original bytes. Saved-version exports return
that revision's Markdown or native blocks. Reopening the canvas uses the current
document; explicit version links open their frozen review version. A later Run
produces a separate Output and never silently replaces human-authored content.
Request Change remains a Run action, separate from collaboration comments.

## Authority

For standalone Outputs, effective access is:

| Access | Read original/document and comment | Edit/checkpoint | Change sharing |
| --- | --- | --- | --- |
| Viewer, Company visibility, conversation grant | Yes | No | No |
| Explicit Editor | Yes | Yes | No |
| Owner or Manager | Yes | Yes | Yes |
| Public link | Original published content only | No | No |

Editing is not implied by a Company role, visibility, or membership in a Project
where someone placed the Output. A Project-owned Output's document follows its
source Project's read and task-write permissions, including the archived-Project
write restriction. Sharing an immutable Output does not grant access to its
source Project's editable content. Company Wiki publication remains a separate
surface; imported Output documents never enter Company Wiki reads or search.

All document reads, writes, block reservations, checkpoints, version exports and
presence heartbeats resolve access on the server. Mutations hold Company and
acting-membership lifecycle locks, then the Output lock used by sharing changes,
before touching the page and draft. Permission revocation fences the next write;
client caches and pending edits never grant authority. The editor retains its
existing recovery behavior when a save loses permission.

## Discussions and collaboration

One review-thread/comment store serves both Project and Output reviews.
Comments on original files or saved revisions retain that exact subject. Comments
on the working document use its page identity and the existing CRDT anchor
reconciliation. Saving a checkpoint does not move or duplicate working comments.
Only the author edits/deletes a comment; readers can reply and resolve threads.
Edits and resolutions retain revision/actor history, and request keys guard
mutation replays.

Realtime document hints capture authorized readers in the committing transaction
and contain no document text. Clients refetch through the access-checked endpoint.
Ephemeral cursors use the canonical page room and recheck access on each heartbeat.
