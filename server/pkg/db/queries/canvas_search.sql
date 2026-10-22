-- name: SearchCanvasDocuments :many
SELECT a.id AS artifact_id,a.title,v.id AS version_id,COALESCE(d.content,wr.block_content) AS content
FROM artifacts a
JOIN wiki_pages p ON p.company_id=a.company_id AND p.id=a.id
JOIN LATERAL (SELECT * FROM artifact_snapshots snapshot WHERE snapshot.artifact_id=a.id AND snapshot.source_type='wiki'
 AND (p.artifact_type<>'wiki' OR snapshot.id=p.current_revision_id)
 ORDER BY snapshot.number DESC LIMIT 1) v ON true
JOIN document_snapshots wr ON wr.id=v.wiki_revision_id
 AND (p.artifact_type<>'wiki' OR wr.published_at IS NOT NULL)
LEFT JOIN wiki_canvas_drafts d ON d.company_id=v.company_id AND d.page_id=v.wiki_page_id
 AND (p.project_id IS NOT NULL OR p.source_output_id IS NOT NULL)
WHERE a.company_id=$1 AND (a.project_id=sqlc.arg(project_id)::uuid OR EXISTS(SELECT 1 FROM canvas_items item WHERE item.company_id=a.company_id AND item.project_id=sqlc.arg(project_id)::uuid AND item.artifact_id=a.id AND item.deleted_at IS NULL))
 AND (strpos(lower(COALESCE(d.markdown,wr.body)),lower(sqlc.arg(term)::text))>0
 OR strpos(lower(a.title),lower(sqlc.arg(term)::text))>0)
ORDER BY a.updated_at DESC LIMIT 50;
