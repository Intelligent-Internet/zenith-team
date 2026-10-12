-- name: SearchCanvasDocuments :many
SELECT a.id AS artifact_id,a.title,v.id AS version_id,COALESCE(d.content,wr.block_content) AS content
FROM project_artifacts a
JOIN LATERAL (SELECT * FROM project_artifact_versions pv WHERE pv.artifact_id=a.id ORDER BY pv.number DESC LIMIT 1) v ON true
JOIN wiki_pages p ON p.company_id=v.company_id AND p.id=v.wiki_page_id
JOIN wiki_revisions wr ON wr.id=CASE
 WHEN p.project_id IS NULL AND p.source_output_id IS NULL THEN p.current_revision_id
 ELSE v.wiki_revision_id END
LEFT JOIN wiki_canvas_drafts d ON d.company_id=v.company_id AND d.page_id=v.wiki_page_id
 AND (p.project_id IS NOT NULL OR p.source_output_id IS NOT NULL)
WHERE a.company_id=$1 AND a.project_id=$2
 AND (strpos(lower(COALESCE(d.markdown,wr.body)),lower(sqlc.arg(term)::text))>0
 OR strpos(lower(a.title),lower(sqlc.arg(term)::text))>0)
ORDER BY a.updated_at DESC LIMIT 50;
