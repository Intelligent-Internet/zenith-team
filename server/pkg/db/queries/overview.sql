-- name: SetProjectOverview :exec
UPDATE project_canvases SET overview_page_id=$3 WHERE company_id=$1 AND project_id=$2;

-- name: GetProjectOverviewVersion :one
SELECT v.* FROM project_canvases c JOIN canvas_version_sources v
 ON v.company_id=c.company_id AND v.project_id=c.project_id AND v.wiki_page_id=c.overview_page_id
 WHERE c.company_id=$1 AND c.project_id=$2 ORDER BY v.number DESC LIMIT 1;

-- name: ListOverviewTasks :many
SELECT t.id,t.title,t.status,t.assignee_user_id,t.executed_by_agent_id,t.execution_generation,t.version,t.labels,
 COALESCE(member.display_name,'Former teammate') AS assignee_name,COALESCE(agent.name,'') AS agent_name
 FROM tasks t LEFT JOIN users member ON member.id=t.assignee_user_id
 LEFT JOIN agents agent ON agent.id=t.executed_by_agent_id AND agent.company_id=t.company_id
 WHERE t.company_id=$1 AND t.project_id=$2 ORDER BY t.created_at,t.id;

-- name: GetOverviewTaskRun :one
SELECT r.task_id,r.conversation_id,t.project_id FROM runs r JOIN tasks t ON t.id=r.task_id
 WHERE r.company_id=$1 AND r.id=$2 AND t.assignee_user_id=$3;

-- name: GetPageManagedBlock :one
SELECT * FROM project_page_managed_blocks WHERE company_id=$1 AND page_id=$2 AND block_id=$3;

-- name: ListPageManagedBlocks :many
SELECT * FROM project_page_managed_blocks WHERE company_id=$1 AND page_id=$2;

-- name: ForgetPageManagedBlock :exec
DELETE FROM project_page_managed_blocks WHERE company_id=$1 AND page_id=$2 AND block_id=$3;

-- name: SetPageManagedBlock :exec
INSERT INTO project_page_managed_blocks(company_id,page_id,block_id,content_hash,run_id) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(block_id) DO UPDATE SET content_hash=excluded.content_hash,run_id=excluded.run_id;

-- name: InsertProjectPageChange :one
INSERT INTO project_page_changes(company_id,project_id,page_id,run_id,actor_user_id,request_key,request_hash,operations,before_content,result,summary,status,before_theme,after_theme,actor_agent_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,
 (SELECT agent_id FROM runs WHERE company_id=$1 AND id=$4)) RETURNING *;

-- name: GetProjectPageChangeReceipt :one
SELECT * FROM project_page_changes WHERE company_id=$1 AND project_id=$2 AND actor_user_id=$3 AND request_key=$4;

-- name: GetProjectPageChange :one
SELECT * FROM project_page_changes WHERE company_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE;

-- name: ListProjectPageChanges :many
WITH candidates AS (
 SELECT change.*,row_number() OVER(PARTITION BY status IN ('proposed','partially_applied','needs_update') ORDER BY created_at DESC,id DESC) AS rank
 FROM project_page_changes change WHERE company_id=sqlc.arg(company_id)::uuid AND project_id=sqlc.arg(project_id)::uuid
 AND (sqlc.narg(page_id)::uuid IS NULL OR page_id=sqlc.narg(page_id)::uuid)
)
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM candidates change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.status IN ('proposed','partially_applied','needs_update') OR change.rank<=50 ORDER BY change.created_at DESC,change.id DESC;

-- name: GetProjectPageChangeTitle :one
SELECT title FROM wiki_pages WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: SetProjectPageChangeStatus :exec
UPDATE project_page_changes SET status=$4 WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: InsertProjectPresentation :one
INSERT INTO project_presentations(company_id,project_id,page_id,page_version_id,theme,title,content,documents,export_markdown,source_sequence,fingerprint,request_key,created_by_user_id,source_run_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING *;

-- name: GetProjectPresentationReceipt :one
SELECT * FROM project_presentations WHERE company_id=$1 AND project_id=$2 AND created_by_user_id=$3 AND request_key=$4;

-- name: GetProjectPresentation :one
SELECT * FROM project_presentations WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: ListProjectPresentations :many
SELECT id,page_id,theme,title,source_sequence,created_at,created_by_user_id,source_run_id,output_id
 FROM project_presentations WHERE company_id=$1 AND project_id=$2 AND (sqlc.narg(page_id)::uuid IS NULL OR page_id=sqlc.narg(page_id)::uuid) ORDER BY created_at DESC,id DESC;

-- name: PinProjectPresentationSource :exec
INSERT INTO project_presentation_sources(company_id,project_id,presentation_id,version_id,wiki_page_id,wiki_revision_id,asset_version_id,output_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING;

-- name: ListProjectPresentationSources :many
SELECT * FROM project_presentation_sources WHERE company_id=$1 AND project_id=$2 AND presentation_id=$3 ORDER BY version_id;

-- name: BindProjectPresentationOutput :execrows
UPDATE project_presentations SET output_id=$4 WHERE company_id=$1 AND project_id=$2 AND id=$3
 AND source_run_id=$5 AND (output_id IS NULL OR output_id=$4);

-- name: SetProjectPresentationExport :execrows
UPDATE project_presentations SET export_attachment_id=$4 WHERE company_id=$1 AND project_id=$2 AND id=$3
 AND (export_attachment_id IS NULL OR export_attachment_id=$4);

-- name: GetProjectPresentationForOutput :one
SELECT * FROM project_presentations WHERE company_id=$1 AND output_id=$2;

-- name: GetPresentationOutputType :one
SELECT type FROM outputs WHERE company_id=$1 AND id=$2;

-- name: GetPresentationAssetSize :one
SELECT size_bytes FROM asset_versions WHERE company_id=$1 AND id=$2;

-- name: GetOverviewArtifactTitle :one
SELECT title FROM project_artifacts WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: ListProjectPresentationDestinations :many
SELECT output_id,id,project_id,page_id FROM project_presentations
 WHERE company_id=sqlc.arg(company_id)::uuid AND output_id=ANY(sqlc.arg(output_ids)::uuid[])
AND EXISTS(SELECT 1 FROM project_access access WHERE access.company_id=project_presentations.company_id AND access.project_id=project_presentations.project_id AND access.user_id=sqlc.arg(actor_user_id)::uuid);

-- name: GetProjectPresentationForAttachment :one
SELECT id FROM project_presentations WHERE company_id=$1 AND export_attachment_id=$2;

-- name: ListPageSourceVersions :many
SELECT v.id AS version_id,v.artifact_id,latest.id AS latest_version_id,a.title
 FROM canvas_version_sources v JOIN project_artifacts a
 ON a.company_id=v.company_id AND a.project_id=v.project_id AND a.id=v.artifact_id
 JOIN LATERAL(SELECT candidate.id FROM canvas_version_sources candidate
 WHERE candidate.artifact_id=a.id AND (candidate.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access access
     WHERE access.company_id=candidate.company_id AND access.output_id=candidate.access_output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid))
 ORDER BY candidate.number DESC LIMIT 1) latest ON true
 WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid
 AND v.id=ANY(sqlc.arg(version_ids)::uuid[])
 AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access access
     WHERE access.company_id=v.company_id AND access.output_id=v.access_output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid));

-- name: SetProjectPageScope :exec
UPDATE wiki_pages SET project_id=$3,theme=$4 WHERE company_id=$1 AND id=$2;

-- name: GetProjectPageMetadata :one
SELECT id,title,theme FROM wiki_pages WHERE company_id=$1 AND project_id=$2 AND id=$3 AND status='active';

-- name: GetProjectPageVersion :one
SELECT version.* FROM canvas_version_sources version JOIN wiki_pages page ON page.id=version.wiki_page_id
 WHERE version.company_id=$1 AND version.project_id=$2 AND page.id=$3 AND page.project_id=$2
 AND version.access_output_id IS NULL ORDER BY version.number DESC LIMIT 1;

-- name: UpdateProjectPageMetadata :exec
UPDATE wiki_pages SET title=COALESCE(sqlc.narg(title)::text,title),theme=COALESCE(sqlc.narg(theme)::text,theme),updated_at=now()
 WHERE company_id=sqlc.arg(company_id)::uuid AND project_id=sqlc.arg(project_id)::uuid AND id=sqlc.arg(page_id)::uuid;

-- name: ListProjectPageContributions :many
SELECT DISTINCT change.page_id FROM project_page_changes change JOIN runs run ON run.id=change.run_id
 WHERE change.company_id=$1 AND change.project_id=$2 AND run.task_id=$3;

-- name: ListPageChangeHistory :many
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM project_page_changes change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.company_id=sqlc.arg(company_id)::uuid AND change.project_id=sqlc.arg(project_id)::uuid
 AND change.page_id=sqlc.arg(page_id)::uuid AND change.status NOT IN ('proposed','partially_applied','needs_update')
 AND (sqlc.narg(before_time)::timestamptz IS NULL OR (change.created_at,change.id)<(sqlc.narg(before_time)::timestamptz,sqlc.narg(before_id)::uuid))
 ORDER BY change.created_at DESC,change.id DESC LIMIT 51;

-- name: ListPendingPageChanges :many
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM project_page_changes change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.company_id=$1 AND change.project_id=$2 AND change.page_id=$3 AND change.status IN ('proposed','partially_applied','needs_update')
 ORDER BY change.created_at DESC,change.id DESC;

-- name: ListPageSuggestionChanges :many
SELECT * FROM project_page_changes WHERE company_id=$1 AND page_id=$2
 AND status IN ('proposed','partially_applied','needs_update') ORDER BY id;

-- name: UpdatePageSuggestionDecisions :exec
UPDATE project_page_changes SET decisions=$3,status=$4 WHERE company_id=$1 AND id=$2;

-- name: SetPageChangeSupersedes :exec
UPDATE project_page_changes SET supersedes_id=$3 WHERE company_id=$1 AND id=$2;

-- name: GetPageSuggestionReview :one
SELECT * FROM page_suggestion_reviews WHERE company_id=$1 AND actor_user_id=$2 AND request_key=$3;

-- name: RecordPageSuggestionReview :one
INSERT INTO page_suggestion_reviews(company_id,change_id,actor_user_id,request_key,fingerprint,action,selection,feedback,before_sequence,after_sequence,result,before_revision_id,result_revision_id,before_theme,result_theme)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING *;

-- name: SetPresentationSharedContent :exec
UPDATE project_presentations SET shared_content=$3 WHERE company_id=$1 AND id=$2;

-- name: SetPresentationProposal :exec
UPDATE project_presentations SET change_id=$3,change_snapshot=$4 WHERE company_id=$1 AND id=$2;

-- name: SetArtifactCreationProposal :exec
UPDATE project_artifacts SET pending_creation=true,creation_change_id=$3 WHERE company_id=$1 AND id=$2;

-- name: AcceptArtifactCreation :execrows
UPDATE project_artifacts SET pending_creation=false WHERE company_id=$1 AND creation_change_id=$2 AND pending_creation;

-- name: ListPageSuggestionReviews :many
SELECT * FROM page_suggestion_reviews WHERE company_id=$1 AND change_id=$2 ORDER BY created_at,id;

-- name: AdvanceArtifactCreationProposal :exec
UPDATE project_artifacts SET creation_change_id=$3 WHERE company_id=$1 AND creation_change_id=$2 AND pending_creation;

-- name: PagePendingCreation :one
SELECT EXISTS(SELECT 1 FROM project_artifacts a
 JOIN project_artifact_versions v ON v.company_id=a.company_id AND v.artifact_id=a.id
 WHERE a.company_id=$1 AND v.wiki_page_id=$2 AND a.pending_creation)::boolean;

-- name: PageChangeAuthorName :one
SELECT COALESCE(agent.name,'Former Agent')::text AS name FROM project_page_changes change
LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
WHERE change.company_id=$1 AND change.id=$2;
