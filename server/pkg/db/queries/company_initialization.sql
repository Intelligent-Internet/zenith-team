-- name: GetCompanyInitialization :one
SELECT * FROM company_initializations WHERE company_id = $1;

-- name: ListPendingCompanyInitializations :many
SELECT company_id, created_by_user_id FROM company_initializations
WHERE completed_at IS NULL AND (research_run_id IS NOT NULL OR welcome_run_id IS NOT NULL)
AND company_id > sqlc.arg(after_company_id)::uuid
ORDER BY company_id LIMIT sqlc.arg(row_limit)::int;

-- name: IsConversationInitializationPending :one
SELECT EXISTS (
 SELECT 1 FROM company_initializations
 WHERE company_id = sqlc.arg(company_id)::uuid
 AND conversation_id = sqlc.arg(conversation_id)::uuid AND completed_at IS NULL
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

-- name: SaveInitializationContext :exec
UPDATE company_initializations SET context_markdown = $2, updated_at = now() WHERE company_id = $1;

-- name: SetInitializationResearchRun :exec
UPDATE company_initializations SET research_run_id = $2, updated_at = now() WHERE company_id = $1;

-- name: SetInitializationWelcomeRun :exec
UPDATE company_initializations SET welcome_run_id = $2, updated_at = now() WHERE company_id = $1;

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

-- name: CompleteCompanyInitialization :exec
UPDATE company_initializations SET completed_at = COALESCE(completed_at, now()), updated_at = now() WHERE company_id = $1;
