# Migrations

The PostgreSQL schema (`*.up.sql` / `*.down.sql`, golang-migrate format),
embedded into the binaries via `embed.go` and doubling as sqlc's schema input
(`sqlc.yaml` points `schema:` here). `embed.go` also exposes
`LatestVersion()`, from which `internal/db` readiness derives its expected
schema version — adding a migration can never silently desynchronize the
readiness fence.

Append feature migrations after the latest migration on `main`; preserve files
already on `main`. Prefer additive tables, indexes and simple schema changes
that preserve existing rows. Verify the upgrade from the current `main` schema.

The historical recreate baseline is exactly eight domain-grouped files, applied in order:

1. `000001_identity` — users, auth sessions, oauth accounts, the
   login / verification / password-reset flows, desktop handoff codes
2. `000002_tenancy` — the Company tenant root: companies, structured business
   profiles, memberships, typed company resources, the owner-controlled Proactive Mode preference, the
   original Work Plan parent records (removed by `000016`), and starter journey progress
3. `000003_agents` — Agents (the agent-employee domain): agent rows, the
   reportsTo tree, and the department configuration and capability tables
   (removed by `000017`)
4. `000004_work` — the product work surface: S3-compatible attachments,
   Conversations with creator read/pin/archive state, `conversation_items` —
   the one immutable stream carrying both the user's words and everything the
   agent produced answering them, ordered by a single per-conversation counter
   and labelled (not owned) by `run_id` — the deferred Conversation run queue,
   tasks (including task-only plan ownership), and task dependencies
5. `000005_execution` — the execution machine: runtimes, runs (which have no
   transcript table of their own — a run's output is its slice of `000004`'s
   stream), run_tokens, durable approvals and questions, provider-native
   Connector and Plugin catalogs, Schedules and their occurrence receipts,
   Task report batches with manual/automatic provenance, durable Task merge
   jobs, and human-input continuation receipts. A daemon has no table; each
   runtime row carries the registering daemon's persistent id as plain text.
   Runs are claimed via `FOR UPDATE SKIP LOCKED`, with `updated_at` as the
   delivery-receipt clock and a 45s prepare lease the daemon renews until the
   run starts
6. `000006_platform` — activity feed (closed `action` vocabulary + details),
   inbox items (unread counts derived by `COUNT(*)` over the partial unread
   index — no counter table, no outbox), crash reports, the Company mailbox,
   Run-bound outbound mail drafts, and Company-owned Commerce projections
7. `000007_usage` — immutable, FK-free per-run provider/model token and cost
   facts used directly by usage reports
8. `000008_usage_basis` — stamps each usage fact with whether its provider
   counters are per-Run or cumulative across a resumed session

The additive migrations continue that sequence for existing databases:

9. `000009_outputs` — immutable Run Outputs and their Asset Versions,
   founder-reviewed Deliverables, durable publication jobs, and the Inbox
   constraints needed for review actions
10. `000010_publication_subdomains` — persistent hostname reservations and
    the durable routing stage for Output publications
11. `000011_webapp_outputs` — persisted Output type for automatic webapp
    publication; existing Outputs remain file results
12. `000012_runtime_lifecycle` — the user-facing runtime lifecycle columns
    on `runtimes`: typed `unusable_reason_code` / `unusable_reason_message`
    with `runtimes_unusable_reason_check` making an unusable runtime
    structurally offline, nullable `custom_name`, the user's machine
    name, which registration never writes, and nullable `removed_at`, the
    owner's soft removal, which registration clears — a removed row leaves
    the roster and the claim path but keeps its run history
13. `000013_runtime_offline_inbox` — the `runtime_offline` Inbox kind the
    stale sweep raises when it takes a machine with in-flight runs offline;
    the down file deletes those rows before restoring the six-value CHECK
14. `000014_run_tracking_identity` — database-generated UUID Run identities
    and explicit Run tracking metadata
15. `000015_activity_history` — immutable Activity snapshots, frozen visibility,
    subject revisions, and the canonical Task/Conversation/Run state view.
    A database created from a ladder older than 2026-09-12 cannot pass this
    step. That day `000004` gained `tasks.labels`, `tasks.start_date`,
    `tasks.due_date`, `conversations.request_key` and
    `conversation_user_states` in place, and this step's view reads the Task
    columns. Such a database stops here, dirty at version 15. A later migration
    cannot repair it because it would run after this step, so recreate the
    database. Every database past version 15 already has these objects.
16. `000016_remove_goals` — removes the retired Goals table and Task parent
    column, foreign key, and index. Tasks, Conversations, dependencies, and
    historical Activity and Inbox records are preserved. Current Task snapshots
    no longer carry the retired parent field.
17. `000017_agents_only` — every worker is an Agent (ADR 0010): moves the
    avatar key and accent colour a department configuration carried onto
    `agents`, drops the Task's department owner pointer, the department
    configuration and capability tables and the department key on
    `agents` and `task_report_events`, recreates the current Task snapshot
    view without the retired field, and renames the starter journey step
    `activate_departments` to `meet_starter_agents`. Tasks keep their
    assignee, description and dependencies.
18. `000018_projects` adds Projects, mandatory Task Project ownership and
    composite dependency endpoint foreign keys enforcing one Project. This step
    targets fresh installations only: it performs no General Project seeding or
    backfill and refuses nonempty Task tables. Its down migration removes Project
    ownership while preserving Tasks and restoring Company-scoped dependency FKs.
    Application writes must supply Project identity before using this schema;
    the Project API and Company Plan v5 are separate implementation steps.

19. `000019_task_project_activity` includes immutable Project identity in safe
    Task Activity snapshots. Its down migration restores the previous view before
    Project ownership can be removed.

20. `000020_company_invitations` and 21. `000021_company_share_links` add
    member invitations with delivery tokens and revocable share links.

22. `000022_runtime_sign_in` — the daemon's provider sign-in verdict on
    `runtimes` (`provider_sign_in_state`, `_detail`, `_checked_at`), taken
    with the provider's own status command in the same probe round as the
    version and refreshed on demand. It never changes `status`: a signed-out
    provider stays online and its runs fail with a readable reason.

24. `000024_artifact_types` migrates saved Outputs to the user-facing Artifact
    classes (`app`, `media`, `text`, `file`), preserving earlier web applications
    as apps and earlier file sets as files.
25. `000025_local_app_previews` adds the object-shaped JSONB Artifact payload
    and nullable derived loopback URL used by daemon-supervised app previews.
    It also lets one running Run save multiple immutable Artifacts in a
    deterministic presentation order. Every presentation can become its own
    Deliverable, and exact replays remain idempotent across the Run's full
    history.
26. `000026_runtime_visibility` — runtime execution consent (ADR 0007,
    ADR 0011; the sharing columns and the binder are removed again by
    `000059`): `runtimes.visibility` (`private | public`, default private)
    with `runtimes_visibility_check`, `runtimes.visibility_revision`,
    `agents.runtime_bound_by_user_id` — the member who bound the Agent
    to its runtime; a binding by the runtime's registrant is hosting consent —
    and `runs.execution_authorized_by_user_id` — the member whose permitted
    action asked for the Run. Attribution columns, so no FK. The up file is
    also the cutover: every existing row becomes private, existing bindings
    are credited to a registrant who is a Company owner, each unfinished
    Run's authorizer is recovered only from its own user message, direct
    conversation or human decision, and pending Runs that cannot be proven
    are cancelled with a readable reason. The down file drops the columns and
    forgets every sharing decision and every binder; the cancelled Runs are
    not restored.
27. `000027_membership_roles` — `company_invitations.role` and
    `company_share_links.role` (`admin` or `member`, default `member`, with a
    CHECK on each): the role a newcomer joins with (ADR 0012). Nobody is
    invited as owner; existing rows default to `member`, which is what every
    earlier invitation and link granted.
28. `000028_auth_refresh_idempotency` — `auth_sessions.rotation_id`, the
    client-authored refresh attempt that identifies a committed replacement
    session after its response is lost.
29. `000029_agent_creator` — `agents.created_by_user_id`, the member who
    created the Agent (ADR 0013); with the `agents.manage` holders, the one
    who may configure it afterwards. Attribution, no FK; NULL for Agents that
    predate it, which stay configurable by `agents.manage` alone. Its own
    migration because 000026 had already run on deployed databases.

30. `000030_calendar` — personal and Company calendars, participant RSVP,
    recurrence exceptions, per-user reminder completion, Run tool receipts and
    durable Inbox reminders/status updates. It upgrades version 29 directly;
    calendars are provisioned lazily by authorized application access. The down
    migration removes Calendar records and Calendar Inbox items and restores
    the previous Inbox constraints. No Social tables or credentials are included.
31. `000031_discussions` — human DM pairs, private-channel configuration,
    human/Agent grants and generations, personal pin/hide state, and zero-Run
    text/system items. Deferred shape checks enforce final DM/channel membership;
    legacy item attribution stays required and discussion Conversations reject
    Runs. Discussion Activity is internal evidence with NULL visibility. The
    down migration refuses to discard retained human DMs or channels. Application
    rollback must preserve this schema and readers that understand these types.
32. `000032_general_channel` — the system-owned Company General channel, with
    active-member backfill and discussion integrity constraints.
33. `000033_discussion_collaboration` — single-level reply identity, edit/delete
    timestamps, scoped message pins/reactions and grant-bound thread follow state.
    Item shape keeps tombstones immutable and forbids nesting or legacy Run items
    from acquiring discussion fields. Collaboration shape requires live human
    discussion messages for pins/reactions and root/current-grant thread writes.
    The down migration refuses to discard retained collaboration content or state.
34. `000034_social` — private social connections and targets, encrypted OAuth
    flows with web/desktop binding, credential maintenance leases, and durable
    immediate-publishing receipts. It upgrades main at version 33 by creating
    four tables and their indexes only; it does not rewrite existing tables or
    data. The down migration removes only Social data.
35. `000035_calendar_custom_recurrence` — recurrence intervals, selected weekdays,
    monthly weekday patterns and optional occurrence limits. Existing series keep
    their behavior through defaults. The down migration preserves event rows but
    discards custom repeat settings; retain this schema when rolling application
    code back if those settings must be preserved.
36. `000036_output_access` — Artifact ownership, per-person `view` or
    `manage` grants, Conversation grants, Company/public visibility, public-link
    tokens, Company sharing policy, discussion Artifact references, and
    membership-fenced access views. It retires unprotected publications and
    creates no comment or access-history table. Rollback removes sharing state
    while preserving the Outputs themselves.

Migrations 37–58, 61–63, 65–67 and 70–88 are not in this list; several have
their own section below, and 61, 65, 66, 67 and 75–85 have none yet.

59. `000059_agent_runtime_bindings` — private runtimes, Agent templates and
    per-member bindings (ADR 0014, superseding ADR 0007 decisions 2–4 and
    ADR 0011). Every runtime is private to its registrant and only the
    registrant's own authorized Runs execute there: `runtimes.visibility`,
    `visibility_revision` and `agents.runtime_bound_by_user_id` are dropped.
    An Agent is a provider-typed template (`agents.provider`, backfilled from
    its runtime, with `agents_provider_check`) and loses `runtime_id`; a
    member binds an Agent to one of their own runtimes of that provider in
    `agent_runtime_bindings` (one row per `(agent_id, user_id)`, provider
    matched by composite foreign keys onto `agents(company_id, id, provider)`
    and `runtimes(company_id, id, provider)`). A Task always names a member
    assignee (`tasks.assignee_user_id NOT NULL`) and optionally the Agent that
    does the work (`executed_by_agent_id`, renamed from `assignee_agent_id`;
    the exactly-one CHECK goes), and carries `workspace_generation`, the
    identity of its workspace on the assignee's machine, advanced only by a
    confirmed handoff so a daemon never continues another member's stale
    files. `task_run_requested` and `task_handoff` join
    the Inbox kinds; the safe Task snapshot names `executedByAgentId`. The
    up file is also the cutover: one binding per Agent for its runtime's
    registrant when that registrant is an enabled, active member (an Agent
    whose registrant is gone stays a template with no binding); each
    Agent-assigned Task's assignee becomes that registrant, else its creator
    while an active member, else the Company's earliest active owner; and
    pending Runs the new claim can never admit are cancelled with a readable
    reason: those whose authorizer does not own their runtime, and queued or
    deferred ones on a runtime that no binding of their Agent and authorizer
    names (an Agent that moved left them behind). The channel tag such a Run
    served is closed (`agent_unbound`), so it does not hold its
    channel-Agent Conversation's active slot. In-flight Runs finish under
    their existing generation fences. **The down file restores the shape, not the facts:**
    every runtime returns as `private` at revision 1; `agents.runtime_id`
    comes back **nullable** (weaker than the original NOT NULL) from the
    creator's binding, else the oldest binding, else any live runtime of the
    Agent's provider, and `runtime_bound_by_user_id` from that binding's
    member; every other member's binding is lost; the human assignee of every
    Agent-executed Task is cleared so the exactly-one CHECK holds; the
    cancelled Runs are not restored. A build older than 57 that requires a
    non-null `agents.runtime_id` refuses Agents that had no binding. Roll
    back only as part of a reviewed downgrade; a backup is the real rollback.
60. `000060_task_output_authorizer_grants` — the member who ran a Task can see
    what their Run presented. A Task Run executes on its authorizer's computer
    (ADR 0014), but `000036` made a Task Output its creator's alone. Before
    ADR 0014 a Run's authorizer was whoever started it (a message sender, the
    member who assigned or started the Task, an Approval or Question answerer)
    and the Run ran on the Agent's runtime, often someone else's computer. So
    the up file gives an earlier Task Output the `view` grant `InsertOutput`
    now makes only when its Run clearly ran for its authorizer: the authorizer
    registered the Run's runtime, is the Task's assignee, is not the owner, is
    an active member, and was a member when the Output was saved. Every other
    earlier Output keeps the access it had; its owner can still share it.
    It only adds access: the owner and every existing grant stay as they were,
    and an Output that gains a grant becomes `shared` with its access revision
    advanced. No schema changes, so the down file changes nothing and keeps the
    grants.
64. `000064_run_wiki_reads` — the evidence trail of what Agents read in the
    Company Wiki. Adds `run_wiki_reads`, append-only: one `search` row (the
    credential-redacted query and its match count) or one `read` row (page,
    exact `revision_id` and number returned, the title then, and the character
    range) per Wiki tool call of a Run. `run_id` cascades with its Run, the
    composite `(company_id, page_id)` edge and `revision_id` cascade with the
    page and revision, and `agent_id` is attribution with no FK. CHECKs pin
    the two kinds and each kind's row shape. Purely additive; existing rows are
    untouched and the down file drops the table and its trail.
68. `000068_wiki_artifact_refs` — the Artifacts each Company Wiki page
    currently cites with `[[artifact:<id>]]` or `[[artifact:<id>#<ordinal>]]`.
    Adds `wiki_artifact_refs`, a current-state index rebuilt on every page
    write like `wiki_links`; the revision body is the history. A row grants
    nothing: every read re-checks `output_access`. The page, Output and
    `(output_id, ordinal)` item edges cascade; a NULL ordinal cites the whole
    Artifact and MATCH SIMPLE skips the item edge. `UNIQUE NULLS NOT DISTINCT`
    keeps one row per citation. Purely additive; the down file drops the table.
69. `000069_output_list_indexes` — lets the Company-wide Artifact list seek
    each source of a viewer's access newest first instead of scanning every
    Output. Replaces `outputs_owner_idx` with `outputs_owner_created_idx`
    (same prefix, plus `created_at DESC, id DESC`) and adds the partial
    `outputs_company_visible_created_idx` for `visibility = 'company'`.
    Indexes only; the down file restores `outputs_owner_idx`.

Earlier migrations retain their historical Goal and department definitions
and references so an existing database can upgrade through the same sequence.
They do not describe a supported product feature. Existing databases must apply
the additive removal migrations; changing a previously applied baseline alone
would leave the retired schema in place. Neither removal can reconstruct the
deleted records on rollback, so their down migrations require restoring a
backup.

## Schema doctrine

Stated in the header of `000001_identity.up.sql` and held throughout:

- DB-generated ids via `gen_random_uuid()`; sqlc maps UUID columns directly to
  `google/uuid` and uses `pgtype` for nullable scalar and timestamp columns.
- **Real FOREIGN KEY edges on structural ownership** — `ON DELETE CASCADE`
  for owned children and `ON DELETE SET NULL` for optional pointers.
  Attribution and polymorphic actor columns carry no FK; this includes the
  creator attribution on immutable fact rows. `000007_usage` is
  entirely FK-free by design: usage history must survive deletion of the runs,
  agents, and companies it measured.
- The circular task ↔ conversation pair is added after both tables exist and
  is `DEFERRABLE INITIALLY DEFERRED` (`000004_work`), so the pair can be
  created — and deleted — in one transaction.
- A status column is the state machine; partial unique indexes are the
  at-most-one guards (one live root agent per company, one conversation per
  task, …).
- `text` + CHECK constraints for enums, ranges, and structural cross-column
  invariants that the database can enforce. jsonb columns get object-shape checks.
  `created_at` on every table, `updated_at` wherever rows mutate.
- No general stored procedures or trigger-driven product writes; seeding is
  application code. The explicit exception
  is `000031` and `000033`'s discussion integrity checks: deferred detail/grant shape,
  immutable discussion identity/receipt, legacy versus discussion item shape,
  rejection of Runs on discussion Conversations, and scoped collaboration shape. Five trigger functions
  validate or reject writes; they never materialize product rows, publish events
  or own lifecycle transitions. The migration test pins these exact objects.

This baseline replaced the previous ladder wholesale. Databases created from
any older ladder are dropped and recreated — never migrated or restamped.

Down files reverse their up file with children-first drops (`000004`'s first
breaks the circular edge). Rolling back drops business data, so
`cmd/migrate -direction=down` refuses to run without `-confirm-down`.

## Runners

The server applies the baseline automatically at startup: `db.MigrateUp`
(`internal/db/migrate.go`), called from `run()` in `cmd/server/app.go` before
serving — a failed migration exits non-zero so the previous deployment keeps
serving. Concurrent replicas serialize through an advisory-lock gate
(completed try-lock polling around golang-migrate's blocking lock, so a
statement timeout cannot strand a waiter) on a simple-protocol connection
with a bounded `statement_timeout`. `cmd/migrate` drives the same functions
for operators without booting the API. Usage reports read immutable
`run_usage` facts directly; there is no startup backfill step.

Regenerate sqlc after any migration or query change
(`cd server && sqlc generate`); `make go-sqlc-check` fails the gate on drift.

## Zenith Studio (000023)

`conversation_studio_projects` binds one remote Project to a Conversation;
`output_studio_canvases` supplements a saved Output with source provenance once.
The migration also adds the `final` item role for additional completed results.
Rollback removes these owned records and maps `final` items to `supporting`
before restoring the previous role constraint, preserving the saved media.

## General channel (000032)

Creates a separate, system-owned General for each existing Company, without
promoting private channels that happen to have the same name. It extends the
000031 discussion shape/identity functions to protect General and allow a
Company-wide human roster. Rollback refuses while General channels exist so
retained chat history cannot be silently discarded.

## Discussion collaboration (000037–000040)

000037 adds typed references and discussion notifications. 000038 adds Company
member presence. 000039 fences notification visibility by active membership and
live messages. 000040 stores per-channel notification preferences.

## Group direct messages (000044)

Extends human DMs to fixed sets of 2–50 people. Existing pair rows and IDs stay
unchanged. Groups use a sorted participant key for unique creation, validated
against membership rows by the deferred discussion shape constraint. Membership
rows retain the Company/user foreign keys; revoked members remain in the identity.
Rollback refuses while any group DMs exist to preserve their history.

## Mixed direct messages (000045)

Adds Agent IDs to the fixed DM identity, retaining human-only groups and pair IDs.
Groups allow up to 50 humans (including the creator) and 20 Agents. Exact sorted
identity arrays are checked against retained human and Agent grant rows. Agent
archive revokes its grant without introducing channel-only system items. Rollback
refuses while mixed DMs exist. Agent membership does not authorize a discussion Run.

## Direct message invitations (000046)

Permits append-only membership expansion of group DMs while preserving pair identity,
Company scope and grant integrity. Expanded groups leave the canonical participant-set
unique index so separate chat histories can acquire the same roster without merging.
Rollback refuses while expanded groups exist.

## Direct message system items (000047)

Extends the existing zero-Run system-item constraint to group DM invitation notices.
One typed `members_added` item records all human/Agent grants added by a batch,
with the inviter ID. Pair DMs and unrelated system actions remain prohibited.
Rollback refuses while these retained notices exist.

## Company Wiki (000055)

Adds Company-scoped collections, pages, immutable revisions, current directed
links and per-user favorites. A deferrable composite foreign key keeps each
page's current revision on that same page and Company. Each page row carries a
GIN-indexed `search_vector` of its current revision's title and body, rewritten
with `current_revision_id`, so search never tokenizes stored bodies. List and
count queries tokenize search text with the same parser in a materialized CTE,
without a stored function. The migration does not copy Company Memory or grant
Agent access. Rollback removes Wiki records.

## Direct message Agent invocations (000056)

Extends the existing discussion item constraint and execution revision fence to
group direct messages. This follows Company Wiki at version 000055; migration
versions must have exactly one up/down pair.

## Project files (000057)

Registers ready Company uploads as Project logos and reference documents. Replacing
or removing a file retires its association; it does not delete its bytes. Retired
associations remain until Project deletion so frozen Task Runs and retries retain
their exact inputs. Both phases of attachment cleanup and explicit deletion respect
these references. Active Company members can download current Project files.

## Studio editor binding (000058)

Adds the Studio origin, account, reservation and binding revision fields used to
create one Studio project for a Conversation and return a short-lived bearer
credential to the canvas.

## Plugin presentation (000062)

Adds the Codex presentation a Runtime observes for each Plugin to
`plugin_runtime_observations`: the long description, up to three suggested
prompts, the brand colour, the developer and the developer's links, with
checks on the prompt count and colour format. A Claude observation leaves them
empty. A partial name index serves logo borrowing. Presentation only; rollback
drops the columns, and the next discovery fills them again.

## Plugin catalog matching (000063)

Adds the keys that match one Codex Plugin to another to
`plugin_runtime_observations`: `name_key` and `website_host`, stored generated
columns, and `curated_duplicate`, which the backfill and every later discovery
write set for a third-party copy of a curated Plugin that the list leaves out.
Three indexes on the keys replace 000062's name index. Presentation only;
rollback drops the columns and restores that index.
## Huddles (000070–000084)

The Huddle migrations are 000070–000084, with company initialization at
000085. 000074 (missed Huddle Inbox) keeps the `analytics_budget` Inbox kind
when it redefines `inbox_items_kind_check`.

Adds the durable Huddle session and its separate `huddle` chat Conversation.
The migration enforces one active Huddle per human DM/channel source, supports
idempotent start request keys, and keeps Huddle chat rows out of Run-backed
discussion integrity checks. Room, admission, invitation, media, and AI
lifecycle state are added in later migrations.

## Huddle connections (000071)

Adds idempotent server-side connection reservations. A reservation records
admission intent and does not claim that LiveKit media is connected; a later
webhook/reconciliation transition owns `connected`.

## Huddle reservation expiry (000072)

Adds a 30-second reservation deadline so an unconfirmed Join cannot hold a
connection slot indefinitely. Connected rows are unaffected; a reconciler may
revoke expired reservations.

## Huddle invitations (000073)

Adds idempotent per-invitee Huddle invitations with explicit pending,
accepted, declined, expired, and revoked states. Invitation acceptance remains
separate from source Conversation membership.

## Missed Huddle Inbox (000074)

Allows the durable `huddle_missed` Inbox kind. The Huddle lifecycle pass
creates one deduplicated item for each expired invitation whose invitee is
still an active Company member.

## Project access (000086)

Project access scopes (ADR 0017). It runs after the data-only
`000060_task_output_authorizer_grants`, whose Output grants the rewritten
`output_access` view below then reads like any other grant, and after
000061–000085, none of which redefines a view, function or CHECK it rewrites. `projects` gains `visibility`
(`restricted` by default, or `company`) and `access_version`. `project_grants`
holds one row per person per Project permission, its CHECK mirroring
`auth.ProjectPermissions()`. A generated `read_permission` column and the
deferred self foreign key `project_grants_read_fkey` make every row point at
the person's `project.read` row, so no other permission is stored without
read. Grant timestamps use `clock_timestamp()`, the clock `company_members`
uses, so the rejoin fence compares like with like. `conversations`, `inbox_items`, `activity_events`
and `task_report_events` gain a typed, nullable `project_id` with no foreign key.
The rule lives in one `LANGUAGE sql STABLE` function, `project_permission_held`,
and four views, `project_access`, `task_access`, `conversation_readers` and
`attachment_access`; `output_access` is redefined so its Company-visibility arm
reaches only readers of a restricted source Project. Go binds every permission
set and role list, so the function and views name no permission or role.
`conversation_readers` and `attachment_access` are UNION ALL views, so
queries that check a list of rows reach them through a per-row lookup naming
the row and the person; `attachment_access` does the same inside its own chat
arm, checking each Company member against the chat's readers one person at a
time rather than joining the chat's whole reader set.

Backfill: every existing Project becomes `restricted`, and every active owner
holds every permission on every Project of their Company; nobody else holds a
row. Task chats take their Task's Project, and Inbox, Activity and report rows
take the Project of the Task, Task chat, Run, Approval, Question, Deliverable,
message, file or Project they reference through typed columns (never
jsonb). Task chats whose Task was already deleted keep no Project. The
`conversations_task_project_check` CHECK, stamped with the migration's own
time, allows a missing Project only on a Task chat whose Task is gone
(`task_id IS NULL`) and that predates the migration, so a live Task's chat
cannot lose its Project. The CHECK is added after the backfill: the backfill
update queues the deferred discussion trigger, and Postgres refuses
`ALTER TABLE` on a table with pending trigger events, so the migration runs
`SET CONSTRAINTS ALL IMMEDIATE` first.

Cutover (the `000059` pattern), at the end of the up file: a Run that has not
ended (queued, deferred, dispatched, waiting for its directory or running) in
a Task chat whose authorizer no longer holds `project.tasks.execute` on the
chat's Project, or in a Task chat with no Project, or with no authorizer, is
cancelled with `error` `project_access_revoked`, its Run tokens are deleted,
and its Task takes the cancelled Run's terminal state exactly as
`UpdateTaskForRunTerminal` would. Tagged Runs live in channel-Agent chats, so
no channel tag is touched. A pending Approval or Question raised in a Task
chat with a Project gets an Inbox item for each of the Project's deciders
(readers holding `project.approvals.resolve`, here the active owners), built
as the application builds it and deduplicated by the same key; every other
open copy is read and archived; a request with no decider goes to every
active owner. A deferred prompt or saved Task Message queued in a Task chat
by someone who no longer holds `project.tasks.write` there (or in a chat with
no Project) is removed with reason `project_access_revoked`, its file
reservations are released and its Inbox nudge is read and archived, as
`RemoveProjectRunQueueItemsForAccessRevocationInTx` does. No frames or
Activity rows are written.

Deploy: this migration needs a stop-the-world deploy. An older server inserts
Task chats without `project_id`, which the CHECK refuses once the migration
commits, so drain older servers before it runs. The migration holds exclusive
locks on `conversations`, `inbox_items`, `activity_events` and
`task_report_events` while it fills `project_id`; time it on a staging-sized
copy first.

Rollback restores the exact `000036` `output_access` body, then drops the
views, the function, the CHECK, the indexes, the `project_id` columns,
`project_grants` and the two `projects` columns. Grants are lost; Projects,
Tasks and chats are untouched, and what the cutover cancelled or closed stays
so.

## Activity subject Project (000087)

`activity_subject_states` gains a last column, `project_id`: the Task's
Project, a chat's Project, or the Project of a Run's chat (NULL for a chat
or Run outside any Project). The owner recorder stores it in
`activity_events.project_id` from the same observation it already takes under
the owner's locks, so a deletion, whose only observation is the one taken
before it, still names its Project (ADR 0017). The body is otherwise
000070_huddles', `huddle` chats included. Rollback drops the view and creates
000070_huddles' again.

## Project access change time (000088)

`projects.access_changed_at` (NOT NULL, default the migration time for
existing rows) records when a Project's access last changed. Every statement
that advances `projects.access_version` sets it too. The Inbox counts version
takes the newest of a person's item writes and of the access changes of the
Projects those items name, so it keeps moving forward when a grant change or
a visibility change hides or shows items (ADR 0017). Rollback drops the
column.

## Project Pages, Canvas and collaborative documents (000089)

One migration upgrades the schema at 000088. It combines Project
artifact identities and versions, canvas placements and views, Task attribution,
review annotations, native document blocks, structural sequence fences and
ACL-preserving editable Output documents. It also includes Overview designation,
managed blocks, contribution receipts, presentation captures and exact source
pins, shared Wiki draft metadata, Wiki publication timestamps and Page comments.

`project_attachment_scopes` unifies uploaded canvas assets with Project files for
the existing attachment-access boundary. Page and canvas edits use the Project
permission catalog introduced in 000086. The aggregate Output access view retains
that migration's restricted-Project rule and intersects it with current access
to captured sources. Page discussions use stable Page identity; version review
threads remain tied to their exact snapshot.

The up file preserves dependency order and historical Wiki publications. Existing
Outputs are not backfilled; new Outputs register through the normal presentation
service. The down file checks refusal guards before changing the schema and
retains protection for authored Pages, Wiki drafts, native blocks, saved library
state and Output-derived documents. Each direction executes atomically.

Databases at 000088 upgrade normally. The latest version is discovered
through `LatestVersion()`.

## Huddle AI default budget (000090)

Every Company without a `huddle_ai_budgets` row receives the $2 monthly
default (`20000000000` ticks); new Companies get the same row in their
creation transaction. Existing rows, including an owner's explicit zero, are
unchanged. The down file removes only default rows still at revision 1 that
never held a reservation. The latest version is discovered through `LatestVersion()`.

## Chat model settings (000091)

Direct and Task chats retain model/thinking choices per Conversation and Agent.
Direct chats copy Agent defaults when created; a new Task chat copies them at
its first Run or settings edit. Existing chats are backfilled from their most
recent matching Run brief, or Agent defaults if never run. Research-only model
overrides are excluded. Each Run continues to freeze its own brief; retries
retain it. `conversation_model_changes` records model transitions and their
Conversation stream anchors for durable chat notices; initial defaults and
thinking-only edits create no notice. The down migration removes preferences
and their change history without changing Run history.

## Artifact public sharing (000092)

Removes the Company public-sharing policy columns. Every Artifact owner/manager
can explicitly create a public link, including for native Project captures,
without approval or a Company setting. Existing visibility, public tokens and
personal/Conversation grants remain unchanged. Rollback restores the former
policy columns with their original disabled defaults; it does not alter tokens.
Deploy the server and frontend together after draining older server instances
that still query those removed columns.

## Agent custom skills (000093)

Adds four tables for custom Agent skills. `skill_imports`
holds one GitHub, ZIP or Markdown import for an Agent, who made it, its
source and commit, readiness and a 24-hour expiry. `skill_versions` holds
each imported skill's immutable metadata: name, description, source path,
SHA-256 content hash, size, file count and a JSON array file manifest.
`agent_skills` selects one version per Agent and skill name, with an
enabled flag and a positive revision. `run_skills` records which versions
each Run used. Every foreign key carries the Company. Rollback drops the
four tables.

## Skill file edits (000094)

`skill_imports` gains `edited` (default false): the record a saved file
edit publishes through, kept out of the import-rate count and the install
path. Rollback drops the column.

## Output review (000095)

An immutable Output can be reviewed without a Project:
`output_review_threads`, `output_review_comments` and
`output_review_operations` (the idempotent request keys). Access stays
`output_access`. Rollback drops the three tables. 000096 drops the thread
and comment tables (no rows are copied); from then on `review_threads` and
`review_comments` hold Output discussions.

## Artifact documents (000096)

A document belongs to an Output item: `output_documents` names the item's
Wiki page, and `canvas_document_imports` goes. `output_grants` gains the
`edit` permission, and `output_access` reads an Editor grant at level 2
(owner and `manage` stay 3, everything else 1); the rest of its 000089 body
is kept. `review_threads` takes an Output as its subject (either a Project
artifact version or an Output, never both) and an optional document
revision, so one discussion store serves both; 000095's thread and comment
tables go. A Wiki page belongs to an Output or a Project, never both.
Rollback refuses while an artifact document, an Output review thread or an
Editor grant exists (restore a backup), then restores the 000089
`output_access`, `canvas_document_imports` and 000095's tables.

## Activity on behalf of (000097)

Audit has three identities. `activity_events` gains
`on_behalf_of_user_id` and `run_id`: an Agent's row written in a Run names
the person the Run works for and the Run. `activity_events_on_behalf_of_check`
lets only an Agent row name a person, and `activity_events_run_check` names a
Run only with that person. Both columns are attribution on append-only
evidence, with no foreign key and no index. Rows written before 000097 keep
NULL in both. The CHECKs scan `activity_events` once under the ALTER's lock.

A Task a Run creates names its person and the creating Agent, so
`tasks_creator_exactly_one_check` becomes `tasks_creator_present_check` (at
least one creator). `task_run_agents` counts the creating Agent only when no
person created the Task, so a person's own Task still runs with the root
Agent. Rollback clears the creating Agent of every Task a person also
created, restores the single creator CHECK and the 000065 view body, and
drops both Activity columns and their CHECKs; every row stays, and an Agent's
row keeps its Agent.

## Daemon sessions (000098)

The daemon gets its own session. `auth_sessions.client` is `person`
(the default, so every existing session stays a person's) or `daemon`,
closed by `auth_sessions_client_check`. Both session inserts take the value
and a refresh copies it to the new row. Rollback ends every live daemon
session (`revocation_reason = 'downgrade'`) before it drops the column,
because without it a daemon session would read as its person's own.

## Delegate rule (000099)

The delegate rule in one place. A delegate (a Run, or the Huddle voice
assistant) acts with one person's access, its Authorizer's, and never more.
Two `LANGUAGE sql STABLE` functions state what it may do on a Project:
`delegate_permission_held` (the Authorizer holds the permission and read
there, never through the management bridge; a Task chat's answer stays in
that chat's Project; a group chat's whole audience reads the Project, a
term 000100 removes) and `run_permission_held`, the same rule for one Run
read from its own row (its Authorizer, its chat, its invocation's channel
and its Agent). The Agent argument was unused, kept for a per-Agent policy
that was later dropped; 000100 removes it too. Go binds
every permission. The enqueue fence, both claim
queries, the Run-proof reads (`ListRunReadableProjects`) and the voice reads
call them; no row changes.
Rollback drops both functions. 000100 replaces both functions (below).

## Runs use their Authorizer's access (000100)

A delegate uses its Authorizer's Project access, never more, and nothing
else narrows it: the person who tags an Agent
answers for what it shares with the chat. 000099 is never edited, so this
migration drops its two functions and `conversation_audience_reads` (000086,
which nothing calls any more) and creates the two again:
`delegate_permission_held` without the audience and the unused Agent
arguments (the Authorizer holds the permission and read there, never
through the management bridge, and a Task chat's answer stays in that
chat's Project), and `run_permission_held`, with the same arguments, which
no longer reads the Run's invocation. No row changes. Rollback drops both
and restores all three functions exactly as 000099 left them, which the
previous release's queries call.

## Groups (000101)

A Group is a named set of Team members in
one Company, flat and people only, that holds Project grants like a person.
`company_groups` holds the Group: its name (trimmed, 1–80 characters, unique
per Company ignoring case through `company_groups_name_key`), description
(at most 500 characters) and `version`, the revision a Group editor sends
back. `company_group_members` holds one row per Group and Team member; it
goes with the Company membership (foreign key to `company_members`) and with
the Group, and `company_group_members_user_idx` starts from the person, as
every access read does. A membership is added or deleted, never changed, so
its `created_at` is when the person was added. `project_group_grants` is
`project_grants` with a Group in place of a person: the same CHECK
(`project_group_grants_permission_check`, pinned with the person's against
`auth.ProjectPermissions()`), the same generated `read_permission` column and
deferred self foreign key, so a Group holds no permission without read, and
`project_group_grants_group_idx` for the Projects one Group reaches. Every
timestamp uses `clock_timestamp()`, so a membership written with a rejoin
passes the fence.

Access is a union. `project_permission_held` gains a Group arm: the person
is in a Group holding the permission there through a membership written
since their latest (re)join. `project_access` keeps its columns; `via` is
`grant` (their own fenced read row), else `group` (a fenced Group read
path, found by a `LATERAL ... LIMIT 1` lookup so the view stays one row per
person and Project), else `company`. `output_access`'s Company-visibility
arm also accepts a fenced Group read path on the source Project; the rest of
its 000096 body (the Editor level included) is kept. Every other view, the
delegate rule and the Run claim fence follow through them unchanged.
`project_access_paths` is a new `UNION ALL` view of every stored, fenced
path, one row per path and permission (`via` `grant` with no Group, or
`group` with its Group), for active members with enabled accounts in a
Company not being deleted; reach it with a correlated lookup naming the
Project and the person. The Inbox CHECKs gain the kind
`project_access_granted` and the entity `project`.

Rollback deletes the Inbox items only Groups write, restores both CHECKs,
restores `project_permission_held` and `project_access` as 000086 left them
and `output_access` as 000096 left them, then drops the view and the three
tables. A rejoin or a migration back up restores no membership.

## Remove "Review task results" (000102)

Removes `project.results.review` from the
Project catalog: nothing on the server checked it once result review was
retired. Every `project_grants` and `project_group_grants` row holding it
is deleted; every other row stays, `project.read` included. Each Project
that lost a row advances `access_version` and `access_changed_at`, as any
grant change does. Both CHECKs (`project_grants_permission_check`,
`project_group_grants_permission_check`) are redefined with the nine
remaining permissions, so the permission can no longer be stored;
`TestProjectGrantsCheckMatchesCatalog` pins the newest definition of each.
Rollback puts the permission back in both CHECKs and restores no row. The
latest version is discovered through `LatestVersion()`.


## Document Agents and inline writing (000106)

Adds creator-private document-thread Conversations and Agent reply attribution,
plus `document_writing_requests` to freeze the selected passage and content for
reviewable writing suggestions. The request timestamp is database-generated.
The Conversation access view keeps these chats subject to Project access.
This migration follows 000105 without changing the existing artifact or document
history migrations. Rollback removes the document-agent links and captured writing
requests and restores the previous Conversation access view; saved document
history remains intact.
## Company Whiteboards (000107)

One migration upgrades the 000106 schema with Company Boards, object commands,
CRDT note state, lifecycle fences and file references. Board files are ordinary
attachments referenced through `board_files`; `attachment_access` grants Board
readers each file and its preview. `board_files` is keyed by Company, Board and attachment; an item's frame reference is a
deferrable `ON DELETE SET NULL` key, so purging a frame keeps its children; and
indexes cover frame children, connector endpoints, tombstone retention and file
release.

Rollback first restores the prior attachment-access view, then removes Whiteboard
tables in dependency order. It deletes Board metadata; uploaded attachments
remain and ordinary attachment cleanup reclaims the unreferenced ones.
The migration integration suite exercises upgrade, rollback and reapplication,
including file foreign keys and preservation of existing Wiki/access behavior.

## Whiteboard collaboration (000108)

Binds a channel to a Board (`conversations.board_id`, one per Board) as the
Board's shared chat, whose people follow Board access. Board channels never
archive and may hold no one or more than 50 people; `conversation_readers`
reads them through joined members who can still read the Board.

Adds connector notation to `board_edges`: `line`, `start_marker`,
`end_marker`, `route`, `anchor_y` and end labels, with closed-vocabulary
checks. Defaults keep every existing connector as it was. The self-connector
check now admits a `message` route, which draws a sequence diagram's self
message.

Rollback deletes self messages, restores the plain self-connector check and
drops the notation columns, then deletes Board channels and restores the
000107 view and the 000070 discussion shape and identity triggers.

## Scheduler and Calendar Tasks (000109)

One additive upgrade from 000108 covers Scheduler retry receipts and
Calendar Task definitions, assignment acceptance and occurrences. Existing Tasks
remain accepted and unscheduled; date-only planning fields never start work.
History and authored content are retained. Downgrade refuses to discard linked
retries, cancellation receipts, Calendar definitions or assignment decisions.

## Run Wiki writes (000110)

Adds `run_wiki_writes(run_id, key, page_id, source_markdown)`: the pages the
founder's company-research Run published into the Company Wiki, by the Run's own
key. `(run_id, key)` is the key, `page_id` is unique, and the Run and page edges
cascade. A Run reaches only the pages it has a row for; `source_markdown` keeps
its `[[key]]` placeholders so a later call can link a page that did not exist
yet. Purely additive; the down file drops the table and the Run's ownership
record, not the published pages.

## Company initialization writes the Wiki (000111)

Drops `company_initializations.context_markdown` and `welcome_run_id`: the
research Run now saves linked Company Wiki pages instead of one Markdown answer,
and the CEO welcome Run is gone. `completed_at` is now set by the Run terminal
path when the research Run reaches its final state, and
`Company.initializationPending` means the receipt exists but research has not
been started. Deploy the server after draining older instances that still read
the dropped columns. Rollback restores the columns empty; the saved research
Markdown is not recoverable.

## Research Wiki authorship (000112)

Adds `wiki_revisions.author_agent_id`, the Agent that wrote a revision on
behalf of `author_user_id` (an attribution column, so no foreign key), and
`run_wiki_writes.last_draft_seq`, the page's draft sequence right after the
research Run's last publish. A later draft sequence means a person edited the
page, published or not, and the Run no longer replaces it. Purely additive and
nullable: rows written before it have no Agent author and are checked by
published revision alone. Rollback drops both columns.

## Whiteboard artifacts (000113)

A Whiteboard is now an Artifact made in a chat. `outputs.type` admits
`whiteboard`, and `boards` keeps only the live scene under its Output's ID
(`boards_output_fkey`, cascading): the title, owner, grants, chat sharing and
public link are the Output's, and `output_access` is the one access rule, which
`attachment_access` follows for Board files. The Board's own title, purpose,
visibility, public token, creator, archive and revision columns go, with
`board_member_access`, `board_agent_access`, `board_favorites`,
`board_create_receipts`, the `board_access` view and the library indexes.
Boards made before had no Output to belong to, so they are deleted.

A Board no longer has a chat of its own either: Board channels are deleted,
`conversations.board_id` is dropped, and the discussion shape and identity
triggers and `conversation_readers` return to their 000107 form.

Rollback deletes the Whiteboard Artifacts (their Boards cascade) and restores
000108's schema empty: the Board columns, access tables, views, indexes, the
chat column and its triggers. The deleted Boards and Board channels do not come
back.

## Commerce retirement (000116)

Stripe Commerce is no longer part of the server, CLI, MCP or frontend. Its
historical migrations, tables and generated schema models remain so upgrades
preserve provider records and past decisions. No active Commerce query or
provider worker uses them. This migration cancels pending Commerce approvals
and abandons pending continuations without resuming their Runs. Existing
terminal decisions and delivered continuations remain unchanged. The current
approval API serves only Company Plan actions.

Inbox requests for retired approvals are marked read and archived, retaining
their history without leaving an unread request for an unavailable action.

Rollback leaves the cancelled requests and abandoned continuations terminal;
reopening provider work requires a new review. This migration does not revoke
credentials or change resources in Stripe.

## Wiki indexes (000117)

Four indexes for Wiki lookups that had none: `wiki_revisions` and
`wiki_canvas_drafts` by `(company_id, collection_id)` where the Collection is
set (the `ON DELETE SET NULL` foreign keys of 000055 and 000089 and
`UnfileWikiCollectionPages` use that pair), `wiki_canvas_checkpoints` by
`(company_id, page_id, through_seq)` (`OutputDocumentCheckpoint`) and
`run_wiki_writes` by `(company_id, created_at, key)` (`ListRunWikiWrites`).
They are plain `CREATE INDEX`: the runner applies each file as one implicit
transaction, where `CONCURRENTLY` cannot run. Rollback drops them.

## Wiki search weights (000118)

`wiki_pages.search_vector` was the current Revision's title and body at one
weight; it is now the `simple` lexemes of the title (A), summary (B) and body
(C), backfilled in one `UPDATE` from each page's current revision, so
`ts_rank` puts a title hit above a body-only hit. The configuration stays
`simple` because the content is multilingual. A page link `[[<uuid>|label]]`
and an Artifact citation are reduced to their label before the body is
tokenized, so ids do not pollute the index; `SetWikiPageCurrentRevision` writes
the same expression on every publish. The migration also adds `list` to the
closed `run_wiki_reads.kind` vocabulary (the Agent `wiki_list` browse) and
redefines `run_wiki_reads_kind_check` and `run_wiki_reads_shape_check`: a list
row is shaped like a search row, with the collection browsed in `query` and the
pages covered in `result_count`. Rollback deletes the `list` rows, restores the
000064 constraints and rewrites the unweighted title-and-body vectors.
