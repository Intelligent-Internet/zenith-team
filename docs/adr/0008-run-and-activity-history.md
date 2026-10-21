# ADR 0008: Record entity changes in their transaction; keep Run identity separate from revision

Status: Accepted. Date: 2026-09-13.

## Context

Zenith Team needs to explain exact Task/Conversation changes and the state
that a Run executed. The previous Activity sink ran after commit on a
best-effort Bus. A failed insert loses evidence, nested payloads can lose
subject identity, and current-row joins can change historical labels.
Run lifecycle publication additionally re-read the projection after mutation.
These are concrete correctness gaps, independent of whether Run IDs are
numeric or UUID. Runs also need an explicit type (Task, direct chat or
Schedule) and the revision of the source state they ran.

Task and Conversation already store `version` and expose `revision`.
Those counters do not retain old state. A Run has an execution generation,
retry attempt and transcript sequence. A Run revision describes the source
state executed, so it needs no separate lifecycle version.

## Decision

Deepen the existing Activity and execution modules. Capture safe owner-state
before/after snapshots, explicit subject identity, authoritative revision and
verified actor inside the owning mutation transaction. Persist the Activity
record with the entity through the existing transaction seam. Publish the
captured domain projection and eligible Activity announcement after commit.
PostgreSQL remains the adapter; live entity rows remain authoritative.

Activity is the Company action log, with explicit subject identity and actor.
Use the existing domain `action` and a versioned `details` envelope; omit the
proposed `change_type`, `record_type` and `previous_revision` columns. Query
previous retained revisions by Company/type/ID/revision; the exact preceding
state and revision are already in `details.before`. Activity revision means
only the changed Task/Conversation's revision. Run actions use null Activity
revision and retain frozen source revision inside their Run snapshots.

Use a unique entity-revision index for captured Task/Conversation changes and
baselines. A controlled migration wraps legacy evidence in a version-0 details
envelope with the original payload unchanged; no historical state is invented. Explicit
`*.history_baselined` actions identify observed starting state. Freeze visibility
and descriptive attribution without widening private Conversation access.

Activity records Company-wide actions, not a causal Run log. Remove the
proposed per-Run Activity endpoint and dedicated Run attribution from the
target contract. A Run is selected through `entity_type=run` and `entity_id`;
its actions do not include unrelated subject changes it caused. Activity has
no dedicated Run column. Agent Run history remains the Run list.

Use a DB-generated UUID Run PK, immutable server-derived `run_type`, and
one immutable `runs.revision`: Task revision for a Task Run, Conversation
revision for a direct chat Run. Keep the source counters independent, and
freeze source identity with input. Do not add `runs.version`, `task_revision`
or `conversation_revision`. Preserve generation, attempts and the one
Conversation item sequence. Include explicit `schedule` Run type for
existing Schedule Runs with their current privacy and execution policy. UUID adoption needs
a complete retained-reference and server/daemon/client cutover.

The deletion test removes post-commit reconstruction and payload identity
guessing, concentrating correctness at the mutation seam. Depth improves
locality for capture and leverage for history readers; the existing mutation
interface is the test surface. No generic history repository is introduced.

## Relationship to accepted decisions

The earlier decisions named here (ADR 0001 to 0006) are not published in
this repository; each is described in a few words.

ADR 0006 put every reaction on one event bus, where a topic owns its name and
payload and the consumer decides who sees it. Its Activity log was a
post-commit subscriber on that bus, and it assumed that adjacent after-only
events were enough for change history. This decision replaces only that
post-commit Activity writer and that assumption, for the covered mutation
families. ADR 0006's domain topics, post-commit bus reactions and
consumer-owned routing stay.

ADR 0004 made realtime events carry the committed row in the owning domain's
public projection. Those payloads stay; the new history snapshot format is a
separate read contract.

ADR 0005 made a Conversation one item stream in which a Run labels the rows
it wrote, and gave Runs numeric (`bigserial`) ids. The UUID direction here
replaces those numeric ids; the single Conversation stream and its
attribution retention stay.

ADRs 0001 and 0003 made the enqueue freeze the prompt and the Agent's brief
into the Run context. They still own frozen execution input.

## Trade-offs and alternatives

Transactional capture adds storage/write cost and requires covering every
version-changing path, including silent Schedule updates. That cost buys a
mutation/history atomicity guarantee. Baseline and retention semantics are
necessary because old evidence cannot be made complete retroactively.

Keeping the current sink and adding a revision index cannot recover a lost
write or before state. A durable outbox could preserve post-commit event
delivery but would still need transactional snapshot capture plus a second
persisted delivery lifecycle; history alone does not justify that machinery.
Database triggers cannot naturally express verified actor, private scope
and safe domain snapshots, and conflict with the repository's schema doctrine.

## Amendment: schema version column (2026-09-25)

SQL must not filter inside the `details` JSON, for example on
`details->>'schemaVersion'`. The envelope version is also stored in
`activity_events.schema_version`
(`integer NOT NULL DEFAULT 0`, CHECK 0 or 1), and the startup baseline filters
on that column. The envelope keeps `schemaVersion` because clients read it as
the details union tag; each Activity writer sets both from one constant. The
rest of this decision stands: no `change_type`, `record_type` or
`previous_revision` columns.
