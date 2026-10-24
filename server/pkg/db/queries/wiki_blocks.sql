-- name: ReserveWikiBlockIDs :many
INSERT INTO wiki_block_ids(company_id,page_id)
SELECT sqlc.arg(company_id)::uuid,sqlc.arg(page_id)::uuid FROM generate_series(1,sqlc.arg(count)::integer)
RETURNING id;

-- name: CountWikiBlockIDs :one
SELECT count(*) FROM wiki_block_ids WHERE company_id=$1 AND page_id=$2;

-- name: ValidateWikiBlockIDs :one
SELECT count(*) FROM wiki_block_ids WHERE company_id=$1 AND page_id=$2 AND id=ANY(sqlc.arg(ids)::uuid[]);

-- name: LockWikiDraft :one
SELECT * FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2 FOR UPDATE;

-- name: UpdateWikiBlockDraft :execrows
UPDATE wiki_canvas_drafts SET state=$3,content=$4,markdown=$5,head_seq=head_seq+1,updated_at=clock_timestamp()
 ,schema_version=GREATEST(schema_version,sqlc.arg(schema_version)::integer)
 ,structure_seq=CASE WHEN sqlc.arg(structural)::boolean THEN head_seq+1 ELSE structure_seq END
WHERE company_id=$1 AND page_id=$2 AND head_seq=$6 AND schema_version IN (1,2);

-- name: GetWikiBlockDraftHead :one
SELECT head_seq,structure_seq,schema_version FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2;

-- name: GetWikiBlockProjection :one
SELECT content,markdown FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2 AND head_seq=$3;

-- name: SetWikiRevisionBlocks :exec
UPDATE document_snapshots SET block_content=$3,body=$4 WHERE company_id=$1 AND id=$2 AND block_content='{}'::jsonb;

-- name: SetCanvasBranchTitle :exec
UPDATE artifact_snapshots SET branch_title=sqlc.arg(branch_title) WHERE company_id=sqlc.arg(company_id) AND project_id=sqlc.arg(project_id)::uuid AND id=sqlc.arg(id);

-- name: GetCanvasBlockBody :one
SELECT COALESCE(wr.block_content,'{}'::jsonb) AS block_content FROM artifact_snapshots v LEFT JOIN document_snapshots wr ON wr.id=v.wiki_revision_id
WHERE v.company_id=sqlc.arg(company_id) AND (v.project_id=sqlc.arg(project_id)::uuid OR v.project_id IS NULL) AND v.id=sqlc.arg(id);

-- name: GetCanvasReference :one
SELECT v.artifact_id,a.accepted_version_id FROM artifact_snapshots v
JOIN artifacts a ON a.id=v.artifact_id
WHERE v.company_id=sqlc.arg(company_id)::uuid AND (v.project_id=sqlc.arg(project_id)::uuid OR v.project_id IS NULL) AND v.id=sqlc.arg(id)::uuid;

-- name: ListCanvasTemplates :many
SELECT id,title,content,created_at FROM canvas_templates WHERE company_id=$1 AND project_id=$2 ORDER BY created_at DESC LIMIT 100;

-- name: CreateCanvasTemplate :one
INSERT INTO canvas_templates(company_id,project_id,title,content,created_by_user_id) VALUES($1,$2,$3,$4,$5) RETURNING id;

-- name: GetCanvasTemplate :one
SELECT * FROM canvas_templates WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: DeleteCanvasTemplate :exec
DELETE FROM canvas_templates WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: ListCanvasPersonalViews :many
SELECT id,title,config FROM canvas_personal_views WHERE company_id=$1 AND project_id=$2 AND user_id=$3 ORDER BY created_at LIMIT 50;

-- name: CreateCanvasPersonalView :one
INSERT INTO canvas_personal_views(company_id,project_id,user_id,title,config) VALUES($1,$2,$3,$4,$5) RETURNING id;

-- name: DeleteCanvasPersonalView :exec
DELETE FROM canvas_personal_views WHERE company_id=$1 AND project_id=$2 AND user_id=$3 AND id=$4;

-- name: ListRemovedCanvasItems :many
SELECT i.id,i.title,i.artifact_id,COALESCE(i.pinned_version_id,a.accepted_version_id) AS source_version_id,i.content_revision
FROM canvas_items i LEFT JOIN artifacts a ON a.id=i.artifact_id
WHERE i.company_id=$1 AND i.project_id=sqlc.arg(project_id)::uuid AND i.deleted_at IS NOT NULL ORDER BY i.deleted_at DESC LIMIT 100;

-- name: RestoreCanvasItem :one
UPDATE canvas_items SET deleted_at=NULL,parent_frame_id=NULL,content_revision=content_revision+1
WHERE company_id=$1 AND project_id=$2 AND id=$3 AND deleted_at IS NOT NULL AND content_revision=$4 RETURNING *;
