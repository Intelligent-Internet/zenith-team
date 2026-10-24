# Migrations

The PostgreSQL schema (`*.up.sql` / `*.down.sql`, golang-migrate format),
embedded into the binaries via `embed.go` and doubling as sqlc's schema input
(`sqlc.yaml` points `schema:` here). `embed.go` also exposes
`LatestVersion()`, from which `internal/db` readiness derives its expected
schema version, so adding a migration can never silently desynchronize the
readiness fence.

## The baseline

The schema starts from eight domain files, applied in order. Each holds the
final shape of its tables: columns with their types and defaults, primary
keys, unique keys, CHECK constraints, foreign keys and indexes, then the views,
functions and triggers that belong to the domain. A fresh database has
nothing to backfill or fix, so the only data statements are seeds of
reference rows, written right after their table: the `model_prices` rates in
`000008_activity`.

| File | What it holds |
|---|---|
| `000001_identity` | People, sessions, sign-in and verification flows, companies, membership, invitations and groups |
| `000002_agents` | Agents, runtimes, runtime bindings, skills, plugins and connectors |
| `000003_work` | Projects and their grants and files, tasks, approvals, questions, schedules and calendars |
| `000004_conversations` | Conversations, channels, messages and their state, discussion integrity checks, huddles and notes |
| `000005_runs` | Runs, run tokens and usage, outputs, deliverables, assets, attachments and reviews |
| `000006_content` | Wiki, boards, canvases, documents, Artifact access, previews and captures |
| `000007_mail_and_social` | Company mailboxes and social publishing |
| `000008_activity` | The activity feed, the Inbox, analytics budgets and model prices, crash reports |

A foreign key points back to a table created earlier whenever it can. When a
table links to one in a later file, that file adds the link with `ALTER TABLE`
right after creating its own tables, under the heading "Links to these tables
from tables created earlier". The `conversation_readers` view is defined in
`000006_content`, after Artifact access and Page review tables exist.

Each down file drops what its up file created, newest first, so children always
go before the tables they reference. Rolling the whole ladder back leaves an
empty database. Rolling back drops business data, so
`cmd/migrate -direction=down` refuses to run without `-confirm-down`.

A database created from the earlier ladder (versions above 8) is not migrated
across: recreate it. For local development that is `make infra-reset`, then
start the server.

## Adding a migration

For this greenfield implementation, edit the owning domain baseline directly.
Define each table in its final form, with its constraints and indexes; no
backfills, historical repair steps or compatibility aliases are needed. Add a
new numbered up/down pair only when a change cannot fit the domain baseline.
Keep down files in reverse table order and put cross-domain foreign keys in
the file that creates the later target. Regenerate sqlc with `make go-sqlc`;
`make go-sqlc-check` detects drift.

`migrations/embed_test.go` keeps the set honest: versions are contiguous and
paired, the baseline is schema only, every down file drops its tables newest
first, and procedural objects stay limited to the approved list below.

## Schema doctrine

- DB-generated ids via `gen_random_uuid()`; sqlc maps UUID columns directly to
  `google/uuid` and uses `pgtype` for nullable scalar and timestamp columns.
- **Real FOREIGN KEY edges on structural ownership**: `ON DELETE CASCADE`
  for owned children and `ON DELETE SET NULL` for optional pointers.
  Attribution and polymorphic actor columns carry no FK; this includes the
  creator attribution on immutable fact rows. `run_usage` is entirely FK-free
  by design: usage history must survive deletion of the runs, agents and
  companies it measured.
- The circular task ↔ conversation pair is `DEFERRABLE INITIALLY DEFERRED`, so
  the pair can be created, and deleted, in one transaction.
- A status column is the state machine; partial unique indexes are the
  at-most-one guards (one live root agent per company, one conversation per
  task, and so on).
- `text` + CHECK constraints for enums, ranges and structural cross-column
  invariants that the database can enforce. jsonb columns get object-shape
  checks. `created_at` on every table, `updated_at` wherever rows mutate.
- No general stored procedures or trigger-driven product writes; seeding is
  application code. The explicit exceptions are the discussion integrity
  checks in `000004_conversations` (deferred detail and grant shape, immutable
  discussion identity and receipts, item shape, scoped collaboration shape,
  the channel revision fences) and the guard in `000005_runs` that rejects Runs
  on discussion Conversations. These trigger functions validate or reject
  writes; they never materialize product rows, publish events or own
  lifecycle transitions. Three `LANGUAGE sql STABLE` predicates each state one
  access rule (ADR 0017): `project_permission_held` in `000003_work`,
  `delegate_permission_held` in `000004_conversations` and
  `run_permission_held` in `000005_runs`.

## Runners

The server applies the migrations automatically at startup: `db.MigrateUp`
(`internal/db/migrate.go`), called from `run()` in `cmd/server/app.go` before
serving. A failed migration exits non-zero, so the previous deployment keeps
serving. Concurrent replicas serialize through an advisory-lock gate
(completed try-lock polling around golang-migrate's blocking lock, so a
statement timeout cannot strand a waiter) on a simple-protocol connection
with a bounded `statement_timeout`. `cmd/migrate` drives the same functions
for operators without booting the API.
