-- name: GetOutputDocumentSource :one
SELECT i.id AS item_id,i.output_id,av.attachment_id,av.filename,av.content_type,av.size_bytes
FROM output_items i JOIN asset_versions av ON av.company_id=i.company_id AND av.id=i.asset_version_id
WHERE i.company_id=$1 AND i.output_id=$2 AND i.id=$3;

-- name: GetOutputDocument :one
SELECT d.page_id, (SELECT r.id FROM document_snapshots r WHERE r.company_id=d.company_id AND r.page_id=d.page_id ORDER BY r.number DESC LIMIT 1)::uuid AS current_revision_id FROM output_documents d
JOIN wiki_pages p ON p.company_id=d.company_id AND p.id=d.page_id
WHERE d.company_id=$1 AND d.output_id=$2 AND d.output_item_id=$3;

-- name: CreateOutputDocument :exec
INSERT INTO output_documents(company_id,output_id,output_item_id,page_id,submitted_sequence,submitted_content)
VALUES($1,$2,$3,$4,COALESCE((SELECT head_seq FROM wiki_canvas_drafts WHERE page_id=$4),0),
 (SELECT content FROM wiki_canvas_drafts WHERE page_id=$4));

-- name: ListOutputDocuments :many
SELECT d.output_item_id,d.page_id,p.title,p.current_revision_id FROM output_documents d
JOIN wiki_pages p ON p.company_id=d.company_id AND p.id=d.page_id
JOIN artifact_access access ON access.company_id=d.company_id AND access.artifact_id=d.page_id AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE d.company_id=$1 AND d.output_id=$2 ORDER BY d.created_at,d.page_id;

-- name: ListOutputDocumentVersions :many
SELECT d.output_item_id,r.id,r.page_id,r.number,r.title,left(r.body,1600)::text AS excerpt,r.created_at
FROM output_documents d JOIN document_snapshots r ON r.company_id=d.company_id AND r.page_id=d.page_id
JOIN artifact_access access ON access.company_id=d.company_id AND access.artifact_id=d.page_id AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE d.company_id=$1 AND d.output_id=$2 ORDER BY r.created_at DESC,r.id DESC LIMIT 100;

-- name: GetOutputDocumentVersion :one
SELECT r.page_id,r.body,r.block_content FROM output_documents d
JOIN document_snapshots r ON r.company_id=d.company_id AND r.page_id=d.page_id
JOIN artifact_access access ON access.company_id=d.company_id AND access.artifact_id=d.page_id AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE d.company_id=$1 AND d.output_id=$2 AND r.id=$3;

-- name: OutputDocumentPageExists :one
SELECT EXISTS(SELECT 1 FROM output_documents WHERE output_documents.company_id=$1 AND output_documents.output_id=$2 AND output_documents.page_id=$3 AND EXISTS(SELECT 1 FROM artifact_access access WHERE access.company_id=output_documents.company_id AND access.artifact_id=output_documents.page_id AND access.user_id=sqlc.arg(actor_user_id)::uuid))::boolean;

-- name: OutputDocumentProject :one
SELECT o.project_id FROM outputs o
WHERE o.company_id=$1 AND o.id=$2;

-- name: PageDocumentCheckpoint :one
SELECT revision_id FROM wiki_canvas_checkpoints WHERE company_id=$1 AND page_id=$2 AND through_seq=$3 ORDER BY created_at,revision_id LIMIT 1;
-- name: ListOutputDocumentFreshness :many
SELECT d.output_id, d.page_id, p.project_id,p.source_output_id,
 (p.status='active' AND EXISTS(SELECT 1 FROM artifact_access access WHERE access.company_id=p.company_id AND access.artifact_id=p.id AND access.user_id=sqlc.arg(actor_user_id)::uuid))::boolean AS can_read_live,
 (CASE
 WHEN proposal.status IN ('needs_update','superseded','rejected','undone') THEN true
 WHEN proposal.status IN ('applied','resolved') THEN
   capture.change_snapshot->'proposedContent' IS DISTINCT FROM head.content OR capture.theme IS DISTINCT FROM p.theme
 WHEN proposal.id IS NOT NULL THEN
   d.submitted_content IS DISTINCT FROM head.content
   OR proposal.decisions IS DISTINCT FROM capture.change_snapshot->'decisions'
   OR capture.change_snapshot->>'sharedTheme' IS DISTINCT FROM p.theme
 ELSE
   CASE WHEN d.submitted_content IS NOT NULL THEN d.submitted_content IS DISTINCT FROM head.content ELSE head.head_seq<>d.submitted_sequence END
   OR (capture.id IS NOT NULL AND capture.theme IS DISTINCT FROM p.theme)
 END OR (capture.id IS NOT NULL AND capture.title IS DISTINCT FROM p.title))::boolean AS stale
FROM output_documents d
JOIN wiki_pages p ON p.company_id=d.company_id AND p.id=d.page_id
JOIN wiki_canvas_drafts head ON head.company_id=d.company_id AND head.page_id=d.page_id
LEFT JOIN delivery_snapshots delivery ON delivery.company_id=d.company_id AND delivery.output_id=d.output_id
LEFT JOIN project_presentations capture ON capture.company_id=delivery.company_id AND capture.id=delivery.snapshot_id
LEFT JOIN project_page_changes proposal ON proposal.company_id=capture.company_id AND proposal.id=capture.change_id
WHERE d.company_id=sqlc.arg(company_id)::uuid AND d.output_id=ANY(sqlc.arg(output_ids)::uuid[]);

-- name: ListDeliveryAttribution :many
SELECT id,actor_type,actor_user_id,actor_agent_id FROM outputs
WHERE company_id=$1 AND id=ANY(sqlc.arg(output_ids)::uuid[]);

-- name: LinkPresentationDocument :exec
INSERT INTO output_documents(company_id,output_id,output_item_id,page_id,submitted_sequence,submitted_content)
SELECT capture.company_id,sqlc.arg(output_id)::uuid,item.id,capture.page_id,capture.source_sequence,capture.shared_content
FROM project_presentations capture
JOIN output_items item ON item.company_id=capture.company_id AND item.output_id=sqlc.arg(output_id)::uuid AND item.role='primary'
WHERE capture.company_id=sqlc.arg(company_id)::uuid AND capture.id=sqlc.arg(id)::uuid;

-- name: GetOutputDocumentOwnership :one
SELECT owner_user_id,project_id FROM outputs WHERE company_id=$1 AND id=$2;
