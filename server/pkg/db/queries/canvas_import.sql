-- name: GetCanvasOutputDocumentSource :one
SELECT i.id AS item_id,i.output_id,av.attachment_id,av.filename,av.content_type,av.size_bytes
FROM project_artifact_versions v
JOIN output_items i ON i.company_id=v.company_id AND i.output_id=v.output_id
JOIN asset_versions av ON av.company_id=i.company_id AND av.id=i.asset_version_id
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid
AND v.id=sqlc.arg(version_id)::uuid AND i.id=sqlc.arg(output_item_id)::uuid;

-- name: GetCanvasImportedDocument :one
SELECT v.id FROM output_documents i
JOIN project_artifact_versions v ON v.company_id=i.company_id AND v.wiki_page_id=i.page_id
WHERE i.company_id=$1 AND v.artifact_id=$2 AND i.output_item_id=$3
ORDER BY v.number DESC LIMIT 1;

-- name: SetWikiOutputSource :exec
UPDATE wiki_pages SET source_output_id=$3 WHERE company_id=$1 AND id=$2;

-- name: LockCanvasWikiPage :one
SELECT id,status,version FROM wiki_pages WHERE company_id=$1 AND id=$2 FOR UPDATE;
