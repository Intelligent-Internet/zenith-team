# Zenith Team server — code map

Where things live. The route table (`internal/handler/route_table_test.go`)
is authoritative for *what* the API exposes; the
per-domain charters and the rules for adding code to a domain package live in
the repository root's [`AGENTS.md`](../AGENTS.md) ("Server domain map"). This
file is the directory index. Each top-level directory carries a short README
stating the rule that governs it — [`cmd/`](./cmd/README.md),
[`internal/`](./internal/README.md), [`pkg/`](./pkg/README.md),
[`migrations/`](./migrations/README.md), [`tests/`](./tests/README.md).

Vocabulary: company, task, run (one agent execution, one queued execution),
agent, conversation, daemon (the machine-side background process), runtime (a
provider instance a daemon registered in a company), provider = claude | codex.
The execution layer follows the reference architecture; where this map names a
convention without a local rationale, that is the provenance.

## Whiteboards

A Whiteboard is an Artifact (`outputs.type = 'whiteboard'`) that a member makes
in a chat: `canvas.CreateWhiteboardInTx` (`board_create.go`) inserts the Output
and its Board under one ID inside the discussion send that shares it into the
chat (`DiscussionService.Send` with `whiteboard`), or, in an Agent or Task chat
that has no message to carry it, inside `ConversationService.CreateWhiteboard`
(`conversation/whiteboard.go`). `BoardService.Rename` retitles it and tells
every reader (`whiteboard.renamed`). Its title, owner, grants,
chat sharing and public link are the Artifact's (`execution/artifact_sharing.go`),
and `output_access` is the one access rule: read at level 1, edit at 2, share
at 3. `internal/canvas/board*.go` owns the live scene: atomic scene commands,
fixed-schema note CRDTs, lazy payload hydration, bounded JSON/Markdown context
and retention (`board_retention.go`), in `boards.sql`, `board_files.sql` and
migrations 107 to 109. `board_access.go` authorizes every call against the
Artifact (`boardScopeLock`; a write also locks the Output's row against a
sharing change) and owns the live Run/Task-generation fence (Run writes hold
the Task and Run in share mode). A Run acts with its authorizing member's
access. `board_public.go` reads a Board through its Artifact's unrevoked public
token. `BoardScope` supplements the canonical member principal. Member routes
hang off `/artifacts/:artifactId/whiteboard`, Run-proven ones off
`/runs/:runId/agent-artifacts/:artifactId/whiteboard`, and both reuse the same
handlers and mutation services. Agents reach a Whiteboard through the Artifact
tools (`internal/mcp/artifact_tools.go`): `artifact_inspect` reads it when the
Artifact is a whiteboard and `artifact_edit` changes it, both through
`internal/mcp/tools/whiteboard.go` on the Run routes, answering with one text
reading beside native images. Every Run of a chat freezes the chat's
Whiteboards its authorizer can open (`chatWhiteboards`): a tag those shared
into its channel, an Agent or Task chat Run those made in it
(`execution/run_context.go`). The Run's prompt names them.

Agents work where their person looks. `board_agent_view.go` is the compact
reading behind `artifact_inspect` (`POST …/agent-artifacts/:artifactId/whiteboard/view`):
by default the area the Run's authorizer is looking at (`BoardFocus`, which the
handler reads from presence), one line per object with a short ref (the shortest
unique ID prefix of eight or more hex digits), nearby strokes grouped as one
drawing, within 16 KiB. boardly-mcp keeps each Run's read set of refs and
revisions, so `artifact_edit` commands take refs and fill revisions themselves
and refuse to overwrite what the turn has not read. `artifact_edit` ops
(`POST …/compose`) add content by intent: `board_compose.go` validates and
compiles it, `board_layout.go` measures text at the editor's metrics with the
bundled font and lays out grids and left-to-right layered diagrams,
`board_sketch.go` turns SVG path data into pen strokes,
`board_compose_sequence.go` and `board_compose_usecase.go` lay out sequence and
use case diagrams, all before the Board
lock; under it `board_place.go` places the request as one group (in a frame,
beside the Run's earlier work, inside the requester's view, else the nearest
free space) and the batch applies through `boardApplyCommands` under a receipt
that hashes the intent. Diagram notation lives in `board_shape.go` (shape kinds and compartments),
`board_connector.go` (connector line, markers, routes and presets) and
`board_diagram.go` (the route and outline geometry the previews and layouts
share).

`BoardMediaService` (`board_file.go`, `board_visual.go`) owns the files a Board
references and the images rendered for Agents. A file is an ordinary attachment:
`POST /files` registers the member's own unshared upload or a file they can read
on another Board, validating images and storing a bounded preview attachment.
Image and file items place exactly one registered file. Image decoding shares one weighted memory budget and concurrent attaches of
one upload validate it once. Reading a Whiteboard
automatically requests visual context: `board_visual.go` starts with an overview
of the whole Board, groups nearby content, separates distant groups and tiles
large regions under a region plan hash; it hydrates only the requested
page in the read snapshot and renders previews outside the transaction with
the shared `board_export.go` renderer. Each region renders within a work budget
on at most half the CPUs and stops when its read is cancelled; a region over
budget is skipped and named. Rendered PNGs are ephemeral. The provider
adapters retain image metadata in completed tool transcripts and omit inline
pixel bytes; the model still receives the original native images.

Items/edges use ActorRefs for `createdBy`/`updatedBy` and optional Run
attribution; the database also retains the authorizing member. Board presence
also identifies Agents. `board_operations` keeps member/Run-scoped request
receipts as hash, created IDs and sequence only; replays read current state and
leave out objects a later commit changed. The separate `board_changes` feed keeps
the latest 1,000 sequences and the last ten minutes (at most 20,000) and falls
back to a snapshot after a gap. Retention (`board_retention.go`) works in batches
that skip locked rows, purges tombstones and receipts after 30 days and releases
files no item places after 30 days. Writes serialize on the
Board row lock behind Company/member locks that fence revocation; reads run in a
lock-free `REPEATABLE READ READ ONLY` snapshot. The post-commit
`whiteboard.changed` event carries only the Board ID and committed sequence;
`cmd/server/realtime_routing.go` sends it to the Artifact's readers who have the
Board open. Access changes travel as the Artifact's own `artifact.access_changed`. Presence (`realtime/canvas_presence.go`, one Redis script per
room, capped per room and per person) travels on the `EphemeralRelay` to readers
who currently have the Board open. A browser session reports the Board area
it shows and whether its window has focus, and a person's last view outlives
their session for ten minutes. An Agent's session is not one of its person's
tabs: the handler moves it with every read, edit and compose (cursor, selection,
work area) and it is removed when its Run ends.

## Company Wiki

`internal/wiki` owns human-authored Company pages, collections, immutable
revisions, archive status, personal favorites and the current directed link
graph. `handler/wiki.go` exposes the Company-scoped HTTP API; `wiki.sql`
supplies active-member reads and transactional writes. The owner/admin
`wiki.publish` permission is checked at the route and again under the shared
Company and acting-membership locks before a write (the exclusive Company lock
is never taken; lock order and invariants are in company-wiki TECH §4.1). Members read; Run actors have no
route under `/wiki/`. `wiki/document.go` owns the shared Wiki block draft and its
metadata. Editing saves the draft only; `PublishPage` compares both
`expectedRevision` and `expectedSequence`, freezes the block snapshot, updates
current links and emits `wiki.*` events after commit. History restores into the
draft and requires explicit publication. New unpublished pages are visible only
to publishers; ordinary member and Agent reads stay on published content. The browser invalidates
its Wiki caches on those events. The only Agent write is `AgentWriteService`
(`wiki/agent_write.go`, `wiki_agent_write.sql`): the founder's `company_research`
Run saves up to 12 pages a call and 40 for the whole company research through `POST
/runs/{runId}/agent-wiki/pages`. The write transaction re-proves the Run
(running, `runs.context.executionProfile.source`, authorizer, `wiki.publish`,
the `wiki` switch) under the shared Company fence, the Run row and the Company's research-write advisory lock, upserts by key among the pages the
Run created (`run_wiki_writes`), resolves `[[key]]` placeholders, publishes
through the same internals as `PublishPage`, then rebuilds link edges once every
page of the batch is published. Every other Agent Wiki route is read-only: `AgentReadService`
(`wiki/agent_read.go`, `run_wiki_reads.sql`) serves the Run-proven search, browse
(`wiki_list`) and read routes in `handler/wiki_agent.go` (outside `/wiki/`, checked against the
Run token, the per-Agent `wiki` capability switch and the membership fence) and
records each search, browse and the exact revision each read returned in `run_wiki_reads`
in the same transaction, with the human evidence views. Page writes also
rebuild `wiki_artifact_refs`, the index of `[[artifact:…]]` citations; a newly
added citation is checked against `output_access` for the author in the same
transaction, and citations grant nothing. General Wiki proposals and custom grants remain future work. The Project
Page surface below owns its own current Task Run admission and does not
enable general Wiki publication.

## Message plugin tags

`GET /companies/:companyId/message-connectors` projects executable connectors
on the sender's own bindings for the conversation's current Agents. The `$`
picker groups these as Plugins beside Skills. `conversation/message_connectors.go`
validates every tagged Agent and stores exact contract receipts in
`message_connectors`, plus the sender's frozen placements in
`message_connector_bindings`. A plugin-only message never invokes an Agent.
`execution/run_message_connectors.go` populates `run_connector_selections`;
claim checks policy, fresh discovery, Agent denials and the frozen Runtime,
and cancels an unavailable selection without widening its scope. Retries keep
that exact contract. Receipts never install or enable a plugin.

`internal/agent/connectorselection` shares exact Connector eligibility and Agent capability denials with message admission and Run freezing. It breaks the Agent-service → Conversation/Execution import cycle; credentials remain daemon-local.

## Custom Agent skills

`internal/agent/skill*.go` owns cloud imports and Agent installations;
`handler/agent_skills.go` exposes preview, confirm, inspect, edit files, enable and remove.
Immutable `skill_versions` bytes live in private object storage, with PostgreSQL
metadata and `agent_skills` as the current selection. Enqueue freezes references
and `run_skills` retention in `execution/run_skills.go`; retries inherit them.
Message `$skill` selections use the conversation-scoped `GET /companies/:companyId/message-skills`
catalog (active participant Agents only, refreshed on membership changes) and structured skill/version references in direct chats, channels and group DMs
(Task messages do not expose the picker). `conversation/message_skills.go`
pins each send in `message_skills`, owned by its Conversation and keyed by sender
and request key, before queueing or invoking Agents. Every tagged Agent receives
the same versions. Selections override installed names only inside that Run;
they never change Agent installations. A skill alone never invokes an Agent.
Daemon cache hits reuse verified bundles; selected keys tell the Agent which
local SKILL.md files to load. Two distinct selected versions with the same name
are refused. Message pins retain versions until the Conversation is deleted.
`contentbundle/skill_package.go` verifies exact binary/text bytes and hashes.
The daemon cache and Run snapshot feed both native provider skills and MCP.
Claude uses a Run-owned additional directory, including for local workspaces.
`skillSetHash` fences provider resume when a future Run's selection changes.
See [Skills technical design](../docs/specs/skills/TECH.md).

## Huddle context tools

`conversation/huddle_card.go` creates one immutable source-discussion receipt per
new Huddle and batch-hydrates its live summary through `huddle_cards.sql`.
Migration 78 links the receipt from `huddles.source_item_id`. Source-audience
invalidation uses the discussion dispatcher; opening retained chat still requires
the separate Huddle chat grant.

`conversation/huddle_invitation.go` hydrates recipient invitation reads with the
caller name and source conversation through the canonical Discussion access
projection. Invitees outside the source audience receive no source title.
Realtime invitation frames retain identity/status only; missed-call Inbox
receipts name the caller and retain the invitation ID in their deep link so the
desktop can fetch currently authorized context before presenting it.

`conversation/huddle_transcript.go` checkpoints bounded live-model text under the
current attempt capability and freezes invitation identity across recoveries.
Migration 77 adds `huddle_transcripts`; finalization posts one immutable system card
to the participant-only Huddle chat. It saves neither raw audio nor private Run
transcripts, and opens no additional model session.

Huddle read-only context is owned by `internal/conversation/huddle_ai_tools.go`.
The shared wire projections live in `internal/agent/voice/context.go`; Company
and Task provide transaction-participant readers in their `voice_context.go`
files. `cmd/server/huddle_context.go` wires them without importing Task into
Conversation. Queries are in `pkg/db/queries/voice_context.sql`.
New invitations default to shared read-only context with an explicit opt-out.
Only a moderator who is in the call may share it (`HuddleChatParticipantActive`
in `huddle_ai.sql`: connected, and an active member of the Huddle chat); the
grant (`HuddleContextGrantActive`) ends when that person leaves the call.
`huddle_ai_usage.go` records bounded session stop reasons alongside accounting;
`huddle_ai.go` projects the latest current-generation reason on unavailable status.
`conversation/huddle_proposals.go` owns durable shared work drafts, replay hashes
and moderator edits/rejection. `task/huddle_proposals.go` owns human-confirmed
submission using the canonical Task creation/start helpers and Conversation's
transaction participants. Task, Run and immutable receipt commit together on
the confirming human's binding. Drafts survive Agent departure until call end or
expiry. `conversation/huddle_ai_leave.go` lets an attempt withdraw only its own
invitation without ending the human call. Queries live in `pkg/db/queries/huddle_proposals.sql`.
`conversation/huddle_work.go` projects receipt-bound progress/results and offers
bounded proactive notices on authorized heartbeats. PostgreSQL checkpoints one
speech attempt per proposal/Run/status before generation; recovery never replays
that attempt. These receipts are not proof of audible delivery. No result text,
question body, or approval details are included in a proactive notice.

## Chat model settings

`conversation/model_settings.go` owns direct/Task chat model and thinking choices,
revision checks and creator/Task-assignee access. Migration 91 adds
`conversation_model_settings`, keyed by Conversation and Agent so handoffs do
not reuse another Agent's provider settings. Direct creation and the shared
`execution.EnqueueRunInTx` path initialize from Agent defaults. The Run brief
query overlays saved chat settings, including explicit nulls, before freezing
`runs.context`; retry context remains immutable. The HTTP GET/PUT resource and
chat composer use the existing runtime model discovery and realtime invalidation.
`conversation_model_changes` preserves actual model transitions in the same
transaction, anchored after the latest Conversation item. GET/PUT return that
history for timeline notices; it never enters a Run's provider transcript.

## Collaborative documents and Project canvas

`canvas/output_review.go` presents Task and standalone Outputs through the same
Artifact/version/thread contract as Project review. Output-backed live documents
use this review boundary even when opened from the Project canvas. All discussions use
`review_threads`, `review_comments`, and their revision/event tables. Threads
name either a Project artifact version or an Output, with an optional live
page or immutable document revision. `output_review_operations` owns replay
receipts for the Output mutation boundary; comments never start Runs.

The `internal/canvas` package has five independent service owners: `BoardService`
and `BoardMediaService` (Company Whiteboards, above) and the three below. `CanvasService`
(`service.go`) owns scene arrangement, libraries, artifact review and discovery.
`PageService` (`page_service.go`) owns authoring, metadata, Overview selection,
contributions, document synchronization and Page discussions. `PresentationService`
(`presentation_service.go`) owns capture previews, immutable presentations, exports,
retained source reads and Run preparation. Handlers receive each service directly;
shared access checks and transaction participants remain package functions.

`internal/wiki/blocks.go` owns schema-version-1 native document nodes, Markdown
import/export, Yjs XML encoding/decoding, state-vector deltas, bounded validation
and structural-change detection. `block_draft.go` performs the one-time, page-locked upgrade
from Markdown `Y.Text`, preserving original bytes and text. PostgreSQL allocates
block identities in `wiki_block_ids`; `wiki_canvas_drafts` holds the canonical
collaboration snapshot and derived JSON/Markdown projections. Wiki revisions
freeze block JSON alongside their derived Markdown body. Migration 000089
adds shared draft metadata and records which Wiki revisions were published.

The Wiki and Canvas entry points share the block editor and merge machinery.
Company Wiki edits use only the Wiki document routes; Canvas references expose
published Wiki content and open that same Wiki draft for authorized editing.
The Canvas publishing command and the whole-Markdown Wiki update path are removed.

`internal/canvas/document.go` authorizes Project collaboration, reserves block-ID pools,
merges incremental updates against saved state with optimistic retries and fences
stale splits/joins/moves/deletions and XML replacements. Authorized unchanged
reads load head metadata only; addressed previews read projections without CRDT
bytes. `page_comment_anchors.go` follows working-Page quote anchors by surviving
CRDT character identities during saved edits, updating partial quotes and deleting
threads whose selected text was fully removed in the same transaction. Saved-version
review threads remain immutable. Post-commit `canvas.document_changed` hints drive
peer/preview and Page-comment refresh.
Artifact approval is retired: saved Outputs and Canvas versions require no separate decision.
Task review remains the only work approval; comments, version history and publication remain independent.
Alternative versions have a branch title and an independent Wiki page/draft.
`library.go` owns shared pinned-layout templates, creator-private saved views,
and restoration of removed placements. `search.go` returns authorized document
matches and block locations. Queries live in `canvas.sql`, `canvas_search.sql`
and `wiki_blocks.sql`; migration 000089 is additive and refuses destructive
rollback once native documents or saved library data exist. The same migration
adds the latest-structural-sequence fence without rewriting content.

`output_document.go` imports supported Markdown once per Output item into
`output_documents`. Original uploaded Asset Versions remain immutable. The page
belongs to the Output, independently of Project placements; the shared
`document.go` engine resolves either entry point through `document_target.go`.
Both use the same CRDT merge, block identity allocation, recovery contract,
comment reconciliation and saved Wiki revisions. Output revisions can be
exported as Markdown without replacing the original file. Collaboration hints
carry transaction-captured readers, and cursor rooms use the canonical page ID.
`auth/output_access.go` fences Company membership and Output grants. Standalone
Output documents require an explicit Editor/Manager grant or ownership to write;
visibility and conversation sharing grant reading and commenting only. A
Project-owned Output instead requires its source Project's read/write rights.
Project placement alone never grants editing. Sharing remains Manager-only.
Fresh-schema migration 000096 creates the canonical ownership constraints and
consolidates discussion storage without backfilling historical databases. See
ADR 0019 for the content, placement and permission boundaries.

`handler/canvas.go` exposes these human-only endpoints. Document cursor presence
uses page-scoped ephemeral rooms in `realtime/canvas_presence.go`, with server
resolved member identity. Content writes remain in PostgreSQL, not Redis or a
browser ledger. The browser uses IndexedDB only for disposable accepted snapshots
and best-effort pending-draft recovery; cached permissions grant no authority.
These ordinary document/canvas endpoints have no Agent execution admission.
See [the Pages and Canvas contract](../docs/design/pages-and-canvas.md)
for limits, transport semantics and verification.

### Project Overview and Presentations

`internal/canvas/overview.go` owns designation of one native Overview Page,
source freshness and creation of the small
Overview during ordinary Project/Company Plan creation. The association remains
`project_canvases.overview_page_id`; it points to a stable Project Page identity.
All Page editing uses `pageId`; immutable version IDs identify evidence.
`wiki_pages.project_id` separates Project work from published Company Wiki knowledge
while retaining the same native document storage.

`page_agent.go` admits running Task Runs with the exact current
assignee, execution generation and Project membership, plus document-thread Runs
restricted to their originating Page. Every Task can contribute
within its own Project; there is no additional per-Task grant. It owns bounded reads,
semantic block suggestions, dependency groups, comparisons and targeted
apply/reject/undo. Page-local feeds always
include pending proposals and page through completed history. `wiki/block_operations.go`
edits Yjs XML nodes without replacing the whole document. Human edits advance
shared content; overlapping suggestions require refreshed review. Protected
headings cover their following sections.
Ordinary native Project Pages use the same editing and review rules. A campaign,
research report or workflow guide is a composed Page, not a separate entity type.

Suggestion attribution freezes `project_page_changes.actor_agent_id` when the
proposal is written and survives Run deletion. Suggestion review receipts retain
the before/result captures and separate the proposing Agent from the deciding
member. Delivery previews expose
their immutable required suggestion indexes, so later rejection/resolution does
not silently reduce the work a selected Task result must have applied.

`presentation.go` captures Project Pages, retained native source documents and
timestamped Task/Workflow projections for immutable submitted evidence. Human
capture uses Project editing authority; Company Wiki publication remains separate.
After Task Run admission, capture checkpoints the authorized native Pages through
the same `wiki.CheckpointCanvasPageInTx` used by human editing. The existing Workflow catalog is supplied as
a read-only lookup from the composition root; it gains no table or execution
graph. Queries live in `overview.sql`; migration `000089_project_canvas` adds
managed blocks, change receipts, captures and exact source pins, and
refuses destructive rollback after authored data exists.
Contribution storage is shared by the Overview and supporting Pages:
`project_page_changes` and `project_page_managed_blocks`. The artifact's
`source_run_id` retains initial Task Run attribution across Page checkpoints.

The product calls submitted results **Artifacts**; existing `outputs` and
`present_output` remain their implementation/API names. The Run-scoped
`present_output` MCP tool relays structured input through the daemon Run
channel; the daemon snapshots files and registers local app previews. Its
MCP allowlist and daemon admission both enforce `saved-work`. It replaces the
CLI presentation command; file paths are relative to the Run workspace root. `present_output` with its `page` input uses
the exact reviewed capture to prepare a canonical attachment, then calls the
ordinary Run output endpoint with a bound `presentationId`.
`execution/deliverable_service.go` verifies that binding and retains the existing
Task Deliverable promotion/review lifecycle. Result projections include a native
Presentation destination. Capturing alone does not submit, approve or publish.
`task/result_selection.go` retains explicit ordered result sets.
`task/result_finalization.go` lets the exact live Task Run select its own deliveries
with a Task revision and Run-bound replay receipt. Terminal completion rechecks
that this Run finalized readable results; optional review still cannot bypass
required suggestion application, and never creates a human approval. Only an
explicit, authorized done request records result approval; ordinary edits of
completed Tasks do not. Assignment fixes
proposal-only and optional-review policy before execution. Completion requires
selected results and application of required suggestions; exact Page decisions
and Task approval can commit together. `page_suggestion_reviews` retains the
before/result revisions and themes for partial acceptance and undo.
`output_documents` maps each submitted item to the same live page with its submitted
sequence. Outputs retain their original Run and authorizing member as attribution
(`source_run_id`, `on_behalf_of_user_id`) independently of the nullable live Run
relation. Run completion records `source_run_completed` in the same transaction;
retained completed deliveries remain selectable after Run deletion, while deleting
failed or cancelled execution never grants result eligibility. Migration 000104
backfills these facts and frozen scope from existing Runs.
Human output resubmission remains removed. Automatic history in
`canvas/document_history.go` resolves current document access at both Project and
Output entry points. `wiki/history.go` records accepted saves transactionally in
`document_changes` (migration 000105), with CRDT deltas and bounded checkpoints.
Restoration appends a new shared-document state, reconciles comments and suggestions,
and publishes after commit; it never changes deliveries or Task result approval.
Suggestion history links review receipts to their retained before/result captures.
Existing manual checkpoints and human presentation capture/list routes remain
available; ordinary document controls use automatic history.

`canvas/document_agents.go` connects explicit Agent mentions on native Project
Page comment threads to `conversation/document_agents.go` in the same transaction.
Migration 000106 links one creator-private direct Conversation per thread and Agent;
these Conversations are hidden from the ordinary chat list. Follow-ups reuse the
existing Run queue without creating Tasks. `/` writing actions use this same path.
Tagged comment Runs receive `page_read`, `page_edit` and `source_read`, so they can
carry out document commands as reviewable proposals. Document Runs share the
restricted tool policy used by scheduled Runs. For Codex,
approved MCP namespaces are exposed directly: newer models can defer tools to
code-mode discovery even with `code_mode` disabled, while this policy disables
the code-mode host. Startup and thread restrictions share the feature settings
in `pkg/agent/connector.go`. Explicit writing actions
freeze `documentWriting` in the execution profile, receive no tools, and
capture the text block, UTF-16 selection range and canonical content in
`document_writing_requests` (migration 000106). Completion converts
the response to native block suggestions with the original block hash. Human review
alone applies these to CRDT state; intervening edits require refreshed review.
Both writing completion and `page_edit` persist proposals through `insertPageProposal`,
keeping input validation in their respective paths. Removing a quoted passage
converts its discussion to a Page-level anchor; document edits never delete the
thread, its Agent Conversations or their Run evidence.
Retries reuse the captured request and suggestion receipt, while later comment commands use
the page tools rather than replaying the captured replacement. Enqueue, claim, queue promotion and
reply publication recheck Project access.
The daemon's shared `runToolPolicy` uses the existing restricted provider mode for
document Runs. They start fresh in isolated scratch without external apps, local
files, skills or provider-session reuse. Writing Runs have no Run channel; comment
Runs expose only the document MCP routes, with no ordinary `zen` commands.

Source pins retain exact versions/revisions/assets and canonical export files.
Native submitted-result access is the intersection with included source access
in `output_access`; attachment reads and exports obey the same current authority.
Denied previews redact evidence without changing historical capture bytes.
Private artifact file routes optionally accept `X-Boardly-Artifact-Cache:
revalidate` and an attachment ETag in `If-None-Match`. They resolve current
artifact access first; `attachment.DownloadResolved` then returns `NotModified`
only for a matching ready attachment revision, without opening object storage.
The handler sends 304 and `private, no-cache` for that case; ordinary web and
public downloads retain `private, no-store`. The desktop byte cache is
disposable and never supplies authorization or offline access.
Artifact owners/managers may share any saved result publicly, including native
aggregate captures. Anonymous viewers receive its canonical export files; no
Company policy or approval is required.
`handler/project_page.go` and `mcp/page_tools.go` expose the human and
Run-proven paths, documented in `docs/api/spec/pages.yaml`. `project_read` discovers
Tasks, Pages, results and review needs; `page_read` reads the chosen Page.
The daemon allows Project Page tools only for Task Runs and instructs Agents to preserve
human content, inspect exact sources, create readable result Pages and link them
from the Overview. Submission still uses normal result review.

See [the Pages and Canvas contract](../docs/design/pages-and-canvas.md) for navigation,
vocabulary, capture limits and owner UX acceptance.

## Calendar and Social connections

`internal/calendar` owns personal and Company calendars, shared event series,
member invitations/RSVP, date-keyed exceptions, per-user completion and Run tool receipts.
Shared event mutations write invitations, RSVP and update/cancellation notifications
to Inbox in the same transaction; HTTP and Agent tools use the same path. Its bounded
reminder worker writes durable Inbox items and advances indexed due times atomically.
Inbox supports account-owned personal reminders alongside Company-scoped items.
Migration `000030_calendar` includes reminder delivery and Calendar update Inbox types.
`internal/social` owns per-user social accounts and browser-bound
OAuth flows, credential maintenance and idempotent immediate publication;
`internal/infra/social` implements external OAuth, refresh, discovery and publishing.
Media preparation precedes the domain’s final Run/membership/lease check, then the
prepared request crosses the public publishing boundary.
These are separate domains, not aspects of Company activity or Agent Schedules,
which is the forcing function for the two new domain packages. Their services use
`db.Store` and sqlc (`calendar.sql`, `social.sql`); handlers live in `calendar.go`
and `social.go`; `calendar_agent.go` and `social_agent.go` expose the Run-proven
transports. Migrations `000030_calendar` and `000034_social` add their tables.
`calendar.manage` permits Company event management only; personal event visibility
is organizer/invitation-based. Direct-chat Agents use explicitly allowlisted
`calendar_read`/`calendar_write` MCP adapters, sharing the same domain mutations.
Personal content is never published to Company activity rooms.
Direct-chat Agents can also use `social_read`/`social_publish` with user-owned
accounts. `ask_human` stores an authoritative `socialPost` preview in the existing
Question prompts; only its owner's explicit publish answer authorizes
`social_publish(questionId)`. Publication loads the approved snapshot and checks
the live Run, chat, Agent, connection revision and media access. The Question ID
keys the existing receipt across Runs; neither a retry nor new caption parameters
can bypass review. Settings → Plugins hosts the personal account connections.
Credential maintenance uses the existing server lifecycle and DB leases.
No new queue, Temporal deployment or scheduled publishing path is added.
See [scope and setup](../docs/specs/calendar/TECH.md).
Calendar mutations emit opaque `calendar.changed` hints through their existing
post-commit event list. Schedule definitions and worker outcomes emit creator-
scoped `schedule.changed`; Wiki evidence emits `wiki.run_reads_changed` with
Run-read visibility. Publication state and Studio attachment mutations emit
`artifact.changed` to authorized artifact readers. Routing remains in
`cmd/server/realtime_routing.go`; these transport hints are not Activity entries.


## Baseline architecture

These are the current ownership and persistence rules after the clean-slate
schema and payload cleanup.

- The schema begins with eight domain-grouped baseline migrations, `000001`
  through `000008`, followed by the checked-in incremental migrations in
  `migrations/`. Server startup applies pending migrations; additive feature
  changes preserve existing databases. The migration runner and schema tests
  are authoritative.
- `internal/conversation/pins.go` owns personal pin/archive state for task and
  direct chats. Task filing lives in `conversation_user_states` with independent
  revisions; direct filing remains creator-private on `conversations`. Archive
  preserves Task/Run state and message history. Saved state and optional
  `include_archived` direct listing support browsing and restoring archived chats.
- `internal/conversation/human_direct*.go`, `channel.go` and `discussion_*.go`
  own human DMs (people and Agents, deduplicated until expanded by invitation) and private channels.
  Pair DMs retain their pair identity; group DM identity uses sorted human and Agent participant
  keys backed by the same membership FK edges and private grants. Active participants can
  invite more people or Agents into group DMs. Expanded groups leave canonical creation
  lookup and keep independent history; pair identities remain immutable. `general_channel.go` owns the default
  Company-wide General; Company creation and joins grant human membership in
  the same transaction, while Agents require explicit addition. ConversationService owns creation and
  membership; DiscussionService owns shared message text, bounded history/recovery,
  state and personal pin/hide. `discussion_collaboration*.go` adds single-level
  threads, author edits, permission-checked tombstones, shared message pins,
  canonical emoji reactions and grant-bound follow state.
  `discussion_collaboration.sql` owns their scoped reads/writes. `discussions.sql` supplies current grant/role
  projections and transaction queries. Owner channel access does not require
  joining; admin management metadata does not grant content access. Neither
  permission overrides DM privacy. Agent relations retain history authority
  without exposing an Agent history tool or admitting execution.
- Discussion writes lock Company, acting membership, sorted affected memberships,
  Agents, then Conversation. Company/account/Agent lifecycle owners call the
  exported discussion revocation functions in their own transaction. Internal
  Activity evidence has NULL visibility and never publishes a public Activity
  event. Message cursors carry current Company membership revision and nullable
  grant generation. `cmd/server/discussion_realtime.go` coalesces bodyless hints
  and resolves current Company-user recipients before the bounded Redis batch;
  revoked-user delivery has its own bounded lane. Transient delivery failures retry
  with backoff; queue overflow emits an opaque Company recovery hint. Channel
  Agent status and activity hints resolve the parent discussion's current grants
  without exposing execution payloads. Legacy realtime routing stays
  separate. Human DMs and private channels are available to every Company;
  current membership, role permissions and conversation grants govern access.
- `internal/conversation/channel_invocation*.go` owns channel and group DM structured Agent-mention
  admission, FIFO invocations per discussion/Agent, final-answer publication and
  the activity surface, where each member reads only the Runs they started
  (Access TECH B11). Each pair uses one private `channel_agent`
  execution Conversation. `internal/execution/channel_invocation.go` enforces
  frozen membership/grant generations and builds bounded discussion context plus
  authorized attachments. Human-input continuations retain invocation identity
  and queue ownership; public replies are immutable `agent_text` thread items
  with reactions/pins. Runtime transcripts are never broadcast to the shared discussion.
- `internal/company/project.go` owns Project identity, reads scoped by the
  caller's Project access (ADR 0017), settings, visibility and archive;
  `project_grants.go` owns grants, Join, visibility changes, the
  `project_grant` Activity rows, `project.access_changed`, the Run sweep on
  lost access and the access report (`GET /projects/access-report`);
  `project_files.go` owns atomic logo/document registration (`PUT
  /projects/{id}/files`) and retained attachment associations.
  `groups.go` owns Groups (Access TECH D3, `GroupService`): a named, flat set
  of Team members that holds Project grants; create, rename, delete, the
  batch members change and its preview, each moving access like a person
  grant change (Group then Projects locked, everyone resolved before and
  after in one read each through `auth.ResolvePeopleProjectAccess`,
  revision, one `project_grant.group_*` row and one `project_access_granted`
  Inbox item per manager per Project, the Run sweep), and a departing
  member's Group memberships (D5).
  `project_group_grants.go` owns a Group's grant on a Project (D4).
  `projects.sql` supplies the access-scoped reads and the writes;
  `project_access.sql` the grant rows, the Group grant rows, every person's
  stored paths and the access report; `groups.sql` the Groups and their
  members. Migration
  000086 also cancels, once, the Runs and re-addresses the decisions its
  owner backfill left without access.
  Task Runs freeze Project documents; the daemon places originals and bounded text
  extracts in Run scratch. PDF extraction uses host `pdftotext`; failures are named
  in the Agent prompt, with original files still available. Task owns
  immutable `projectId`, execution admission and Task history. Project events
  use the `project` topic and the existing Activity evidence sink.
- `internal/auth/profile.go` owns revision-guarded personal profile edits. Optional
  `avatarDataUrl` accepts a PNG/JPEG thumbnail validated by `profile_avatar.go`
  (at most 256 × 256 pixels and 96 KiB). It updates the existing `users.avatar_url`
  and `user.updated` projection atomically; omission preserves an OAuth picture
  or the prior thumbnail. Personal avatars are independent of Company uploads.
- `internal/company` owns the company-statistics endpoint, including its query
  and response assembly. `profile.go` owns revision-guarded avatar selection
  from the caller's ready image uploads. `companies.avatar_attachment_id`
  pins the selected attachment and grants active members download access;
  replacement/removal releases both through the existing attachment lifecycle.
  The two onboarding stamps a Company carries —
  `agent_setup_completed_at` and `first_plan_applied_at` — are columns on
  `companies`, written once by the domain whose event they record and taking
  no part in the Company revision, so a background stamp never conflicts with
  a founder profile edit. The retired ten-step Starter Journey that once held
  them, and the onboarding intent beside it, are gone: no client ever rendered
  the intent picker. Company responses carry no access-version
  field or refresh accelerator; `company_members.version` exists only for
  optimistic concurrency on membership mutations.
- Andrew's Company-scoped Run researches the founder's website and any
  reference files they attached, then turns that plus the initial description
  and focused Question answers into `assets/general/business_context.md`.
  Founder-supplied reference documents are Company-owned: registering an
  upload through `company.ReferenceService` holds it out of the abandoned-
  attachment sweep, freezes it into every Run brief as `companyReferences`, and
  the daemon places it in the Run's scratch env root for the root Agent's
  direct Runs only — the ones that write the business context. It is never
  committed to the Company repository: a document handed over to explain the
  business is input to the shared context, not Company content, and committing
  it would keep it in git forever and copy it into every worktree. The prompt
  names the scratch paths and subordinates them to the confirmed context. It is a
  normal workspace Asset, so the conversation's generated-file card and the
  Assets browser use the same Markdown file. Direct Runs work in the Company
  main, where the Asset is simply a file; every Task Run receives that Asset
  directory and the Company Memory mounted read-only, because a worktree
  only carries what the main had committed. There is no separate brief
  draft, approval record, or temporary business-profile projection. Andrew
  pauses on a normal Question so the founder can confirm or correct the
  generated Asset before Company Plan work begins; Company Plan review remains
  the explicit gate before planned Agent work starts. For Runs with Company
  Plan tools, the daemon's Company data boundary explicitly authorizes the
  current Run's `company-plan/` scratch directory for draft/export reads and
  writes. The planning prompt and MCP use that same path outside the workspace;
  this does not authorize parent directories or other Runs' scratch.
- `internal/attachment` depends on one `ObjectStorage` port. Composition wires
  an S3-compatible adapter from `internal/infra/storage`.
  `STORAGE_DRIVER=local` selects the bundled Silo development defaults;
  `s3` (the default) requires explicit configuration. There is no filesystem
  object-storage backend, storage-authority row/binding generation, or
  client-supplied/persisted attachment checksum in the product model.
- `run_usage` is the immutable, FK-free usage-fact authority. Each fact
  carries `usage_basis` (`per_run`, or `session_cumulative` where the provider
  reports whole-session totals — the codex path). Reporting joins those facts
  to retained canonical Run rows and aggregates them directly; there are no
  hourly rollup/dedup tables, usage backfill/reporting state, or
  rollup/backfill workers in the server composition root. Deleting a Task
  detaches its task-kind Conversation instead of deleting the Conversation,
  Messages, or Runs, so historical usage remains reportable while live Task
  lists omit the deleted row.
- The durable Conversation Run queue remains. A terminal Run transition is the
  promotion trigger for its Conversation's next eligible queue item; promotion
  participates in the transition-owned transaction, while events and runtime
  wakeups remain post-commit. There is no polling queue-promotion worker.
- `internal/approval` owns the separate ApprovalService and QuestionService
  lifecycles plus their expiry/continuation reconciler. It is a distinct package
  because Company Plan approval would otherwise form a real import cycle:
  approval resolution invokes a company-owned transactional action participant,
  while company plan application raises the approval. Proactive Mode may
  execute an exact root-Agent direct-conversation Company Plan approval inline;
  every other request remains pending for a person and uses the continuation
  lifecycle. The composition root wakes its supervised reconciler on committed
  Approval and Question resolutions; a 30-second timer remains for expiry,
  retries, and recovery from missed events.
- `internal/agent` owns the Agent and Runtime lifecycles plus the Connector and
  Plugin catalogues and revisioned per-Agent capability denials. Runtime tool
  reports persist observation freshness but emit `daemon.roster_changed` with
  `tools_changed` only when tool state changes; clients refresh runtime reads
  without invalidating Agent, Run or conversation caches. The enqueue
  kernel freezes denials into Run context; daemon preparation narrows inherited
  availability without granting Connector policy. Daemons publish sanitized replacement discovery snapshots
  for an exact Runtime; PostgreSQL owns the latest projection and Connector
  policy. Only a reviewed Connector contract grants execution authority. An
  exact Run selection freezes atomically in full or is cancelled before
  dispatch. A Plugin is an install/presentation record and grants none.
  Freshness is `protocol.ConnectorDiscoveryFreshWindow` (ten minutes) on
  `connector_discovery_generations.observed_at`; the daemon's reaffirm
  (`ReaffirmDiscovery`) renews that one row for an unchanged generation.
  Changed catalogs and freshness recovery publish `runtime.capabilities_changed`
  post-commit to the Runtime registrant; unchanged fresh keepalives do not.
  Shared policy changes from review, publication or reaffirmation emit the
  opaque Company-wide `connector.policy_changed` hint; private Runtime details
  remain in the registrant's capability event.
  `connector_app.go` holds the exact app pins and `ReadAppReadiness`, which
  restates the claim freeze's rule per app for one Agent as the viewer's
  binding runs it (ADR 0014; Workflow app readiness); an integration test
  keeps the two in lockstep.
  `connector_logo.go` chooses the logos the Connector, Plugin and readiness
  reads return: a `chatgpt.com` logo loads only in a ChatGPT session, so a
  loadable stand-in wins when there is one (D26).
- `internal/schedule` owns creator-private one-shot/cron definitions, exact
  reviewed Connector sources, replay-safe firing receipts, creator-private
  attention, the Asynq scan/fire handlers, and the independent repair sweep.
  `cmd/server/schedule_beat.go` composes the shared-client queue lifecycle;
  PostgreSQL alone owns firing truth and retry lineage.
  Each Schedule owns one `schedule` Conversation whose Runs use a
  capacity-exempt non-task lane. The Schedule transaction calls narrow
  transaction participants in `conversation` and `execution` to materialize,
  supersede, or invalidate Runs; calendar policy never enters the run lifecycle
  kernel.
- `internal/mailbox` owns one Company Email identity, retained inbound metadata,
  provider-backed detail reads, and retention. The same package owns Run-bound
  Agent Mail draft groups, optimistic review, recipient quota, and a leased
  send worker. Every Mailbox and Agent Mail route requires `mailbox.manage`,
  including Runs acting for a member. The permission catalog currently grants
  it only to owners; Mailbox consumers use `Can` and `RolesWith`, so granting
  another role access needs no Mailbox-specific role checks. Reads, mutation
  fences, realtime audiences and Activity history enforce the permission alongside
  chat/Project access. It stays separate from `internal/inbox`: email is user content,
  while Inbox items are Boardly notifications.
- `internal/task` owns the closed milestone and execution classifications,
  Task execution generations, Agent-reported business failure, and durable
  upward done/failed outcome reports. `run_scope.go` owns what a Run may read
  (`RunScope`): only the Tasks its Agent does for the member it works for,
  its authorizer, read with that member's session and Project access, and
  only in Projects the delegate rule admits (`run_permission_held` through
  `ListRunReadableProjects`, D31): its authorizer reads there, and a Task
  Run only in its Task chat's Project (ADR 0017). No chat audience narrows a
  tagged Run: the person who tagged the Agent answers for it. The one
  exception is `RunScope.Planner`: the root Agent's Run in its person's
  direct chat, with its Company Plan tools on, reads every Task that person
  reads, still within the delegate rule; the handler
  decides it from the proof (`handler.runIsPlanner`, IA-FR12). Any other
  Task read carrying a Run proof is the plain `ErrOutsideRunScope`, which carries
  nothing of the Task. The handler admits a proof only on its authorizer's
  session (`handler.runProofBearer`). A person-confirmed or exact Proactive
  Mode report batch becomes one Message and Run in the recipient Agent's direct
  Conversation, with distinct durable provenance; it carries only reports
  about Projects that member reads, and the rest stay queued (ADR 0017). The
  package also owns leased, generation-fenced Task merge jobs claimed only by
  the completed Run's daemon.
  A job records whether authority came from Proactive Mode or an explicit
  person review; desktop acceptance uses the same durable claim, lease, and
  atomic Task-result fence as automated merging.
  Explicit standalone scheduled-Task requests in CEO chats use `zen task create`
  without Company Context review. The daemon's frozen channel admits that write
  only with a one-shot or cron schedule; immediate CEO-proposed work still uses
  the reviewed Company Plan. The ordinary Task service validates the schedule,
  member assignment and Project access, and defers execution until it is due.
  `failed` and `error` are server-owned states; ordinary Task mutations cannot
  select them. The package owns Task-message admission and the generation/
  assignment/status-fenced projection from terminal Runs back into Task state.
  Completed Runs with pending human-input requests or continuations leave the
  Task `in_progress`; other completed Runs move it to `in_review`.
  Human-performed Tasks keep their human `executedBy` and can explicitly start
  or message a Task Run with the creating Agent (CEO when a member created it).
  The `task_run_agents` view owns that resolution across admission, retries,
  continuations and terminal effects; `execution.agent` exposes it to clients.
  Its Run can read that exact human Task, but no other human Tasks. The frozen
  `humanWork` brief tells the daemon to gather and record the member's decisions.
  Human Tasks require manual completion; proactive merge never closes them.
  A new user Message explicitly resumes a `failed` Task through a fresh Run
  and supersedes its still-pending upward failure report.
- `internal/execution` advances the execution generation in every fresh Task
  Run insert. An automatic retry copies its parent's generation and is created
  only while the Task is still `in_progress`, assigned to the same Agent, on
  that generation, with every prerequisite done. A created retry deliberately
  leaves the Task `in_progress`; only a terminal chain applies the Task-owned
  outcome projection.
- `internal/execution` also owns Run Artifacts and Deliverables: Run-token-bound
  presentation, current Artifact access checks (owner, people, Conversation,
  Company, or explicit public link), Company-controlled public final-file downloads,
  image/audio/video media, immutable Attachment-to-Asset-Version promotion,
  completed Task Run promotion,
  founder review, result downloads, and the `deliverable.*` event family. These
  remain part of the Run lifecycle rather than a second job or file-management
  domain. Artifact access changes use revision-guarded writes; access history and ownership transfer are unavailable. Runnable app
  publication and local preview remain disabled pending an authorized gateway.
  Its Task results read model aggregates immutable Outputs, review/publication
  state, and user-message Attachment sources independently of chat pagination.
  Both result and source feeds use bounded keyset pagination under one
  membership-checked snapshot; item metadata identifies the immutable Asset Version.
  `company_artifacts.go` lists every Artifact the caller can open and resolves
  a set of ids for Wiki citation chips; both filter through `output_access`.
  `artifact_run.go` resolves immutable Output items for live consuming Runs,
  requiring current access for the Run's Authorizer alone: every Run, a Task
  Run's included, reads exactly what its Authorizer reads, and no chat
  audience narrows it (D31). It logs each refusal's reason with the Run and
  Artifact ids.
  The MCP `artifact_inspect` and `artifact_fetch` tools use the daemon Run
  channel; `daemon/run_artifacts.go` downloads only requested items into
  `.zenith/artifacts/<runId>/` under the workspace, reauthorizes cache hits,
  and verifies cached bytes against an in-memory SHA-256 digest.
- Public concurrency fields use `revision` and `expectedRevision`; row-birth
  timestamps use `createdAt`. Lifecycle clocks keep their semantic names such
  as `queuedAt`, `completedAt`, and `deletedAt`. PostgreSQL/sqlc keep
  `version`, `expected_version`, and snake_case timestamps; those storage
  names do not leak onto the wire.

## Entry points

| Binary | Ships as |
| --- | --- |
| `server` (`cmd/server`) | the API container — the only binary `server/Dockerfile` builds. `main.go` is a thin shell; the composition root is `run()` in `cmd/server/app.go`, which applies migrations, builds every service, hub, relay and worker, and serves HTTP. |
| `zen` (`cmd/zen`) | the Zenith Team CLI: auth, daemon lifecycle, config, doctor and the task/run/conversation/question/approval/inbox/schedule/agent/runtime families; Agent mode over the daemon's Run channel (ADR 0009). Links `internal/daemon`, never a server service. |
| `boardly-mcp` (`cmd/mcp`) | the MCP stdio server shipped in the desktop bundle. The daemon starts it for a Run with the Run's isolated `BOARDLY_HOME`; it reaches the server only over the Run channel that home's `run.json` names, and refuses to start without one. The binary name is pinned by every build site's `-o` flag. |
| `migrate` (`cmd/migrate`) | operator tool, not deployed: applies or rolls back the embedded migration sequence without booting the API, subject to each migration's retention guards. |
| `bundlectl` (`cmd/bundlectl`) | maintainer and CI tool, never shipped: pins a boardly-plugins release tag into `internal/platformbundle` for Workflows or `internal/daemon/tool_releases` for managed Tools (`import`), re-imports every locked tag to prove the pin (`import --verify`), pins a local checkout for development (`import --dev`), checks plugin folders with the server's own parser and loader (`check`) and compares the pin with a git revision, holding each change to the release rules (`diff`), and resolves public npm skills into ignored build assets (`prepare`). ADR 0016. |

## Layout

```text
server/
├── cmd/{server,zen,mcp,migrate,bundlectl}/   see cmd/README.md
├── pkg/                 shared contract layer — see pkg/README.md
│   ├── protocol/        flat event registry (<domain>.<verb>), Message envelope,
│   │                    daemon RPC frames, structured Run transcript payloads,
│   │                    activity entity/actor vocabulary, the provider list,
│   │                    wire value contracts
│   ├── db/              sqlc: queries/ (per-domain .sql) + generated/ (package
│   │                    db, google/uuid override plus pgtype nullable/time
│   │                    values); sqlc.yaml names both paths
│   ├── agent/           provider-neutral ordered execution contract:
│   │                    exec_unix.go owns POSIX process-group cleanup;
│   │                    exec_windows.go uses bounded taskkill tree termination
│   │                    and disables retries without confirmed tree cleanup.
│   │                    ItemDelta, ToolActivity, CompletedItem,
│   │                    SessionReady, Finished;
│   │                    Codex persists immutable tool-call items at start
│   │                    under codex-tool-call:<native id>, separate from
│   │                    the final result, so live clients can show targets;
│   │                    resumed Codex threads append the current Run brief
│   │                    as a developer message before starting the turn,
│   │                    because resume settings alone can leave stale
│   │                    instructions in the model's persisted history;
│   │                    agent.New(provider, cfg) is the one factory switch;
│   │                    claude_provider*.go /
│   │                    codex_provider*.go are sibling files, not subpackages
│   ├── runfailure/      canonical runs.failure_reason taxonomy
│   └── redact/          credential redaction preserving product email addresses;
│                        diagnostics additionally mask email addresses
├── internal/
│   ├── auth/ account/ company/ conversation/ inbox/ approval/
│   │   attachment/ usage/ telemetry/
│   │                    product domains: handler → service → sqlc queries.
│   │                    conversation/huddle_ai.go owns voice intent, leases,
│   │                    recovery, exact call-chat delivery and attempt receipts;
│   │                    infra/livekit owns named dispatch. The separately deployed
│   │                    Python voice-agent/ process never enters the CLI link set.
│   │                    usage/huddle_usage.go owns Company voice-budget
│   │                    configuration, UTC-month reservations, cumulative
│   │                    spending and settlement. Unknown final usage retains
│   │                    the reservation; reconnect never resets the allowance.
│   │                    auth/permission.go is the Company permission
│   │                    catalog (ADR 0012): the one table mapping roles to
│   │                    permissions, CompanyRole.Can / UserPrincipal.Can,
│   │                    PermissionsOf and RolesWith (the text[] shape a
│   │                    sqlc actor_roles parameter binds).
│   │                    auth/project_permission.go is the Project
│   │                    permission catalog (ADR 0017): ten names granted
│   │                    per person per Project, the Company default set
│   │                    and the management set. auth/project_access.go
│   │                    reads standing on a Project from the database
│   │                    (ResolveProjectAccess / ResolveMemberProjectAccess,
│   │                    Require* incl. RequireTaskChatPermission and
│   │                    RequireProjectContentPermission, LockProjectAccess,
│   │                    HoldsProjectPermission / RequireProjectPermissionHeld,
│   │                    TaskChatProjectID, ProjectAudienceInTx,
│   │                    TaskChatDecidersInTx, ProjectDecidersInTx); the
│   │                    rule lives in the project_access views and
│   │                    project_permission_held (migration 000086); what a
│   │                    delegate (a Run, the voice assistant) may do is
│   │                    delegate_permission_held and run_permission_held:
│   │                    its Authorizer's access, no chat audience
│   │                    (migrations 000099 and 000100, D31).
│   │                    auth/project_audience.go decides a realtime
│   │                    frame's Project audience in the producing
│   │                    transaction (ProjectFrame, ProjectAudienceCarrier,
│   │                    ProjectFramePayload, StampProjectAudiencesInTx,
│   │                    ProjectFrameAudienceInTx,
│   │                    ConversationFrameAudienceInTx)
│   ├── agent/            Agent templates (binding.go owns each member's
│   │                    placement of one on a runtime of their own,
│   │                    ADR 0014), private Runtime lifecycle, and the
│   │                    provider-neutral Connector policy/catalogue and
│   │                    Plugin presentation catalogue. Runtime lifecycle
│   │                    includes atomic daemon registration/deregistration
│   │                    behind a registrant fence, offline sweep/GC,
│   │                    per-registrant roster fanout, per-binder
│   │                    agent.status frames, and the runtime_offline Inbox
│   │                    notice the sweep raises for the registrant alone.
│   │                    Model discovery is transient (memory or Redis);
│   │                    sanitized capability discovery is Runtime-scoped
│   │                    structured state in PostgreSQL. connector_app.go
│   │                    pins each Workflow app hint to exact provider
│   │                    identities and answers per-Agent app readiness on
│   │                    the viewer's own binding of the Agent with the
│   │                    claim freeze's own rule, kept fresh by the
│   │                    daemon's reaffirmation
│   │   └── voice/       shared Voice settings and frozen Huddle profile;
│   │                    separate to avoid agent → conversation → agent cycles.
│   │                    No runtime or Connector execution authority.
│   ├── task/             Tasks, dependency, execution- and
│   │                    workspace-generation rules, milestone/, member
│   │                    assignees with confirmed handoffs, Message-Run
│   │                    admission, terminal Run projection, Agent business
│   │                    failure, the Run read scope (own Tasks only),
│   │                    and upward outcome report events/batches
│   │                    plus proactive merge jobs and automatic report
│   │                    dispatch. access.go holds the Project access
│   │                    decisions for Task writes (ADR 0017): write,
│   │                    execute, assignee-must-read, the write-statement
│   │                    fence; reads decide access in the statement via
│   │                    project_access
│   ├── schedule/         creator-private definitions and lifecycle,
│   │                    source review, firing receipts, Asynq handlers and repair;
│   │                    materializes capacity-exempt Runs through a
│   │                    private Schedule Conversation, never a Task
│   ├── workflow/        the release-owned Workflow catalog: parses and
│   │                    validates the zenith-workflows skill
│   │                    (workflow.yaml + playbook.md per Workflow,
│   │                    authored in boardly-plugins and read through
│   │                    platformbundle.WorkflowCatalog; CheckCatalog
│   │                    reports every problem for bundlectl) and serves
│   │                    List/Get to handler/workflow.go, which also
│   │                    serves GET …/workflows/:workflowKey/readiness by
│   │                    handing the app hints to agent.ReadAppReadiness
│   │                    (workflow/ and agent/ never import each other).
│   │                    No table, no writes, no service trio. Its own
│   │                    package because it is a separate domain noun
│   │                    (tools/ would otherwise host two domains) and it
│   │                    keeps the catalog parser out of the cmd/zen link
│   │                    set, which links internal/mcp/tools
│   ├── mailbox/          Company Email metadata/detail/retention and
│   │                    Run-bound Agent Mail drafts plus the send worker
│   ├── activity/        Company action history: owner-transaction snapshots
│   │                    for Task/Conversation/Run, revision-indexed reads and
│   │                    startup baselines; a SubscribeAll sink retains other
│   │                    events as version-0 evidence. Feed, calendar and
│   │                    announcements share frozen visibility; feed and
│   │                    calendar also follow Project access (ADR 0017).
│   │                    An Agent's row in a Run also names the person it
│   │                    acted for and the Run (events.RunActor, D31).
│   ├── execution/       the run lifecycle kernel: enqueue/trigger + fresh
│   │                    Task generation advance, fenced retry, claim +
│   │                    reviewed Connector snapshot freeze, terminal
│   │                    transitions + Conversation Run queue
│   │                    promotion, one-item transcript append + digest head,
│   │                    immutable Artifact presentation + Deliverable review,
│   │                    app and explicit file publication, durable worker,
│   │                    and custom subdomain routing,
│   │                    Schedule-run cancellation, EmptyClaimCache;
│   │                    the enqueue kernel and the claim need the
│   │                    authorizer's project.tasks.execute for a Task
│   │                    chat (ExecutePermissionError, ADR 0017);
│   │                    RunService also owns the enriched user-plane
│   │                    read/event projections
│   ├── events/          the in-process Bus: producers build an events.Event
│   │                    literal in the transaction and bus.Publish(ctx,
│   │                    topic, …) after commit; bus.Subscribe(topic, …) or
│   │                    SubscribeAll registers a consumer. See ADR 0006
│   ├── realtime/        receive-only client WS plane: identity-indexed hub,
│   │                    the Broadcaster's delivery methods, sharded
│   │                    Redis durable relay, metrics. Knows rooms, no
│   │                    domains — who sees what is in cmd/server
│   ├── daemonws/        daemon WS control plane: connection hub, heartbeat
│   │                    frames, RPC correlation, wakeup fanout
│   ├── handler/         the Gin HTTP surface: one file per URL resource;
│   │                    strict bindRequest + Gin binding tags + errorMapping;
│   │                    pgtype mappers in pgx.go; daemon plane in daemon.go
│   │                    (/api/v1/daemon/*)
│   ├── middleware/      every Gin middleware, as plain functions taking a
│   │                    logger, a config, or a domain service: request
│   │                    identity, client metadata, logging + recovery, HTTP
│   │                    metrics, CORS, CSRF, the Run proof policy
│   │                    (RunProofPolicy over handler/route_actor.go),
│   │                    credential and company guards
│   │                    (RequireCompanyMember, RequireCompanyPermission,
│   │                    RequireHumanActor; a daemon's own session passes
│   │                    only the daemon plane and a run/any route with a
│   │                    live Run proof of its person, resolved once and
│   │                    kept on the request, ResolvedRunOrigin), and the
│   │                    Project
│   │                    guards (RequireProjectVisible,
│   │                    RequireProjectPermission: 404 before 403). Refusals
│   │                    go out through util.WriteError
│   ├── db/              pgxpool, migration runner + advisory-lock gate,
│   │                    readiness, Store/WithinTx, schema integration tests
│   ├── daemon/          the local agent runtime: discovers the claude/codex
│   │                    CLIs and their Connector/Plugin capabilities,
│   │                    reads the server-owned Studio origin at startup
│   │                    (server_config.go), manages Studio setup and
│   │                    Company-and-Agent-local browser sign-in,
│   │                    sanitized Runtime tool observations, and
│   │                    opt-in UUID-bound Studio Skill snapshots
│   │                    (studio*.go, tool_*.go), with the Team entrypoint
│   │                    and runtime in studio_skill.md/studio_runtime.md,
│   │                    with media and video routes loaded on demand from
│   │                    studio_media.md/studio_video.md;
│   │                    tool_studio.cjs keeps Project/account guards around
│   │                    named-model filtering and team/jobs collect (wait/download/
│   │                    canvas); review, validation, commit and presentation
│   │                    remain Agent steps after collection; CLI 0.1.15 intent
│   │                    receipts can supply scoped collection IDs without parsing
│   │                    scripts; explicit IDs still collect previously submitted nodes;
│   │                    tool_releases holds one zenith-studio.lock.json from
│   │                    boardly-plugins (metadata and provenance, no archive); its ignored
│   │                    generated/ skills are downloaded and verified at build time,
│   │                    publishes sanitized replacement snapshots,
│   │                    coalesces provider-change refreshes (changed <
│   │                    reobserve < forced), keeps Connector discovery
│   │                    fresh with a keepalive that re-observes every
│   │                    BOARDLY_CONNECTOR_DISCOVERY_INTERVAL (3m) and
│   │                    reaffirms an unchanged generation instead of
│   │                    republishing it (connector_service.go), registers
│   │                    runtimes with their credentialIsolation report,
│   │                    starts every provider and app preview on macOS
│   │                    inside the Seatbelt profile execenv/seatbelt.go
│   │                    builds once each round's probe passed, which also
│   │                    keeps each Run out of other Runs' homes and lets
│   │                    it write only its own folders under the workspaces
│   │                    root (its env root, its workdir, its Task
│   │                    worktree's own folder in the Project main's
│   │                    repository, workspace.Acquired.GitDir), hides
│   │                    that root but its own folders, the repository
│   │                    cache and every bound folder from a Run tagged in
│   │                    a chat and from its app's preview
│   │                    (runHiddenFolders, SeatbeltOptions.Hidden,
│   │                    appPreviewManifest.Hidden),
│   │                    and reads a file for a Run (a presented output, a
│   │                    generated image) only when that profile would let
│   │                    the Run read it (runFileGuard,
│   │                    credential_isolation.go, Access TECH B3d, B3e,
│   │                    B3f, B7b),
│   │                    claims and executes Runs, renews exact
│   │                    Task-merge attempts, and serializes managed-main Git
│   │                    mutation across daemon processes,
│   │                    appends completed items through daemonws, serves the
│   │                    Desktop-only run preview plus capability-authenticated
│   │                    bounded workspace asset catalog/preview/export,
│   │                    local .memory reads, and Task review (manual
│   │                    acceptance reuses the managed-main mutation guard;
│   │                    review/merge patches reference binary blobs in the
│   │                    shared Git object database instead of embedding them), and
│   │                    resolves each Run's frozen Connector snapshot to
│   │                    local provider settings and names its apps in
│   │                    the Run's instructions (run_policy.go);
│   │                    exact-runtime model discovery rides heartbeat
│   │                    work; subpackages execenv/, workspace/, repocache/
│   ├── authfile/        the versioned local credential store (one file
│   │                    lock per file, atomic writes, MintDaemonSession,
│   │                    Logout): the zen CLI's auth.json and the daemon's
│   │                    own daemon-auth.json (its session and its output
│   │                    presenter, D31); the daemon never reads auth.json
│   │                    and boardly-mcp reads neither
│   ├── runchannel/      the Run channel wire (ADR 0009, Access TECH B3b):
│   │                    run.json (RunControlConfig, LoadRunControlConfig),
│   │                    the Request/Response documents and the Client that
│   │                    posts them to the daemon. A leaf (standard library
│   │                    and uuid) so zen, the daemon and the mcp-sealed
│   │                    boardly-mcp all link it
│   ├── mcp/             boardly-mcp composition: apiclient/ (RunChannel,
│   │                    the only transport boardly-mcp uses, plus the
│   │                    daemon presenter's credential Client), companyplan/,
│   │                    tools/ (Company Plan, Agent Mail, Task failure,
│   │                    Questions, Agent roster, execution skills, the
│   │                    compiled zenith-platform skill under
│   │                    tools/builtin/zenith-platform, and
│   │                    platform_skills.go, which serves it with the
│   │                    pinned bundles). Skill reads return instruction
│   │                    text once, plus structured verification metadata;
│   │                    the full reference inventory is opt-in via
│   │                    includeReferences on SKILL.md. Beyond its own tree
│   │                    imports only internal/runchannel, internal/authfile,
│   │                    internal/platformbundle, the asset-free
│   │                    internal/contentbundle verifier,
│   │                    internal/task/milestone and pkg/studiocanvas. It
│   │                    holds no credential: every call goes over the Run
│   │                    channel named by run.json
│   ├── contentbundle/   canonical archive/lock codec and managed Tool release
│   │                    contract, with no embedded assets or service imports.
│   │                    Separate for the cmd/zen link set: daemon verifies
│   │                    Studio without linking the Workflow bytes.
│   ├── platformbundle/  pinned Workflow content from boardly-plugins
│   │                    (ADR 0016): <plugin>.bundle.tar + <plugin>.lock.json
│   │                    per plugin component and harness/zenith-workflows/
│   │                    SKILL.md, all embedded and verified against the
│   │                    lock at load (see its README). The server refuses
│   │                    to start on a bad pin; boardly-mcp drops only that
│   │                    bundle. Its own package for the cmd/zen link set:
│   │                    the CLI links internal/mcp/tools and must never
│   │                    link the Workflow bytes. Imports internal/mcp/tools
│   │                    and contentbundle, so boardly-mcp stays sealed
│   ├── infra/           technology adapters: cloudrun/ (public application
│   │                    build/deploy), email/, redis/ (required shared client),
│   │                    storage/ (S3-compatible
│   │                    ObjectStorage and Cloudflare KV route projections)
│   ├── config/          env configuration, logger construction, secret
│   │                    redaction
│   ├── util/            boundary helpers: pgtype conversions for text, time
│   │                    and nullable ids; strict JSON body decoding and the
│   │                    validator's JSON field naming (json.go); UUID parsing
│   │                    (ParseGoUUID); the Task label parser the server
│   │                    and the zen CLI share (task_label.go,
│   │                    ParseTaskLabel); errors.go — the 422 detail renderer —
│   │                    and http.go, the request-id context accessors plus
│   │                    WriteError, the one JSON error envelope every package
│   │                    answers errors with
│   └── testutil/dbtest/ shared PostgreSQL test scaffolding
├── migrations/          historical baselines 000001–000008 and additive
│                        migrations through 000035; retained discussions and
│                        collaboration state block destructive rollback
└── tests/               testpostgres/ (one PostgreSQL container per test
                         command) and wsobserve/ (e2e smoke WS observer)
```

Zenith Studio canvas admission is owned by `internal/conversation/studio_editor.go` and the
`/studio/operations` handler. Team checks the signed-in human and runtime, creates or
reuses one Conversation binding through the daemon's local CLI bridge, and returns a
short-lived Studio bearer token. The browser then uses Studio REST for graph CRUD and
Socket.IO for collaboration. `daemonws/studio.go` and `daemon/studio_control.go` only
carry probe, token, binding verification, and binding creation frames. Credentials stay
daemon-local; Team persists only the binding and origin metadata.

With Redis configured, `daemonws/studio_relay.go` implements the admission relay
across API replicas. Each authenticated socket registers a unique process/socket
member under `daemon:studio:v1:presence:<runtimeId>` (45-second lifetime, refreshed
every 15 seconds using Redis time). More than one live member is ambiguous and
fails closed, including on the local replica. An admission connection pins that
exact socket and its authenticated principal; reconnect never silently reroutes
an in-flight operation. Expired members cannot be revived by heartbeat.

Requests and replies use per-process Pub/Sub channels, bounded concurrency and a
propagated deadline. They are never written to the retained realtime streams:
token replies are transient. PUBLISH retries are disabled; loss after publication
or daemon dispatch is an unknown outcome, recovered through the existing durable
Studio reservation and fresh admission rather than replaying an expired dispatch.
Readiness checks the subscription with a round trip. Subscription/heartbeat failure
stops the supervised relay worker and drains the API; process restart reestablishes
subscriptions and daemon connections. Shutdown removes only its own socket members;
crashed-process members expire. The hub retains its local path, while server
configuration now requires Redis for the scheduler in every deployment.

All API replicas must run the relay-aware build. During the first rolling upgrade,
sockets on older replicas are not discoverable until their daemons reconnect to
upgraded replicas. No daemon/CLI wire change or database migration is required.


## Data access

The shape is `handler → service → sqlc queries`. Generated code is
`pkg/db/generated` (package `db`), queries are `pkg/db/queries/*.sql`;
`sqlc.yaml` names schema (`migrations/`), queries and output, so those paths
are load-bearing.

- A query that binds a person (any `*user_id` or `actor_roles` parameter in
  any sqlc spelling, except one that only records who did something) and
  reads a Project-scoped table names one of the access views
  (`project_access`, `task_access`, `conversation_readers`,
  `attachment_access`, `output_access`) or, for a write, calls
  `project_permission_held` (ADR 0017), or for a Run or the voice
  assistant the delegate rule, `delegate_permission_held` or
  `run_permission_held` (D31). `internal/db/query_shape_test.go` fails on one
  that does not and on hand-written Go SQL over `projects`, `tasks`,
  `task_dependencies` or `conversations`; its allowlists name the step that
  sweeps each remaining statement, or why it is not swept.
- Product-domain services hold `*db.Store` (`internal/db`): reads through
  `store.Queries()`, writes through `store.WithinTx` or `store.WithinRawTx`.
  The daemon-driven run kernel depends on the narrower
  `execution.Transactor`; `*db.Store` implements that seam.
- Cross-domain writes are exported functions taking the **caller's**
  `*generated.Queries` handle — e.g.
  `inbox.CreateInTx(ctx, tx, pending, input)` and
  `conversation.MaterializeScheduleRunInTx(ctx, tx, pending, input)`. The
  callee is a transaction participant, never an owner.
- Import direction between the agent and run domains: `internal/agent`
  imports `internal/execution` (runtime removal calls
  `execution.RetargetPendingRunsInTx` to move the rebound agents' pending
  runs, and `execution.PublishRuntimeWork` after commit). `internal/execution`
  must never import `internal/agent` — not even from a test file, which Go
  counts in the cycle; the seams it needs from the agent domain
  (`agent.FreezeRunConnectorSnapshotInTx`) reach it through
  `execution.ConnectorSnapshotter`, wired by the composition root.
  `internal/agent` also imports `internal/inbox` — the stale sweep raises the
  `runtime_offline` item and the heartbeat and registration close it, all
  through the Inbox's exported in-transaction seams. `internal/inbox` imports
  no domain, so this edge cannot close a cycle.
- Events are staged inside the transaction and published after commit with
  `bus.Publish(ctx, topic, …)`. `cmd/server/app.go` holds the consumer ledger
  — `daemonws.RegisterListeners`, the discussion dispatcher
  (`discussion_realtime.go`), `registerTaskReadiness` (`task_readiness.go`:
  a member's Agent setup or computer changed, so their open Tasks go out
  again Company-wide as `task.updated`), then `registerRealtimeRouting`, then
  `activity.RegisterListeners` last because the bus delivers whole-stream
  consumers after every topic's own. `cmd/server/realtime_routing.go` is the
  one list of what reaches a browser; a routing fact stays off the wire by
  being tagged `json:"-"` on the payload. A frame about a Project or the work
  inside it embeds `auth.ProjectFrame`: its audience, decided in the
  producing transaction (`auth.StampProjectAudiencesInTx`, ADR 0017), and
  the router sends it to exactly that audience or drops it.
- Row → domain mapping lives in plain unexported functions. There is no DAO
  and no repository interface; non-storage collaborators (password manager,
  OAuth provider, object store, email senders) stay interfaces.

## Adding an HTTP endpoint

1. Add or extend `internal/handler/<resource>.go`; endpoints are methods on
   the single `*Handler`. Service dependencies are consumer-side interface
   fields on `Handler` (`TaskService`, `InboxService`, …); the daemon plane
   also takes `execution.RunService`, `*db.Queries`, `execution.Transactor` and
   the Bus directly.
2. Register the route in `Handler.registerRoutes` and update `wantRouteTable`
   in `route_table_test.go` — registration is conditional on non-nil deps, and
   that test turns a forgotten production wire into a failure instead of a
   silent 404. Then say who may call it in `route_actor.go`: `person` (the
   usual answer; a Run proof gets 403 `human_actor_required`), `run` (the
   handler resolves the proof and checks `runProofBearer`) or `any` (Company
   metadata only, with a comment saying why).
   `TestEveryRouteDeclaresItsActorType` fails on a route with no entry.
3. Bind with `bindRequest` (strict JSON, size-capped, unknown fields
   rejected) — it returns `(T, error)`, so pair it with `writeRequestError`.
   Gin's `binding:"…"` tags run inside it (`request_body.go`).
4. Map errors with an `errorMapping` table and `writeMappedError`;
   `error_mapping_test.go` pins every `{status, detail, code}` triple.

## Linting and shape checks

`server/.golangci.yml` (golangci-lint v2, version pinned in the root Makefile)
is the whole lint story — there are **no** depguard or importas rules; import
direction is held by the compiler (`internal/`), by convention, and by review.

- Formatters: gofumpt (extra rules), goimports (local prefix
  `boardly.dev/boardly/server`), golines (120 cols). `make go-fmt` rewrites,
  `make go-fmt-check` diffs.
- Linters: the standard set plus a curated list (bodyclose, errorlint,
  gocritic, revive with the golint/Google rule set, testifylint, …);
  `errcheck` also checks type assertions; misspell keeps `cancelled` as a
  domain spelling. Staged-debt exclusions (exported-identifier doc comments,
  ST1005) are documented inside the file and meant to be burned down.
- `make go-shape` holds four greps at zero: no `doc.go` anywhere; no type
  assertion back to `*generated.Queries` in `internal/`; no identifier
  beginning with `Postgres`; PostgreSQL testcontainers only under
  `tests/testpostgres`.
- `make go-sqlc-check` regenerates sqlc and fails on drift. `make go-gate`
  runs shape + sqlc + build + vet + the full `-race` suite (`-tags=integration`)
  through `tests/testpostgres`; `make gate` adds the frontend lanes.
- `make go-unit` is the lane CI gates on: the same checks minus the container,
  with `*_integration_test.go` files tagged out. `make go-integration` is the
  container lane, and CI only runs it on a manual workflow dispatch.

Company deletion lives in `internal/company/delete.go`. Under the Company and
membership locks, it removes Task report batches, mail draft groups, presentation
captures and Huddles before cascading the Company graph: their immediate or
restrictive foreign keys would otherwise block parent deletion. This cleanup,
FK-free Run records and retained Activity all share the deletion transaction;
access-revocation receipts publish only after commit.

Company invitations live in `internal/company/invitation_service.go`: owner management, verified-email acceptance, renewal requests, and leased durable email delivery. The composition root in `cmd/server/invitations.go` adapts the existing email provider. Token hashes authorize links; encrypted delivery secrets are erased after terminal delivery.


Membership removal and leaving live in `internal/company/membership_service.go`.
They use the execution transaction participant in
`internal/execution/membership.go` to cancel work and revoke Run tokens before
publishing access changes. Realtime routing disconnects the revoked user's
Company sockets through a durable relay control; the hub remains domain-neutral.
Client registration rechecks Company authorization after joining, closing the
upgrade/revocation race.

Reusable Company invite links live in `internal/company/share_link_service.go`
as part of InvitationService. They reuse the existing invitation preview/accept
routes and desktop handoff, with owner-only create/copy/reset/disable management,
hash lookup, encrypted owner retrieval and Company-locked membership writes.

Managed Tool auth in `internal/daemon/tool_service.go` is isolated by machine,
Company, Agent, Tool and API origin through one generic home resolver. Shared
installations grant no account access. Desktop operations verify the member's
Agent binding, and Runtime Tool projections carry sanitized `agentId` profiles.
Studio editor connections resolve that scope from the authorized Conversation
or Task, and own separate CLI bridges per Runtime and Agent. New authenticated
Tools must use the same scope for login, status, cancellation and execution.

Company initialization lives in `internal/company/initialization.go`. The human
founder starts one idempotent, platform-authored research Run on their own CEO
binding using the runtime's advertised Luna model, then continues into the app:
the Run works in the background and writes linked Company Wiki pages through
`wiki_write` (hub key `company-overview`). The `company_initializations` receipt
serializes starts and retries and preserves the original company description.
`Get` reports `phase` (`not_started|research|ready`), the Run status,
`pagesWritten` (counted over every research attempt of the Company)
and `overviewPageId`. `Company.initializationPending` is true only while the
receipt exists and `research_run_id` is still NULL. The terminal hook lives in
`execution/company_research.go`: in the same transaction as the Run's terminal
write and its Inbox item, `settleCompanyResearchInTx` sets `completed_at` when
the Run is the research lineage's final attempt (a failure that creates a retry
child is not final), and the founder's `run_completed`/`run_failed` item gets
dedicated copy and a `/wiki[/pages/<id>]` deep link. A retry (`retry: true`
after a failure) starts a new Run, clears `completed_at` and archives the
failure notice (`inbox.CloseEntityInTx`); the new Run owns the pages earlier
attempts wrote (`run_wiki_writes` is scoped to the Company, which has one
research), so it updates them rather than duplicating them.
`conversation.MaterializeInitializationRunInTx` owns conversation writes. A new
send to the receipt's CEO conversation is fenced (and its queue paused) only
until the research starts; afterwards a message queues behind the research Run.
A bounded Company reconciler is a recovery only: it completes a receipt whose
final Run the terminal path could not reach, rechecks the creator's membership
under the Company lock, and releases the preserved queue through
`conversation.ResumeInitializationQueueInTx`; it never starts discovery, a Run
or a retry. `execution` freezes the one-run model override. The daemon uses a research
policy instead of the normal CEO planning policy (`daemon/company_initialization.go`).

### Calendar Task scheduling

`internal/task/calendar.go` owns acceptance, schedules and bounded Calendar
reads. `calendar_worker.go` materializes each recurring occurrence as a new Task
and Task Conversation and starts it through the ordinary Task execution path.
Company Plan uses `DetachDeletedCalendarTemplatesInTx` before deleting a template
to detach retained occurrences and cancel only their unclaimed first attempts;
the Plan owns the combined Task history snapshots.
`calendar_beat.go` uses the existing Schedule Asynq queue to distribute identity-only
jobs; PostgreSQL receipts and Company transaction fences make duplicate deliveries
safe. `handler/task_calendar.go` is the HTTP boundary. Connector Schedules remain
creator-private and use the non-task lane; Task schedules use Project access and
Task capacity. Migration 000097 is additive and preserves legacy Task acceptance.
The detailed behavior and operating limits live in
[`docs/specs/scheduler/TASK-CALENDAR.md`](../docs/specs/scheduler/TASK-CALENDAR.md).
