-- name: LockRunForWikiWrite :one
-- The Run's standing to write the Wiki, read under a share lock so the Run
-- cannot reach a terminal state while its pages are being written. source is
-- the frozen execution profile's source; authorized says the Run works for the
-- actor. No row means the Run is not in the Company. The server enforces the
-- Agent's wiki switch and the source itself: the daemon's tool allowlist is
-- not authority.
SELECT runs.status::text AS status, runs.agent_id,
  COALESCE(runs.context->'executionProfile'->>'source', '')::text AS source,
  (runs.execution_authorized_by_user_id IS NOT DISTINCT FROM sqlc.arg(actor_user_id)::uuid)::boolean AS authorized,
  (COALESCE(agents.settings->'capabilities'->'disabledBuiltIns', '[]'::jsonb) @> '"wiki"'::jsonb)::boolean AS disabled
FROM runs
JOIN agents ON agents.company_id = runs.company_id AND agents.id = runs.agent_id
WHERE runs.company_id = sqlc.arg(company_id)::uuid AND runs.id = sqlc.arg(run_id)::uuid
FOR SHARE OF runs;

-- name: ListRunWikiWrites :many
-- Every page the Company's onboarding research has written, oldest first, with
-- its title, status and current revision now. Only a company-research Run ever
-- writes these rows and a Company has one research receipt, so every attempt
-- (an automatic retry child or the founder's manual retry) owns the pages the
-- earlier attempts wrote and updates them instead of writing a second copy.
SELECT w.key, w.page_id, w.source_markdown, w.last_revision_id, w.last_draft_seq,
  p.title, p.status, p.current_revision_id
FROM run_wiki_writes w
JOIN company_wiki_pages p ON p.company_id = w.company_id AND p.id = w.page_id
WHERE w.company_id = sqlc.arg(company_id)::uuid
ORDER BY w.created_at, w.key;

-- name: InsertRunWikiWrite :exec
INSERT INTO run_wiki_writes (run_id, company_id, key, page_id)
VALUES (sqlc.arg(run_id)::uuid, sqlc.arg(company_id)::uuid, sqlc.arg(key)::text, sqlc.arg(page_id)::uuid);

-- name: SetRunWikiWriteSaved :exec
-- Records what the research just wrote to a page: its markdown source, the
-- published revision it produced and the draft sequence that publish left. The
-- page is the handle: it belongs to one row, whichever attempt created it.
UPDATE run_wiki_writes
SET source_markdown = sqlc.arg(source_markdown)::text, last_revision_id = sqlc.narg(last_revision_id)::uuid,
  last_draft_seq = sqlc.narg(last_draft_seq)::bigint
WHERE company_id = sqlc.arg(company_id)::uuid AND page_id = sqlc.arg(page_id)::uuid;

-- name: SetWikiRevisionAuthorAgent :exec
-- Names the Agent that wrote a revision on behalf of its author_user_id.
UPDATE wiki_revisions SET author_agent_id = sqlc.arg(agent_id)::uuid
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(revision_id)::uuid;

-- name: LockWikiResearchWrites :exec
-- Serializes the Company's research writes with one another and with nothing
-- else. The 40-page cap and key ownership are read-then-insert over every
-- attempt of the Company's research (a retry child shares its parent's pages),
-- so two concurrent calls must not both read the same total. Nothing but
-- AgentWriteService takes it, always right after the member lock, so it cannot
-- join a cycle with an editor's locks.
SELECT pg_advisory_xact_lock(hashtextextended(
    'boardly/wiki-research-writes/v1/' || sqlc.arg(company_id)::uuid::text,
    0
));

-- name: GetWikiCollectionByName :one
-- No row lock: the caller holds LockWikiCollectionsShared, which excludes a
-- delete; a concurrent rename only decides which side of this write it falls on.
SELECT * FROM wiki_collections
WHERE company_id = sqlc.arg(company_id)::uuid AND lower(name) = lower(sqlc.arg(name)::text);
