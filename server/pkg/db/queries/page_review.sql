-- name: ListPageReviewThreads :many
SELECT jsonb_build_object('id',t.id,'versionId',t.artifact_version_id,'pageId',t.page_id,'anchor',t.anchor,'status',t.status,'revision',t.version,'createdAt',t.created_at,'agentRuns',COALESCE((SELECT jsonb_agg(jsonb_build_object('agentId',ac.agent_id,'agentName',aa.name,'runId',ar.id,'status',ar.status)) FROM conversations ac JOIN agents aa ON aa.id=ac.agent_id JOIN LATERAL (SELECT id,status FROM runs WHERE conversation_id=ac.id ORDER BY created_at DESC,id DESC LIMIT 1) ar ON true WHERE ac.document_thread_id=t.id),'[]'::jsonb),'comments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'body',CASE WHEN c.deleted_at IS NULL THEN c.body ELSE '' END,'revision',c.version,'authorId',c.author_user_id,'authorAgentId',c.author_agent_id,'runId',c.agent_run_id,'authorName',COALESCE(a.name,u.display_name,'Former teammate'),'createdAt',c.created_at,'editedAt',c.edited_at,'deleted',c.deleted_at IS NOT NULL) ORDER BY c.created_at,c.id) FROM review_comments c LEFT JOIN users u ON u.id=c.author_user_id LEFT JOIN agents a ON a.id=c.author_agent_id WHERE c.thread_id=t.id),'[]'::jsonb)) AS payload
FROM review_threads t WHERE t.company_id=$1 AND t.page_id=$2 ORDER BY t.created_at,t.id LIMIT 100;

-- name: CountPageReviewThreads :one
SELECT count(*) FROM review_threads WHERE company_id=$1 AND page_id=$2;

-- name: GetPageReviewThread :one
SELECT t.* FROM review_threads t WHERE t.company_id=$1 AND t.page_id=$2 AND t.id=$3 FOR UPDATE;

-- name: UpdatePageReviewComment :execrows
UPDATE review_comments SET body=sqlc.arg(body), version=review_comments.version+1,
 edited_at=now(), deleted_at=CASE WHEN sqlc.arg(remove)::boolean THEN now() ELSE NULL END
WHERE review_comments.company_id=sqlc.arg(company_id) AND thread_id=sqlc.arg(thread_id) AND EXISTS(SELECT 1 FROM review_threads t WHERE t.id=review_comments.thread_id AND t.page_id=sqlc.arg(page_id)::uuid)
 AND review_comments.id=sqlc.arg(id) AND author_agent_id IS NULL AND author_user_id=sqlc.arg(actor_user_id) AND review_comments.version=sqlc.arg(expected_revision) AND deleted_at IS NULL;

-- name: ResolvePageReviewThread :execrows
UPDATE review_threads SET status=sqlc.arg(status),version=version+1,
 resolved_at=CASE WHEN sqlc.arg(status)::text='resolved' THEN now() ELSE NULL END,
 resolved_by_user_id=CASE WHEN sqlc.arg(status)::text='resolved' THEN sqlc.arg(actor_user_id)::uuid ELSE NULL END
WHERE company_id=sqlc.arg(company_id)::uuid AND page_id=sqlc.arg(page_id)::uuid AND id=sqlc.arg(id)::uuid AND version=sqlc.arg(expected_revision)::bigint;

-- name: GetPageReviewOperation :one
SELECT fingerprint,result_id FROM page_review_operations WHERE page_id=$1 AND actor_user_id=$2 AND request_key=$3;

-- name: CreatePageReviewOperation :exec
INSERT INTO page_review_operations(company_id,page_id,actor_user_id,request_key,fingerprint,result_id) VALUES($1,$2,$3,$4,$5,$6);

-- name: CreatePageReviewThread :one
INSERT INTO review_threads(company_id,page_id,project_id,anchor,created_by_user_id)
SELECT p.company_id,p.id,p.project_id,sqlc.arg(anchor)::jsonb,sqlc.arg(created_by_user_id)::uuid
FROM wiki_pages p WHERE p.company_id=sqlc.arg(company_id)::uuid AND p.id=sqlc.arg(page_id)::uuid AND p.artifact_type='page'
RETURNING id;
