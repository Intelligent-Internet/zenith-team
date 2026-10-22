-- name: SetProjectOverview :exec
UPDATE project_canvases SET overview_page_id=$3 WHERE company_id=$1 AND project_id=$2;

-- name: GetProjectOverviewVersion :one
SELECT v.* FROM project_canvases c JOIN canvas_version_sources v
 ON v.company_id=c.company_id AND v.project_id=c.project_id AND v.wiki_page_id=c.overview_page_id
 WHERE c.company_id=$1 AND c.project_id=$2 AND v.source_type='wiki' ORDER BY v.number DESC LIMIT 1;

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
INSERT INTO project_page_changes(company_id,project_id,page_id,run_id,actor_user_id,request_key,request_hash,operations,before_content,result,summary,status,before_theme,after_theme,crdt_updates,actor_agent_id)
 VALUES(sqlc.arg(company_id),NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid),sqlc.arg(page_id),sqlc.arg(run_id),sqlc.arg(actor_user_id),sqlc.arg(request_key),sqlc.arg(request_hash),sqlc.arg(operations),sqlc.arg(before_content),sqlc.arg(result),sqlc.arg(summary),sqlc.arg(status),sqlc.arg(before_theme),sqlc.arg(after_theme),sqlc.arg(crdt_updates),
 (SELECT agent_id FROM runs WHERE company_id=sqlc.arg(company_id) AND id=sqlc.arg(run_id))) RETURNING *;

-- name: GetProjectPageChangeReceipt :one
SELECT * FROM project_page_changes WHERE company_id=sqlc.arg(company_id) AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND actor_user_id=sqlc.arg(actor_user_id) AND request_key=sqlc.arg(request_key) AND (page_id=sqlc.narg(page_id)::uuid OR (sqlc.narg(page_id)::uuid IS NULL AND project_id IS NOT NULL));

-- name: GetProjectPageChange :one
SELECT * FROM project_page_changes WHERE company_id=sqlc.arg(company_id) AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND id=sqlc.arg(id) FOR UPDATE;

-- name: ListProjectPageChanges :many
WITH candidates AS (
 SELECT change.*,row_number() OVER(PARTITION BY status IN ('proposed','partially_applied','needs_update') ORDER BY created_at DESC,id DESC) AS rank
 FROM project_page_changes change WHERE actor_user_id=sqlc.arg(actor_user_id)::uuid AND company_id=sqlc.arg(company_id)::uuid AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
 AND (sqlc.narg(page_id)::uuid IS NULL OR page_id=sqlc.narg(page_id)::uuid)
)
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM candidates change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.status IN ('proposed','partially_applied','needs_update') OR change.rank<=50 ORDER BY change.created_at DESC,change.id DESC;

-- name: GetProjectPageChangeTitle :one
SELECT title FROM wiki_pages WHERE company_id=sqlc.arg(company_id) AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid AND id=sqlc.arg(id);

-- name: SetProjectPageChangeStatus :exec
UPDATE project_page_changes SET status=sqlc.arg(status) WHERE company_id=sqlc.arg(company_id) AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND id=sqlc.arg(id);

-- name: InsertProjectPresentation :one
WITH allocated AS (
 UPDATE artifacts SET next_number=next_number+1 WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(page_id)::uuid
 RETURNING next_number-1 AS number
), snapshot AS (
 INSERT INTO artifact_snapshots(company_id,project_id,artifact_id,number,source_type,title,theme,source_sequence,created_by_user_id,base_version_id)
 SELECT sqlc.arg(company_id)::uuid,sqlc.arg(project_id)::uuid,sqlc.arg(page_id)::uuid,number,'capture',
 sqlc.arg(title)::text,sqlc.arg(theme)::text,sqlc.arg(source_sequence)::bigint,sqlc.arg(created_by_user_id)::uuid,sqlc.arg(page_version_id)::uuid FROM allocated
 RETURNING id
)
INSERT INTO project_presentations(id,company_id,project_id,page_id,page_version_id,theme,title,content,documents,export_markdown,source_sequence,fingerprint,request_key,created_by_user_id,source_run_id)
 SELECT snapshot.id,sqlc.arg(company_id)::uuid,sqlc.arg(project_id)::uuid,sqlc.arg(page_id)::uuid,sqlc.arg(page_version_id)::uuid,
 sqlc.arg(theme)::text,sqlc.arg(title)::text,sqlc.arg(content)::jsonb,sqlc.arg(documents)::jsonb,sqlc.arg(export_markdown)::text,
 sqlc.arg(source_sequence)::bigint,sqlc.arg(fingerprint)::text,sqlc.arg(request_key)::uuid,sqlc.arg(created_by_user_id)::uuid,sqlc.narg(source_run_id)::uuid
 FROM snapshot RETURNING *;

-- name: GetProjectPresentationReceipt :one
SELECT * FROM project_presentations WHERE company_id=$1 AND project_id=$2 AND created_by_user_id=$3 AND request_key=$4;

-- name: GetProjectPresentation :one
SELECT * FROM project_presentations WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: ListProjectPresentations :many
SELECT capture.id,capture.page_id,capture.theme,capture.title,capture.source_sequence,capture.created_at,capture.created_by_user_id,capture.source_run_id,
 ARRAY(SELECT link.output_id FROM delivery_snapshots link JOIN output_access access ON access.company_id=link.company_id AND access.output_id=link.output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid WHERE link.company_id=capture.company_id AND link.snapshot_id=capture.id ORDER BY link.created_at,link.output_id)::uuid[] AS delivery_ids
 FROM project_presentations capture WHERE capture.company_id=sqlc.arg(company_id)::uuid AND capture.project_id=sqlc.arg(project_id)::uuid AND (sqlc.narg(page_id)::uuid IS NULL OR capture.page_id=sqlc.narg(page_id)::uuid) ORDER BY capture.created_at DESC,capture.id DESC;

-- name: PinProjectPresentationSource :exec
INSERT INTO project_presentation_sources(company_id,project_id,presentation_id,version_id,wiki_page_id,wiki_revision_id,asset_version_id,output_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING;

-- name: ListProjectPresentationSources :many
SELECT * FROM project_presentation_sources WHERE company_id=$1 AND project_id=$2 AND presentation_id=$3 ORDER BY version_id;

-- name: SetProjectPresentationExport :execrows
UPDATE project_presentations SET export_attachment_id=$4 WHERE company_id=$1 AND project_id=$2 AND id=$3
 AND (export_attachment_id IS NULL OR export_attachment_id=$4);

-- name: GetProjectPresentationForOutput :one
SELECT capture.* FROM project_presentations capture JOIN delivery_snapshots link ON link.company_id=capture.company_id AND link.snapshot_id=capture.id WHERE capture.company_id=sqlc.arg(company_id)::uuid AND link.output_id=sqlc.narg(output_id)::uuid;

-- name: GetPresentationOutputType :one
SELECT type FROM outputs WHERE company_id=$1 AND id=$2;

-- name: GetPresentationAssetSize :one
SELECT size_bytes FROM asset_versions WHERE company_id=$1 AND id=$2;

-- name: GetOverviewArtifactTitle :one
SELECT title FROM artifacts WHERE company_id=sqlc.arg(company_id) AND (project_id=sqlc.arg(project_id)::uuid OR type='wiki') AND id=sqlc.arg(id);

-- name: ListProjectPresentationDestinations :many
SELECT link.output_id,capture.id,capture.project_id,capture.page_id FROM project_presentations capture
 JOIN delivery_snapshots link ON link.company_id=capture.company_id AND link.snapshot_id=capture.id
 WHERE capture.company_id=sqlc.arg(company_id)::uuid AND link.output_id=ANY(sqlc.arg(output_ids)::uuid[])
AND EXISTS(SELECT 1 FROM project_access access WHERE access.company_id=capture.company_id AND access.project_id=capture.project_id AND access.user_id=sqlc.arg(actor_user_id)::uuid);

-- name: GetProjectPresentationForAttachment :one
SELECT id FROM project_presentations WHERE company_id=$1 AND export_attachment_id=$2;

-- name: ListPageSourceVersions :many
SELECT v.id AS version_id,v.artifact_id,latest.id AS latest_version_id,a.title
 FROM canvas_version_sources v JOIN artifacts a
 ON a.company_id=v.company_id AND a.project_id=v.project_id AND a.id=v.artifact_id
 JOIN LATERAL(SELECT candidate.id FROM canvas_version_sources candidate
 WHERE candidate.artifact_id=a.id AND candidate.source_type<>'capture' AND (candidate.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access access
     WHERE access.company_id=candidate.company_id AND access.output_id=candidate.access_output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid))
 ORDER BY candidate.number DESC LIMIT 1) latest ON true
 WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid
 AND v.id=ANY(sqlc.arg(version_ids)::uuid[])
 AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access access
     WHERE access.company_id=v.company_id AND access.output_id=v.access_output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid));

-- name: SetProjectPageScope :exec
UPDATE wiki_pages SET project_id=$3,theme=$4 WHERE company_id=$1 AND id=$2;

-- name: GetProjectPageMetadata :one
SELECT id,title,theme FROM wiki_pages WHERE company_id=sqlc.arg(company_id) AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::uuid AND id=sqlc.arg(id) AND status='active';

-- name: GetProjectPageVersion :one
SELECT version.* FROM canvas_version_sources version JOIN wiki_pages page ON page.id=version.wiki_page_id
 WHERE version.company_id=sqlc.arg(company_id) AND version.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND page.id=sqlc.arg(id) AND page.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
 AND version.source_type='wiki' AND page.artifact_type='page' ORDER BY version.number DESC LIMIT 1;

-- name: UpdateProjectPageMetadata :exec
UPDATE wiki_pages SET title=COALESCE(sqlc.narg(title)::text,title),theme=COALESCE(sqlc.narg(theme)::text,theme),updated_at=now()
 WHERE company_id=sqlc.arg(company_id)::uuid AND project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND id=sqlc.arg(page_id)::uuid;

-- name: ListProjectPageContributions :many
SELECT DISTINCT change.page_id FROM project_page_changes change JOIN runs run ON run.id=change.run_id
 WHERE change.company_id=$1 AND change.project_id=$2 AND run.task_id=$3;

-- name: ListPageChangeHistory :many
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM project_page_changes change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.actor_user_id=sqlc.arg(actor_user_id)::uuid AND change.company_id=sqlc.arg(company_id)::uuid AND change.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
 AND change.page_id=sqlc.arg(page_id)::uuid AND change.status NOT IN ('proposed','partially_applied','needs_update')
 AND (sqlc.narg(before_time)::timestamptz IS NULL OR (change.created_at,change.id)<(sqlc.narg(before_time)::timestamptz,sqlc.narg(before_id)::uuid))
 ORDER BY change.created_at DESC,change.id DESC LIMIT 51;

-- name: ListPendingPageChanges :many
SELECT change.id,change.page_id,change.run_id,change.summary,change.status,change.created_at,page.title AS page_title,
 change.actor_agent_id,COALESCE(agent.name,'Former Agent')::text AS agent_name,
 COALESCE((SELECT review.feedback FROM page_suggestion_reviews review WHERE review.company_id=change.company_id AND review.change_id=change.id AND review.feedback<>'' ORDER BY review.created_at DESC,review.id DESC LIMIT 1),'')::text AS feedback
 FROM project_page_changes change JOIN wiki_pages page ON page.id=change.page_id
 LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
 WHERE change.actor_user_id=sqlc.arg(actor_user_id)::uuid AND change.company_id=sqlc.arg(company_id) AND change.project_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(project_id)::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND change.page_id=sqlc.arg(page_id) AND change.status IN ('proposed','partially_applied','needs_update')
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
UPDATE artifacts SET pending_creation=true,creation_change_id=$3 WHERE company_id=$1 AND id=$2;

-- name: AcceptArtifactCreation :execrows
UPDATE artifacts SET pending_creation=false WHERE company_id=$1 AND creation_change_id=$2 AND pending_creation;

-- name: ListPageSuggestionReviews :many
SELECT * FROM page_suggestion_reviews WHERE company_id=$1 AND change_id=$2 ORDER BY created_at,id;

-- name: AdvanceArtifactCreationProposal :exec
UPDATE artifacts SET creation_change_id=$3 WHERE company_id=$1 AND creation_change_id=$2 AND pending_creation;

-- name: PagePendingCreation :one
SELECT EXISTS(SELECT 1 FROM artifacts a
 JOIN artifact_snapshots v ON v.company_id=a.company_id AND v.artifact_id=a.id
 WHERE a.company_id=$1 AND v.wiki_page_id=$2 AND a.pending_creation)::boolean;

-- name: PageChangeAuthorName :one
SELECT COALESCE(agent.name,'Former Agent')::text AS name FROM project_page_changes change
LEFT JOIN agents agent ON agent.company_id=change.company_id AND agent.id=change.actor_agent_id
WHERE change.company_id=$1 AND change.id=$2;

-- name: ListIntelligentCaptureTasks :many
-- The caller holds Project read authority in the capture transaction.
SELECT t.id,t.title,t.status,t.assignee_user_id,t.executed_by_agent_id,t.version,
 COALESCE(member.display_name,'Former teammate') AS assignee_name,COALESCE(agent.name,'') AS agent_name
 FROM tasks t LEFT JOIN users member ON member.id=t.assignee_user_id
 LEFT JOIN agents agent ON agent.id=t.executed_by_agent_id AND agent.company_id=t.company_id
 WHERE t.company_id=sqlc.arg(company_id)::uuid AND t.project_id=sqlc.arg(project_id)::uuid
 AND CASE sqlc.arg(filter)::text
   WHEN 'active' THEN t.status NOT IN ('done','cancelled')
   WHEN 'review' THEN t.status='in_review'
   WHEN 'done' THEN t.status='done'
   ELSE true
 END
 ORDER BY t.number DESC,t.id
 LIMIT sqlc.arg(row_limit)::int;

-- name: SetPresentationIntelligentEvidence :exec
UPDATE project_presentations SET intelligent_evidence=$4
 WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: ListCaptureDeliveries :many
SELECT link.output_id FROM delivery_snapshots link
JOIN output_access access ON access.company_id=link.company_id AND access.output_id=link.output_id AND access.user_id=$3
WHERE link.company_id=$1 AND link.snapshot_id=$2 ORDER BY link.created_at,link.output_id;
