# Run and Activity history cutover

The UUID API is a coordinated server, daemon, MCP and client change. Numeric
Run IDs and old Run/Activity cursors are rejected. This is an operator
procedure for adopting `feat/run-tracking-history`; no production database
or user workspace has been migrated by preparing this branch.

## Before changing the database

1. Stop new sends, claims, scheduled firing and other mutations. Drain active
   Runs and finish or reconcile executing Task merge jobs. Stop all old server
   and daemon writers. Migration 000014 refuses dispatched/running/waiting Runs
   and executing merge jobs; that guard does not replace draining traffic.
2. Take a restorable PostgreSQL backup and preserve each daemon workspace root,
   its signed sidecars, daemon identity and metadata signing material. Include
   unmerged work, authored main, Company Memory and unresolved merge receipts.
   An Output or uploaded Asset Version is not a workspace backup.
3. Rehearse the migration and restoration with a copy of the real retained
   data. Do not reset business tables or delete local work to bypass a failure.

## Database and retained local state

With old servers and daemons stopped, use the existing migration command;
it applies the embedded migrations without booting the API or its workers:

```bash
cd server
# APP_DATABASE_URL must already name the database selected for this cutover.
go run ./cmd/migrate -direction=up -timeout=15m
```

Keep all writers stopped while exporting the identity map and converting
local workspaces. After conversion, the upgraded server records initial
Activity baselines before starting workers or serving traffic.

Migration 000014 creates `run_identity_mappings(legacy_id, run_id)` over live
Runs and retained references, including orphans. It converts all typed Run
references while preserving their FK/non-FK policies. It rewrites owned Inbox
Run subjects, deep links and dedupe keys, but preserves user content and old
JSON evidence. No missing Run row is fabricated. Existing Run source revisions
remain null because today's Task/Conversation state cannot prove old input.

Export the map with a database account allowed to read the migration table:

```sql
SELECT COALESCE(jsonb_object_agg(legacy_id::text, run_id::text), '{}'::jsonb)
FROM run_identity_mappings;
```

Save the single JSON value as `run-identity-map.json` in a protected local
operator directory. Retain the table and export until every daemon and
retained workspace has been reconciled; the application does not use this map
as a numeric-ID compatibility API.

For each stopped daemon, use its existing identity, workspace root, and
configured health port (the same port used by that daemon):

```bash
zen daemon migrate-run-history \
  --mapping /path/to/run-identity-map.json \
  --daemon-id EXISTING_DAEMON_ID \
  --workspaces-root /path/to/existing/workspaces \
  --health-port EXISTING_HEALTH_PORT
```

Review the JSON change list, then repeat with `--apply`. The default is a
preview. The converter verifies signed metadata and the complete required map
before changing anything. It holds the daemon's health port and existing
workspace mutation lock, updates recorded Run IDs, and renames legacy
`boardly/task-<decimal>` branches to their mapped UUID names. It preserves
worktree paths, commits and unmerged files. Existing completed merge commits
receive UUID-named Git references under `refs/boardly/run-merges/`, preserving
merge review and retry recognition without rewriting main or commit messages.
The runtime accepts a converted receipt only while it remains reachable from
main. Conflicting receipts stop conversion. It can resume after a branch was
renamed but its metadata was not yet replaced. Unmapped IDs, conflicting
branches, active leases or untrusted sidecars abort conversion for operator
reconciliation; do not delete the affected workspace.

Run-derived legacy directories remain in place. Their signed metadata
continues to locate retained work; later Runs can allocate new UUID-derived
directories. Keep unresolved merge receipts for explicit reconciliation.

## Resume and rollback boundary

Deploy matching daemon/MCP and web/desktop builds. Restart upgraded clients
and refetch history. Realtime relay streams use the new
`ws:relay:run-uuid-v1:shard:<n>` namespace, so old numeric frames are not replayed;
old streams can expire under their existing retention policy. Old numeric
bookmarks outside stored owned Inbox links need to be replaced with mapped
UUID links. There is no mixed-version protocol compatibility mode.

Before resuming writes, verify row/reference counts, private Direct/Schedule
reads, a Task revision history, same-revision retries, and access to retained
unmerged work. Activity v1 contains safe before/after snapshots; legacy and
uncovered events use v0. Coverage remains `partial`, with `historyStartsAt`
null; startup baselines do not create missing past history.

Both cutover migrations deliberately refuse downgrade. Rollback requires
stopping all writers and restoring the corresponding database and daemon
workspace/metadata backups together. Once UUID writes have occurred, reverting
only binaries or reversing a subset of identities is not a safe rollback.
