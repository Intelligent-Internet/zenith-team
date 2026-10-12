-- name: GetOutputReviewSource :one
SELECT o.id, o.title, o.summary, o.run_id, o.created_at,
 COALESCE(u.display_name, 'Former teammate')::text AS creator_name,
 a.access_level
FROM outputs o
JOIN output_access a ON a.company_id=o.company_id AND a.output_id=o.id AND a.user_id=sqlc.arg(actor_user_id)::uuid
LEFT JOIN users u ON u.id=o.owner_user_id
WHERE o.company_id=sqlc.arg(company_id)::uuid AND o.id=sqlc.arg(output_id)::uuid;

-- name: ListOutputReviewThreads :many
SELECT jsonb_build_object('id',t.id,'versionId',COALESCE(t.document_revision_id,t.output_id),'pageId',t.page_id,'anchor',t.anchor,'status',t.status,'revision',t.version,'createdAt',t.created_at,
 'comments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'body',CASE WHEN c.deleted_at IS NULL THEN c.body ELSE '' END,'revision',c.version,'authorId',c.author_user_id,'authorAgentId',c.author_agent_id,'runId',c.agent_run_id,'authorName',COALESCE(a.name,u.display_name,'Former teammate'),'createdAt',c.created_at,'editedAt',c.edited_at,'deleted',c.deleted_at IS NOT NULL) ORDER BY c.created_at,c.id)
 FROM review_comments c LEFT JOIN users u ON u.id=c.author_user_id LEFT JOIN agents a ON a.id=c.author_agent_id WHERE c.thread_id=t.id),'[]'::jsonb)) AS payload
FROM review_threads t WHERE t.company_id=$1 AND t.output_id=$2 ORDER BY t.created_at,t.id;

-- name: CountOutputReviewThreads :one
SELECT count(*) FROM review_threads WHERE company_id=$1 AND output_id=$2;

-- name: CreateOutputReviewThread :one
INSERT INTO review_threads(company_id,output_id,anchor,created_by_user_id,page_id,document_revision_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id;

-- name: GetOutputReviewThread :one
SELECT * FROM review_threads WHERE company_id=$1 AND output_id=$2 AND id=$3;

-- name: CountOutputReviewComments :one
SELECT count(*) FROM review_comments WHERE thread_id=$1;

-- name: CreateOutputReviewComment :one
INSERT INTO review_comments(company_id,thread_id,author_user_id,body) VALUES($1,$2,$3,$4) RETURNING id;

-- name: UpdateOutputReviewComment :execrows
UPDATE review_comments SET body=sqlc.arg(body), version=review_comments.version+1,
 edited_at=now(), deleted_at=CASE WHEN sqlc.arg(remove)::boolean THEN now() ELSE NULL END
WHERE review_comments.company_id=sqlc.arg(company_id) AND thread_id=sqlc.arg(thread_id) AND EXISTS(SELECT 1 FROM review_threads t WHERE t.id=review_comments.thread_id AND t.output_id=sqlc.arg(output_id)::uuid)
 AND review_comments.id=sqlc.arg(id) AND author_agent_id IS NULL AND author_user_id=sqlc.arg(actor_user_id) AND review_comments.version=sqlc.arg(expected_revision) AND deleted_at IS NULL;

-- name: ResolveOutputReviewThread :execrows
UPDATE review_threads SET status=sqlc.arg(status),version=version+1,
 resolved_at=CASE WHEN sqlc.arg(status)::text='resolved' THEN now() ELSE NULL END,
 resolved_by_user_id=CASE WHEN sqlc.arg(status)::text='resolved' THEN sqlc.arg(actor_user_id)::uuid ELSE NULL END
WHERE company_id=sqlc.arg(company_id)::uuid AND output_id=sqlc.arg(output_id)::uuid AND id=sqlc.arg(id)::uuid AND version=sqlc.arg(expected_revision)::bigint;

-- name: GetOutputReviewOperation :one
SELECT fingerprint,result_id FROM output_review_operations WHERE output_id=$1 AND actor_user_id=$2 AND request_key=$3;

-- name: CreateOutputReviewOperation :exec
INSERT INTO output_review_operations(company_id,output_id,actor_user_id,request_key,fingerprint,result_id) VALUES($1,$2,$3,$4,$5,$6);

-- name: OutputReviewAnchorSource :one
SELECT av.content_type FROM output_items i
JOIN asset_versions av ON av.company_id=i.company_id AND av.id=i.asset_version_id
WHERE i.company_id=sqlc.arg(company_id)::uuid AND i.output_id=sqlc.arg(output_id)::uuid
 AND i.id=sqlc.arg(output_item_id)::uuid AND av.id=sqlc.arg(asset_version_id)::uuid;
