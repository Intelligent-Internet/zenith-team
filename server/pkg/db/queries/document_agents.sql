-- name: EnsureDocumentAgentConversation :one
INSERT INTO conversations(company_id,kind,project_id,document_thread_id,agent_id,created_by_user_id,title,request_key)
SELECT t.company_id,'direct',t.project_id,t.id,a.id,sqlc.arg(actor_user_id)::uuid,p.title,gen_random_uuid()
FROM review_threads t JOIN wiki_pages p ON p.company_id=t.company_id AND p.id=t.page_id AND p.status='active'
JOIN agents a ON a.company_id=t.company_id AND a.id=sqlc.arg(agent_id)::uuid AND a.archived_at IS NULL
WHERE t.company_id=sqlc.arg(company_id)::uuid AND t.id=sqlc.arg(thread_id)::uuid AND t.project_id IS NOT NULL AND t.status='open'
ON CONFLICT(document_thread_id,agent_id,created_by_user_id) WHERE document_thread_id IS NOT NULL
DO UPDATE SET title=EXCLUDED.title
RETURNING *;

-- name: GetDocumentAgentContext :one
SELECT c.document_thread_id AS thread_id,c.project_id,p.id AS page_id,p.title,t.anchor,
 COALESCE((SELECT string_agg(concat(COALESCE(a.name,u.display_name,'Former teammate'),': ',r.body),E'\n' ORDER BY r.created_at,r.id)
 FROM (SELECT * FROM review_comments WHERE thread_id=t.id AND deleted_at IS NULL ORDER BY created_at DESC,id DESC LIMIT 30) r
 LEFT JOIN agents a ON a.id=r.author_agent_id LEFT JOIN users u ON u.id=r.author_user_id),'')::text AS discussion
FROM conversations c JOIN review_threads t ON t.company_id=c.company_id AND t.id=c.document_thread_id
JOIN wiki_pages p ON p.company_id=t.company_id AND p.id=t.page_id AND p.status='active'
WHERE c.company_id=sqlc.arg(company_id)::uuid AND c.id=sqlc.arg(conversation_id)::uuid;

-- name: InsertDocumentAgentReply :exec
INSERT INTO review_comments(company_id,project_id,thread_id,body,author_user_id,author_agent_id,agent_run_id)
SELECT r.company_id,c.project_id,c.document_thread_id,sqlc.arg(body)::text,r.execution_authorized_by_user_id,r.agent_id,r.id
FROM runs r JOIN conversations c ON c.id=r.conversation_id AND c.company_id=r.company_id
WHERE r.id=sqlc.arg(run_id)::uuid AND c.document_thread_id IS NOT NULL
ON CONFLICT(agent_run_id) WHERE agent_run_id IS NOT NULL DO NOTHING;

-- name: GetDocumentThreadForInvocation :one
SELECT * FROM review_threads WHERE company_id=$1 AND id=$2;

-- name: LockDocumentAgentConversations :many
SELECT c.id FROM conversations c
WHERE c.company_id=$1 AND c.document_thread_id=$2 AND c.created_by_user_id=$3 AND c.agent_id=ANY(sqlc.arg(agent_ids)::uuid[])
ORDER BY c.id FOR UPDATE;

-- name: SaveDocumentWritingRequest :exec
INSERT INTO document_writing_requests(company_id,run_id,page_id,request_key,target,before_content,sequence)
VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: GetDocumentWritingRequest :one
WITH RECURSIVE attempts AS (
 SELECT r.id,r.parent_run_id FROM runs r WHERE r.company_id=sqlc.arg(company_id)::uuid AND r.id=sqlc.arg(run_id)::uuid
 UNION ALL
 SELECT r.id,r.parent_run_id FROM runs r JOIN attempts a ON r.id=a.parent_run_id WHERE r.company_id=sqlc.arg(company_id)::uuid
)
SELECT w.* FROM document_writing_requests w JOIN attempts a ON a.id=w.run_id WHERE w.company_id=sqlc.arg(company_id)::uuid LIMIT 1;

-- name: GetDocumentAgentRun :one
SELECT c.project_id,c.document_thread_id,c.id AS conversation_id,t.page_id
FROM runs r JOIN conversations c ON c.id=r.conversation_id AND c.company_id=r.company_id
JOIN review_threads t ON t.id=c.document_thread_id AND t.company_id=c.company_id
JOIN project_access access ON access.company_id=c.company_id AND access.project_id=c.project_id AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE r.company_id=sqlc.arg(company_id)::uuid AND r.id=sqlc.arg(run_id)::uuid
 AND r.execution_authorized_by_user_id=sqlc.arg(actor_user_id)::uuid AND c.document_thread_id IS NOT NULL
 AND COALESCE(r.context->'executionProfile'->>'documentWriting', 'false') <> 'true';
