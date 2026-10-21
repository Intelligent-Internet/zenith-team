# ADR 0020: Time-based document history

Status: Accepted

The normal artifact editor presents one autosaved document and a History view.
Numbered review versions are not the editing model. Task Outputs are immutable submissions. Manual review snapshots are retired;
Task review applies to submitted work while the live document remains editable.

## Recording and reconstruction

`document_changes` belongs to a Company/page and is ordered by the draft's saved
sequence. Every accepted content save records its authenticated user or proven
Run's Agent, server save time and CRDT delta in the same transaction as the draft.
Replayed saves that change nothing produce no extra history. Initial documents
and periodic full checkpoints bound reconstruction to at most 63 deltas. History
is retained with its owning page; no pruning job silently removes recovery data.
Checkpoints store their state once, rather than duplicating it in a delta column.

Content records are append-only. A checkpoint's human-readable name is a mutable
annotation; naming it does not create another content save or publish the document.
This schema targets a fresh database and has no backfill or compatibility path.

## Reading and attribution

Both standalone Output and Project entry points resolve the same document and
source permissions before listing, reading, naming or restoring history. Canvas
history reads reject Company Wiki targets because published reference access does
not authorize reading unpublished draft saves. Lists
use sequence cursors and bounded pages. Time filters use server save timestamps,
not a device's clock: offline edits appear when accepted by the server. The UI
renders timestamps in the reader's local time and groups by day or minute.

Contributor filters select that contributor's accepted saves. The default text
comparison uses the preceding save in the complete timeline, independently of
filters. A saved state always includes the shared document at that point; it is
not a reconstruction containing only one person's edits. Agent actions are
identified separately from the member authorizing their Run.

## Restoration and review

Restoring content requires current edit authority and the exact draft sequence
observed by the restoring user. A concurrent save rejects the restore. Restoration
is a new CRDT change against the current document, retaining causal deletion
history and block identities; it never replaces the current CRDT with old bytes.
The restore, history receipt, comment-anchor reconciliation and notification
commit together. All intervening history remains available. A stale client may
replay acknowledged updates harmlessly; new stale edits enter recovery through
the existing structural-sequence fence.

The history preview and comparison concern document content. Restoring it does
not change Project placement, title, theme, permissions, original S3 files,
published Wiki content or Task review decisions.

## Immutable deliveries

The shared document is canonical current content. A Run output is the immutable
snapshot delivered by the Agent. Editing or restoring the shared document never
rewrites that snapshot, creates a new Run output, or changes Task result selection
or approval. Delivery freshness reflects changes to shared content. Human output
resubmission is not supported. Company Wiki retains its independent draft/publish
boundary.

Suggestion applications and undo operations are accepted document saves. History
links those saves to their review receipts and retained before/after captures,
separating the proposing Agent from the member who accepted the change.
