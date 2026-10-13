-- name: GetCompanyInitialization :one
SELECT * FROM company_initializations WHERE company_id = $1;

-- name: ListPendingCompanyInitializations :many
-- Receipts whose research was started and not yet completed: the recovery set.
SELECT company_id, created_by_user_id FROM company_initializations
WHERE completed_at IS NULL AND research_run_id IS NOT NULL
AND company_id > sqlc.arg(after_company_id)::uuid
ORDER BY company_id LIMIT sqlc.arg(row_limit)::int;

-- name: IsConversationInitializationPending :one
-- The Conversation is held only until the research starts; while the research
-- Run works, messages queue behind it like behind any other Run.
SELECT EXISTS (
 SELECT 1 FROM company_initializations
 WHERE company_id = sqlc.arg(company_id)::uuid
 AND conversation_id = sqlc.arg(conversation_id)::uuid
 AND research_run_id IS NULL AND completed_at IS NULL
)::boolean;

-- name: GetInitializationTarget :one
SELECT a.id AS agent_id, b.runtime_id, r.status AS runtime_status
FROM agents a
JOIN agent_runtime_bindings b ON b.agent_id = a.id AND b.user_id = sqlc.arg(actor_user_id)::uuid
JOIN runtimes r ON r.id = b.runtime_id AND r.owner_user_id = b.user_id AND r.removed_at IS NULL
JOIN conversations c ON c.agent_id = a.id AND c.company_id = a.company_id
WHERE a.company_id = sqlc.arg(company_id)::uuid AND a.reports_to_agent_id IS NULL AND a.archived_at IS NULL
AND c.id = sqlc.arg(conversation_id)::uuid AND c.kind = 'direct' AND c.archived_at IS NULL
AND c.created_by_user_id = sqlc.arg(actor_user_id)::uuid;

-- name: InsertCompanyInitialization :exec
INSERT INTO company_initializations (company_id, created_by_user_id, conversation_id, research_run_id)
VALUES ($1, $2, $3, $4);

-- name: SetInitializationResearchRun :exec
-- A new research Run starts a new attempt, so the earlier completion no longer holds.
UPDATE company_initializations SET research_run_id = $2, completed_at = NULL, updated_at = now() WHERE company_id = $1;

-- name: GetInitializationRun :one
-- Follow automatic retries; a failed parent with a live child is still in progress.
WITH RECURSIVE attempts AS (
 SELECT r.id, r.status, r.final_answer_item_key, r.runtime_id, r.created_at FROM runs r WHERE r.id = $1
 UNION ALL
 SELECT r.id, r.status, r.final_answer_item_key, r.runtime_id, r.created_at FROM runs r JOIN attempts p ON r.parent_run_id = p.id
)
SELECT a.id, a.status, COALESCE(i.content, '')::text AS output,
 COALESCE(rt.status, 'offline')::text AS runtime_status
FROM attempts a
LEFT JOIN runtimes rt ON rt.id = a.runtime_id AND rt.removed_at IS NULL
LEFT JOIN conversation_items i ON i.run_id = a.id AND i.item_key = a.final_answer_item_key AND i.type = 'agent_text'
ORDER BY a.created_at DESC, a.id DESC LIMIT 1;

-- name: CompleteCompanyInitializationForRun :one
-- The terminal hook: the Run is the receipt's research Run or one of its
-- automatic-retry descendants. Marks the receipt complete and returns it, or
-- no row when the Run is not part of the research lineage.
WITH RECURSIVE ancestry AS (
 SELECT r.id, r.parent_run_id FROM runs r WHERE r.id = sqlc.arg(run_id)::uuid
 UNION ALL
 SELECT r.id, r.parent_run_id FROM runs r JOIN ancestry a ON r.id = a.parent_run_id
)
UPDATE company_initializations i
SET completed_at = now(), updated_at = now()
WHERE i.company_id = sqlc.arg(company_id)::uuid
  AND i.research_run_id IN (SELECT id FROM ancestry)
RETURNING i.research_run_id;

-- name: IsInitializationResearchRun :one
-- Whether the Run belongs to the receipt's current research lineage (the
-- research Run or an automatic-retry descendant), so its terminal notice has
-- dedicated copy.
WITH RECURSIVE ancestry AS (
 SELECT r.id, r.parent_run_id FROM runs r WHERE r.id = sqlc.arg(run_id)::uuid
 UNION ALL
 SELECT r.id, r.parent_run_id FROM runs r JOIN ancestry a ON r.id = a.parent_run_id
)
SELECT EXISTS (
 SELECT 1 FROM company_initializations i
 WHERE i.company_id = sqlc.arg(company_id)::uuid AND i.research_run_id IN (SELECT id FROM ancestry)
)::boolean AS research;

-- name: CountInitializationWikiWrites :one
-- Pages the Company's onboarding research wrote, across every attempt.
SELECT count(*)::int AS pages FROM run_wiki_writes w
WHERE w.company_id = sqlc.arg(company_id)::uuid;

-- name: GetInitializationOverviewPage :one
-- The hub page the Company's onboarding research wrote under the given key.
SELECT w.page_id FROM run_wiki_writes w
WHERE w.company_id = sqlc.arg(company_id)::uuid AND w.key = sqlc.arg(key)::text
ORDER BY w.created_at DESC LIMIT 1;

-- name: CompleteCompanyInitialization :exec
UPDATE company_initializations SET completed_at = COALESCE(completed_at, now()), updated_at = now() WHERE company_id = $1;
