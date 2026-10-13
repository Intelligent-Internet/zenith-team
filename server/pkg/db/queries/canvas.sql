-- name: EnsureProjectCanvas :one
INSERT INTO project_canvases(company_id,project_id) VALUES ($1,$2)
ON CONFLICT(project_id) DO UPDATE SET title=project_canvases.title RETURNING *;

-- name: GetProjectCanvas :one
SELECT * FROM project_canvases WHERE company_id=$1 AND project_id=$2;

-- name: LockCanvasHead :one
SELECT * FROM project_canvases WHERE company_id=$1 AND project_id=$2 FOR UPDATE;

-- name: AdvanceCanvasHead :one
UPDATE project_canvases SET head_seq=head_seq+1 WHERE company_id=$1 AND project_id=$2 RETURNING head_seq;

-- name: GetCanvasReceipt :one
SELECT * FROM project_canvas_operations WHERE project_id=$1 AND actor_user_id=$2 AND request_key=$3;

-- name: InsertCanvasReceipt :exec
INSERT INTO project_canvas_operations(company_id,project_id,seq,actor_user_id,request_key,request_hash,changes,result)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: ListCanvasChanges :many
SELECT seq,changes FROM project_canvas_operations WHERE project_id=$1 AND seq>$2 ORDER BY seq LIMIT 501;

-- name: CreateCanvasArtifact :one
INSERT INTO project_artifacts(company_id,project_id,title,created_by_user_id,task_id,source_run_id) VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: GetCanvasTask :one
SELECT id FROM tasks WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: LockCanvasArtifact :one
SELECT * FROM project_artifacts WHERE company_id=$1 AND project_id=$2 AND id=$3 AND archived_at IS NULL FOR UPDATE;

-- name: AllocateCanvasVersion :one
UPDATE project_artifacts SET next_number=next_number+1, version=version+1,updated_at=clock_timestamp()
WHERE id=$1 RETURNING (next_number-1)::bigint AS number;

-- name: CreateCanvasVersion :one
INSERT INTO project_artifact_versions(company_id,project_id,artifact_id,number,source_type,wiki_page_id,wiki_revision_id,asset_version_id,output_id,base_version_id,created_by_user_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *;

-- name: SetCanvasAccepted :exec
UPDATE project_artifacts SET accepted_version_id=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1;

-- name: RenameCanvasArtifact :exec
UPDATE project_artifacts SET title=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1;

-- name: GetCanvasVersion :one
SELECT * FROM canvas_version_sources WHERE company_id=$1 AND project_id=$2 AND id=$3;

-- name: CanvasVersionAccess :one
SELECT EXISTS(SELECT 1 FROM canvas_version_sources v
LEFT JOIN wiki_revisions w ON w.id=v.wiki_revision_id
LEFT JOIN wiki_pages page ON page.id=v.wiki_page_id
LEFT JOIN asset_versions f ON f.id=v.asset_version_id
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid AND v.id=sqlc.arg(version_id)::uuid
AND (v.access_output_id IS NOT NULL OR EXISTS(SELECT 1 FROM project_access access WHERE access.company_id=v.company_id AND access.project_id=COALESCE(page.project_id,v.project_id) AND access.user_id=sqlc.arg(actor_user_id)::uuid))
AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.source_type='wiki' AND w.id IS NOT NULL AND (page.project_id IS NOT NULL OR page.source_output_id IS NOT NULL OR w.published_at IS NOT NULL)) OR (v.source_type='file' AND f.id IS NOT NULL AND EXISTS(SELECT 1 FROM project_file_assets pf WHERE pf.asset_id=f.asset_id AND pf.project_id=v.project_id))
OR (v.source_type='output' AND EXISTS(SELECT 1 FROM output_access a WHERE a.output_id=v.output_id AND a.user_id=sqlc.arg(actor_user_id)::uuid))))::boolean;

-- name: CreateCanvasItem :one
INSERT INTO canvas_items(company_id,project_id,canvas_id,item_type,artifact_id,pinned_version_id,title,x,y,width,height,created_by_user_id,view_config)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING *;

-- name: LockCanvasItem :one
SELECT * FROM canvas_items WHERE company_id=$1 AND project_id=$2 AND id=$3 AND deleted_at IS NULL FOR UPDATE;

-- name: MoveCanvasItem :execrows
UPDATE canvas_items SET x=$2,y=$3,width=$4,height=$5,geometry_revision=geometry_revision+1
WHERE id=$1 AND geometry_revision=$6 AND deleted_at IS NULL;

-- name: ConfigureCanvasItem :exec
UPDATE canvas_items SET title=$2,representation=$3,view_config=$4,version_policy=$5,pinned_version_id=$6,parent_frame_id=$7,content_revision=content_revision+1 WHERE id=$1;

-- name: DeleteCanvasItem :exec
UPDATE canvas_items SET deleted_at=clock_timestamp(),content_revision=content_revision+1 WHERE id=$1;

-- name: UngroupCanvasChildren :many
UPDATE canvas_items SET parent_frame_id=NULL,content_revision=content_revision+1 WHERE parent_frame_id=$1 AND deleted_at IS NULL RETURNING id;

-- name: DeleteCanvasItemEdges :many
UPDATE canvas_edges SET deleted_at=clock_timestamp(),version=version+1 WHERE (source_item_id=$1 OR target_item_id=$1) AND deleted_at IS NULL RETURNING id;

-- name: CreateCanvasEdge :one
INSERT INTO canvas_edges(company_id,project_id,canvas_id,source_item_id,target_item_id,label) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id;

-- name: DeleteCanvasEdge :execrows
UPDATE canvas_edges SET deleted_at=clock_timestamp(),version=version+1 WHERE company_id=$1 AND project_id=$2 AND id=$3 AND deleted_at IS NULL;

-- name: CreateReviewThread :one
INSERT INTO review_threads(company_id,project_id,artifact_version_id,anchor,created_by_user_id,page_id) VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: LockReviewThread :one
SELECT * FROM review_threads WHERE company_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE;

-- name: CreateReviewComment :one
INSERT INTO review_comments(company_id,project_id,thread_id,body,author_user_id) VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- name: RecordReviewCommentRevision :exec
INSERT INTO review_comment_revisions(comment_id,revision,body,author_user_id) SELECT id,version,body,sqlc.arg(actor_user_id)::uuid FROM review_comments WHERE id=sqlc.arg(comment_id)::uuid;

-- name: EditReviewComment :one
UPDATE review_comments SET body=$4,version=version+1,edited_at=clock_timestamp(),deleted_at=$5
WHERE company_id=$1 AND project_id=$2 AND id=$3 AND version=$6 AND author_user_id=$7 AND author_agent_id IS NULL AND deleted_at IS NULL RETURNING *;

-- name: SetReviewThreadStatus :exec
UPDATE review_threads SET status=$2,version=version+1,resolved_by_user_id=$3,resolved_at=$4 WHERE id=$1;

-- name: RecordReviewThreadEvent :exec
INSERT INTO review_thread_events(thread_id,action,actor_user_id) VALUES ($1,$2,$3);

-- name: RecordCanvasDecision :one
INSERT INTO artifact_review_decisions(company_id,project_id,artifact_id,version_id,action,feedback,actor_user_id) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id;

-- name: RegisterCanvasFileAsset :exec
INSERT INTO project_file_assets(company_id,project_id,asset_id) VALUES ($1,$2,$3);

-- name: InsertCanvasFileVersion :one
INSERT INTO asset_versions(company_id,asset_id,version_number,attachment_id,source_type,created_by_user_id,filename,content_type,size_bytes,integrity_status)
SELECT a.company_id,sqlc.arg(asset_id)::uuid,1,a.id,'human',sqlc.arg(actor_user_id)::uuid,a.filename,a.content_type,a.size_bytes,'verified'
FROM attachments a WHERE a.id=sqlc.arg(attachment_id)::uuid AND a.company_id=sqlc.arg(company_id)::uuid AND a.uploaded_by_user_id=sqlc.arg(actor_user_id)::uuid AND a.status='ready'
AND EXISTS(SELECT 1 FROM attachment_access access WHERE access.company_id=a.company_id AND access.attachment_id=a.id AND access.user_id=sqlc.arg(actor_user_id)::uuid)
AND NOT EXISTS(SELECT 1 FROM asset_versions av WHERE av.attachment_id=a.id) RETURNING *;

-- name: LockCanvasUpload :one
SELECT * FROM attachments WHERE company_id=$1 AND id=$2 AND uploaded_by_user_id=$3 AND status='ready' FOR UPDATE;

-- name: CanvasOutputAccess :one
SELECT EXISTS(SELECT 1 FROM output_access WHERE company_id=$1 AND output_id=$2 AND user_id=$3)::boolean;

-- name: GetCanvasWikiRevision :one
SELECT * FROM wiki_revisions WHERE company_id=$1 AND page_id=$2 AND id=$3;

-- name: GetCanvasWikiPage :one
SELECT * FROM wiki_pages WHERE company_id=$1 AND id=$2 AND status='active';

-- name: GetCanvasDraft :one
SELECT * FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2;

-- name: CreateCanvasDraft :one
INSERT INTO wiki_canvas_drafts(company_id,page_id,state,markdown,title,summary,collection_id)
SELECT $1,$2,$3,$4,p.title,p.summary,p.collection_id FROM wiki_pages p WHERE p.company_id=$1 AND p.id=$2
ON CONFLICT(page_id) DO UPDATE SET page_id=wiki_canvas_drafts.page_id RETURNING *;

-- name: UpdateCanvasDraft :execrows
UPDATE wiki_canvas_drafts SET state=$3,markdown=$4,head_seq=head_seq+1,updated_at=clock_timestamp() WHERE company_id=$1 AND page_id=$2 AND head_seq=$5;

-- name: AllocateWikiRevisionNumber :one
UPDATE wiki_pages SET next_revision_number=next_revision_number+1 WHERE company_id=$1 AND id=$2 RETURNING (next_revision_number-1)::bigint AS number;

-- name: RecordCanvasCheckpoint :exec
INSERT INTO wiki_canvas_checkpoints(company_id,page_id,revision_id,through_seq) VALUES ($1,$2,$3,$4);

-- name: ListCanvasItems :many
SELECT jsonb_build_object('id',i.id,'type',i.item_type,'artifactId',i.artifact_id,'pinnedVersionId',i.pinned_version_id,'versionPolicy',i.version_policy,'parentFrameId',i.parent_frame_id,'title',i.title,'x',i.x,'y',i.y,'width',i.width,'height',i.height,'stackOrder',i.stack_order,'representation',i.representation,'viewConfig',i.view_config,'geometryRevision',i.geometry_revision,'contentRevision',i.content_revision) AS payload FROM canvas_items i WHERE i.company_id=$1 AND i.project_id=$2 AND i.deleted_at IS NULL AND (sqlc.arg(all_items)::boolean OR i.id=ANY(sqlc.arg(ids)::uuid[])) ORDER BY i.stack_order,i.id;

-- name: ListCanvasEdges :many
SELECT jsonb_build_object('id',e.id,'source',e.source_item_id,'target',e.target_item_id,'label',e.label,'revision',e.version) AS payload FROM canvas_edges e WHERE e.company_id=$1 AND e.project_id=$2 AND e.deleted_at IS NULL AND (sqlc.arg(all_edges)::boolean OR e.id=ANY(sqlc.arg(ids)::uuid[]));

-- name: ListCanvasArtifacts :many
SELECT jsonb_build_object('id',a.id,'pageId',(SELECT page.id FROM wiki_pages page WHERE page.id=v.wiki_page_id AND page.project_id=a.project_id AND page.source_output_id IS NULL),'title',a.title,'revision',a.version,'acceptedVersionId',CASE WHEN EXISTS(SELECT 1 FROM canvas_version_sources p LEFT JOIN output_access oa ON oa.output_id=p.access_output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid WHERE p.id=a.accepted_version_id AND (p.access_output_id IS NULL OR oa.output_id IS NOT NULL)) THEN a.accepted_version_id ELSE NULL END,'latestVersionId',v.id,'latestVersion',jsonb_build_object('id',v.id,'artifactId',v.artifact_id,'number',v.number,'sourceType',v.source_type,'branchTitle',v.branch_title,'wikiPageId',v.wiki_page_id,'wikiRevisionId',v.wiki_revision_id,'assetVersionId',v.asset_version_id,'outputId',v.output_id,'sourceOutputId',v.access_output_id,'outputItemId',(SELECT d.output_item_id FROM output_documents d WHERE d.page_id=v.wiki_page_id),'runId',COALESCE(o.run_id,(SELECT origin.source_run_id FROM project_artifacts origin WHERE origin.id=v.artifact_id)),'attachmentId',av.attachment_id,'filename',COALESCE(av.filename,primary_file.filename),'contentType',CASE WHEN v.source_type='wiki' THEN 'text/markdown' ELSE COALESCE(av.content_type,primary_file.content_type) END,'sizeBytes',COALESCE(av.size_bytes,primary_file.size_bytes),'excerpt',left(COALESCE(wr.body,o.summary,''),1600),'createdAt',v.created_at,'baseVersionId',v.base_version_id,'openComments',(SELECT count(*) FROM review_threads t WHERE t.artifact_version_id=v.id AND t.page_id IS NULL AND t.status='open')),'openComments',(SELECT count(*) FROM review_threads t WHERE t.artifact_version_id=v.id AND t.page_id IS NULL AND t.status='open'),'updatedAt',a.updated_at,'taskId',COALESCE(a.task_id,origin.task_id),'creatorName',COALESCE(creator.display_name,'Former teammate'),
'resultStatus',(SELECT d.status FROM deliverables d WHERE d.output_id=v.access_output_id),
'reviewState',CASE WHEN a.accepted_version_id=v.id THEN 'accepted' WHEN decision.action IN ('request_changes','reject') THEN 'changes_requested' ELSE 'needs_review' END) AS payload
FROM project_artifacts a JOIN LATERAL(SELECT pv.* FROM canvas_version_sources pv
LEFT JOIN output_access access ON access.output_id=pv.access_output_id AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE pv.artifact_id=a.id AND (pv.access_output_id IS NULL OR access.output_id IS NOT NULL)
AND ((pv.source_type='wiki' AND pv.wiki_revision_id IS NOT NULL) OR (pv.source_type='file' AND pv.asset_version_id IS NOT NULL) OR (pv.source_type='output' AND pv.output_id IS NOT NULL))
ORDER BY pv.number DESC LIMIT 1) v ON true
LEFT JOIN wiki_revisions wr ON wr.id=v.wiki_revision_id
LEFT JOIN asset_versions av ON av.id=v.asset_version_id
LEFT JOIN outputs o ON o.id=v.output_id
LEFT JOIN LATERAL (
 SELECT f.filename,f.content_type,f.size_bytes FROM output_items oi
 JOIN asset_versions f ON f.id=oi.asset_version_id WHERE oi.output_id=o.id
 ORDER BY (oi.role='primary') DESC,oi.ordinal LIMIT 1
) primary_file ON true
LEFT JOIN users creator ON creator.id=a.created_by_user_id
LEFT JOIN LATERAL (
 SELECT source.task_id FROM canvas_version_sources pv
 JOIN outputs source ON source.id=pv.output_id
 JOIN tasks t ON t.id=source.task_id AND t.project_id=a.project_id
 WHERE pv.artifact_id=a.id AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=pv.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid)
 ORDER BY pv.number LIMIT 1
) origin ON true
LEFT JOIN LATERAL (
 SELECT d.action FROM artifact_review_decisions d WHERE d.version_id=v.id
 ORDER BY d.created_at DESC,d.id DESC LIMIT 1
) decision ON true
WHERE a.company_id=sqlc.arg(company_id)::uuid AND a.project_id=sqlc.arg(project_id)::uuid AND a.archived_at IS NULL AND NOT a.pending_creation
AND (sqlc.arg(all_artifacts)::boolean OR a.id=ANY(sqlc.arg(ids)::uuid[])) AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.wiki_revision_id IS NOT NULL AND wr.id IS NOT NULL) OR
(v.asset_version_id IS NOT NULL AND av.id IS NOT NULL) OR
(v.output_id IS NOT NULL AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid))) ORDER BY a.updated_at DESC,a.id;

-- name: ListCanvasVersions :many
SELECT jsonb_build_object('id',v.id,'artifactId',v.artifact_id,'number',v.number,'sourceType',v.source_type,'branchTitle',v.branch_title,'wikiPageId',v.wiki_page_id,'wikiRevisionId',v.wiki_revision_id,'assetVersionId',v.asset_version_id,'outputId',v.output_id,'sourceOutputId',v.access_output_id,'outputItemId',(SELECT d.output_item_id FROM output_documents d WHERE d.page_id=v.wiki_page_id),'runId',COALESCE(o.run_id,(SELECT origin.source_run_id FROM project_artifacts origin WHERE origin.id=v.artifact_id)),'attachmentId',av.attachment_id,'filename',COALESCE(av.filename,primary_file.filename),'contentType',CASE WHEN v.source_type='wiki' THEN 'text/markdown' ELSE COALESCE(av.content_type,primary_file.content_type) END,'sizeBytes',COALESCE(av.size_bytes,primary_file.size_bytes),'excerpt',left(COALESCE(wr.body,o.summary,''),1600),'createdAt',v.created_at,'baseVersionId',v.base_version_id,'openComments',(SELECT count(*) FROM review_threads t WHERE t.artifact_version_id=v.id AND t.page_id IS NULL AND t.status='open')) AS payload FROM canvas_version_sources v LEFT JOIN wiki_revisions wr ON wr.id=v.wiki_revision_id
LEFT JOIN asset_versions av ON av.id=v.asset_version_id
LEFT JOIN outputs o ON o.id=v.output_id
LEFT JOIN LATERAL (
 SELECT f.filename,f.content_type,f.size_bytes FROM output_items oi
 JOIN asset_versions f ON f.id=oi.asset_version_id WHERE oi.output_id=o.id
 ORDER BY (oi.role='primary') DESC,oi.ordinal LIMIT 1
) primary_file ON true
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid AND v.artifact_id=sqlc.arg(artifact_id)::uuid AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.wiki_revision_id IS NOT NULL AND wr.id IS NOT NULL) OR
(v.asset_version_id IS NOT NULL AND av.id IS NOT NULL) OR
(v.output_id IS NOT NULL AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid))) ORDER BY v.number DESC LIMIT 200;

-- name: GetCanvasVersionBody :one
SELECT COALESCE(wr.body,'')::text AS body FROM canvas_version_sources v LEFT JOIN wiki_revisions wr ON wr.id=v.wiki_revision_id WHERE v.company_id=$1 AND v.project_id=$2 AND v.id=$3;

-- name: ListReviewThreads :many
SELECT jsonb_build_object('id',t.id,'versionId',t.artifact_version_id,'pageId',t.page_id,'anchor',t.anchor,'status',t.status,'revision',t.version,'createdAt',t.created_at,'agentRuns',COALESCE((SELECT jsonb_agg(jsonb_build_object('agentId',ac.agent_id,'agentName',aa.name,'runId',ar.id,'status',ar.status)) FROM conversations ac JOIN agents aa ON aa.id=ac.agent_id JOIN LATERAL (SELECT id,status FROM runs WHERE conversation_id=ac.id ORDER BY created_at DESC,id DESC LIMIT 1) ar ON true WHERE ac.document_thread_id=t.id),'[]'::jsonb),'comments',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',c.id,'body',CASE WHEN c.deleted_at IS NULL THEN c.body ELSE '' END,'revision',c.version,'authorId',c.author_user_id,'authorAgentId',c.author_agent_id,'runId',c.agent_run_id,'authorName',COALESCE(a.name,u.display_name,'Former teammate'),'createdAt',c.created_at,'editedAt',c.edited_at,'deleted',c.deleted_at IS NOT NULL) ORDER BY c.created_at,c.id) FROM review_comments c LEFT JOIN users u ON u.id=c.author_user_id LEFT JOIN agents a ON a.id=c.author_agent_id WHERE c.thread_id=t.id),'[]'::jsonb)) AS payload
FROM review_threads t WHERE t.company_id=$1 AND t.project_id=$2 AND (CASE WHEN sqlc.narg(page_id)::uuid IS NOT NULL THEN t.page_id=sqlc.narg(page_id)::uuid ELSE t.page_id IS NULL AND t.artifact_version_id=$3 END) ORDER BY t.created_at,t.id LIMIT 100;

-- name: ListCanvasDecisions :many
SELECT jsonb_build_object('id',d.id,'versionId',d.version_id,'action',d.action,'feedback',d.feedback,'actorName',COALESCE(u.display_name,'Former teammate'),'createdAt',d.created_at) AS payload FROM artifact_review_decisions d LEFT JOIN users u ON u.id=d.actor_user_id
WHERE d.company_id=$1 AND d.project_id=$2 AND d.artifact_id=$3 ORDER BY d.created_at DESC LIMIT 100;

-- name: ListCanvasAccessibleVersions :many
SELECT v.id,v.artifact_id,a.accepted_version_id FROM canvas_version_sources v
JOIN project_artifacts a ON a.id=v.artifact_id AND a.archived_at IS NULL
LEFT JOIN wiki_revisions w ON w.id=v.wiki_revision_id
LEFT JOIN wiki_pages page ON page.id=v.wiki_page_id
LEFT JOIN asset_versions av ON av.id=v.asset_version_id
WHERE v.company_id=$1 AND v.project_id=$2
AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=$3))
AND ((w.id IS NOT NULL) OR (av.id IS NOT NULL) OR
 EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=$3));

-- name: CountCanvasItems :one
SELECT count(*) FROM canvas_items WHERE project_id=$1 AND deleted_at IS NULL;

-- name: ListCanvasArtifactItems :many
SELECT id FROM canvas_items WHERE project_id=$1 AND artifact_id=$2 AND deleted_at IS NULL;

-- name: LockCanvasEdge :one
SELECT * FROM canvas_edges WHERE company_id=$1 AND project_id=$2 AND id=$3 AND deleted_at IS NULL FOR UPDATE;

-- name: CountCanvasArtifacts :one
SELECT count(*) FROM project_artifacts WHERE project_id=$1 AND archived_at IS NULL;

-- name: CountCanvasVersions :one
SELECT count(*) FROM canvas_version_sources WHERE artifact_id=$1;

-- name: CountReviewThreads :one
SELECT count(*) FROM review_threads WHERE CASE WHEN sqlc.narg(page_id)::uuid IS NOT NULL THEN page_id=sqlc.narg(page_id)::uuid ELSE page_id IS NULL AND artifact_version_id=$1 END;

-- name: CountReviewComments :one
SELECT count(*) FROM review_comments WHERE thread_id=$1;

-- name: AdvanceCanvasArtifact :exec
UPDATE project_artifacts SET version=version+1,updated_at=clock_timestamp() WHERE id=$1;

-- name: ListCanvasSceneVersions :many
SELECT jsonb_build_object('id',v.id,'artifactId',v.artifact_id,'number',v.number,'sourceType',v.source_type,'branchTitle',v.branch_title,'wikiPageId',v.wiki_page_id,'wikiRevisionId',v.wiki_revision_id,'assetVersionId',v.asset_version_id,'outputId',v.output_id,'sourceOutputId',v.access_output_id,'outputItemId',(SELECT d.output_item_id FROM output_documents d WHERE d.page_id=v.wiki_page_id),'runId',COALESCE(o.run_id,(SELECT origin.source_run_id FROM project_artifacts origin WHERE origin.id=v.artifact_id)),'attachmentId',av.attachment_id,'filename',COALESCE(av.filename,primary_file.filename),'contentType',CASE WHEN v.source_type='wiki' THEN 'text/markdown' ELSE COALESCE(av.content_type,primary_file.content_type) END,'sizeBytes',COALESCE(av.size_bytes,primary_file.size_bytes),'excerpt',left(COALESCE(wr.body,o.summary,''),1600),'createdAt',v.created_at,'baseVersionId',v.base_version_id,'openComments',(SELECT count(*) FROM review_threads t WHERE t.artifact_version_id=v.id AND t.page_id IS NULL AND t.status='open')) AS payload FROM canvas_version_sources v LEFT JOIN wiki_revisions wr ON wr.id=v.wiki_revision_id
LEFT JOIN asset_versions av ON av.id=v.asset_version_id
LEFT JOIN outputs o ON o.id=v.output_id
LEFT JOIN LATERAL (
 SELECT f.filename,f.content_type,f.size_bytes FROM output_items oi
 JOIN asset_versions f ON f.id=oi.asset_version_id WHERE oi.output_id=o.id
 ORDER BY (oi.role='primary') DESC,oi.ordinal LIMIT 1
) primary_file ON true
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid AND v.id=ANY(sqlc.arg(ids)::uuid[]) AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.wiki_revision_id IS NOT NULL AND wr.id IS NOT NULL) OR
(v.asset_version_id IS NOT NULL AND av.id IS NOT NULL) OR
(v.output_id IS NOT NULL AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid))) ORDER BY v.number DESC;


-- name: CountVersionComments :one
SELECT count(*) FROM review_comments c JOIN review_threads t ON t.id=c.thread_id WHERE CASE WHEN sqlc.narg(page_id)::uuid IS NOT NULL THEN t.page_id=sqlc.narg(page_id)::uuid ELSE t.page_id IS NULL AND t.artifact_version_id=$1 END;

-- A Whiteboard is live content in its chat, not a result a Project canvas
-- places.
-- name: LockCanvasOutput :one
SELECT id FROM outputs WHERE company_id=$1 AND id=$2 AND type<>'whiteboard' FOR SHARE;

-- name: GetCanvasDraftSequence :one
SELECT head_seq FROM wiki_canvas_drafts WHERE company_id=$1 AND page_id=$2;

-- name: CountCanvasEdges :one
SELECT count(*) FROM canvas_edges WHERE project_id=$1 AND deleted_at IS NULL;

-- name: CanvasAnchorSource :one
SELECT av.content_type FROM canvas_version_sources v
JOIN asset_versions av ON av.company_id=v.company_id AND av.id=sqlc.arg(asset_version_id)::uuid
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid AND v.id=sqlc.arg(version_id)::uuid
AND ((v.source_type='file' AND v.asset_version_id=av.id AND sqlc.narg(output_item_id)::uuid IS NULL)
OR (v.source_type='output' AND EXISTS(SELECT 1 FROM output_items i WHERE i.company_id=v.company_id AND i.output_id=v.output_id AND i.id=sqlc.narg(output_item_id)::uuid AND i.asset_version_id=av.id)));

-- name: GetCanvasOutputSource :one
SELECT o.title, p.id AS project_id
FROM outputs o JOIN projects p ON p.company_id=o.company_id AND p.id=o.project_id
WHERE o.company_id=$1 AND o.id=$2 AND o.task_id IS NOT NULL;

-- name: GetCanvasRegisteredOutput :one
SELECT v.id, v.artifact_id FROM canvas_version_sources v
JOIN project_artifacts a ON a.company_id=v.company_id AND a.project_id=v.project_id AND a.id=v.artifact_id
WHERE v.company_id=$1 AND v.project_id=$2 AND v.output_id=$3
ORDER BY v.created_at, v.id LIMIT 1;

-- name: PlaceCanvasOutputArtifact :many
-- The caller holds the canvas row lock. Append to the last row when there is
-- space; otherwise start below existing work. Never move an authored card.
WITH occupied AS (
 SELECT x,y,width,height FROM canvas_items
 WHERE project_id=sqlc.arg(project_id)::uuid AND deleted_at IS NULL
), bounds AS (
 SELECT count(*) AS total,GREATEST(40,COALESCE(max(y+height)+40,40))::double precision AS bottom FROM occupied
), tail AS (
 SELECT x+width+40 AS x,y FROM occupied ORDER BY y DESC,x DESC LIMIT 1
), candidate AS (
 SELECT COALESCE(t.x,40)::double precision AS x,COALESCE(t.y,40)::double precision AS y
 FROM (SELECT 1) seed LEFT JOIN tail t ON true
), position AS (
 SELECT c.*,c.x>=40 AND c.x+320<=1800 AND NOT EXISTS(
  SELECT 1 FROM occupied o WHERE c.x<o.x+o.width+20 AND c.x+340>o.x
   AND c.y<o.y+o.height+20 AND c.y+300>o.y
 ) AS fits FROM candidate c
)
INSERT INTO canvas_items(company_id,project_id,canvas_id,item_type,artifact_id,pinned_version_id,x,y,width,height,created_by_user_id)
SELECT sqlc.arg(company_id)::uuid,sqlc.arg(project_id)::uuid,sqlc.arg(canvas_id)::uuid,
 'artifact',sqlc.arg(artifact_id)::uuid,sqlc.arg(version_id)::uuid,
 CASE WHEN p.fits THEN p.x ELSE 40 END,CASE WHEN p.fits THEN p.y ELSE b.bottom END,
 320,280,sqlc.arg(actor_user_id)::uuid
FROM position p CROSS JOIN bounds b WHERE b.total<2000 RETURNING id;

-- name: ListOutputCanvasDestinations :many
SELECT DISTINCT ON (v.output_id) v.output_id, v.project_id, v.artifact_id, v.id
FROM canvas_version_sources v
JOIN project_artifacts a ON a.company_id=v.company_id AND a.project_id=v.project_id AND a.id=v.artifact_id AND a.archived_at IS NULL
JOIN outputs o ON o.company_id=v.company_id AND o.id=v.output_id
LEFT JOIN tasks t ON t.company_id=o.company_id AND t.id=o.task_id
WHERE v.company_id=sqlc.arg(company_id)::uuid
AND v.output_id=ANY(sqlc.arg(output_ids)::uuid[])
AND EXISTS(SELECT 1 FROM project_access access WHERE access.company_id=v.company_id AND access.project_id=v.project_id AND access.user_id=sqlc.arg(actor_user_id)::uuid)
ORDER BY v.output_id, (v.project_id=t.project_id) DESC NULLS LAST, v.created_at, v.id;

-- name: ListCanvasTimeline :many
WITH version_events AS (
 SELECT v.id,v.artifact_id,v.id AS version_id,v.number,a.title,
 CASE WHEN v.number=1 THEN 'created' ELSE 'version' END AS action,
 ''::text AS feedback,v.created_by_user_id AS actor_id,v.created_at
 FROM canvas_version_sources v JOIN project_artifacts a ON a.id=v.artifact_id AND a.archived_at IS NULL
 WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.project_id=sqlc.arg(project_id)::uuid
 AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.source_type='wiki' AND v.wiki_revision_id IS NOT NULL) OR (v.source_type='file' AND v.asset_version_id IS NOT NULL)
 OR (v.source_type='output' AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid)))
 AND (sqlc.arg(first_page)::boolean OR (v.created_at,v.id)<(sqlc.arg(before_time)::timestamptz,sqlc.arg(before_id)::uuid))
 ORDER BY v.created_at DESC,v.id DESC LIMIT 51
), decision_events AS (
 SELECT d.id,v.artifact_id,v.id AS version_id,v.number,a.title,d.action,d.feedback,d.actor_user_id AS actor_id,d.created_at
 FROM artifact_review_decisions d JOIN canvas_version_sources v ON v.id=d.version_id
 JOIN project_artifacts a ON a.id=v.artifact_id AND a.archived_at IS NULL
 WHERE d.company_id=sqlc.arg(company_id)::uuid AND d.project_id=sqlc.arg(project_id)::uuid
 AND (v.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=v.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((v.source_type='wiki' AND v.wiki_revision_id IS NOT NULL) OR (v.source_type='file' AND v.asset_version_id IS NOT NULL)
 OR (v.source_type='output' AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=v.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid)))
 AND (sqlc.arg(first_page)::boolean OR (d.created_at,d.id)<(sqlc.arg(before_time)::timestamptz,sqlc.arg(before_id)::uuid))
 ORDER BY d.created_at DESC,d.id DESC LIMIT 51
), entries AS (SELECT * FROM version_events UNION ALL SELECT * FROM decision_events)
SELECT jsonb_build_object('id',e.id,'artifactId',e.artifact_id,'versionId',e.version_id,'versionNumber',e.number,'title',e.title,
'action',e.action,'feedback',e.feedback,'actorName',COALESCE(u.display_name,'Former teammate'),'createdAt',e.created_at) AS payload
FROM entries e LEFT JOIN users u ON u.id=e.actor_id
ORDER BY e.created_at DESC,e.id DESC LIMIT 51;

-- name: LockCanvasPlacementOutputs :many
-- Match the source lifecycle fence used by single placement. The following
-- INSERT rechecks access after these locks, in a fresh READ COMMITTED statement.
SELECT o.id FROM outputs o WHERE o.company_id=$1 AND EXISTS(
 SELECT 1 FROM canvas_version_sources v JOIN project_artifacts a ON a.id=v.artifact_id
 WHERE v.access_output_id=o.id AND a.project_id=$2 AND a.archived_at IS NULL
) ORDER BY o.id FOR SHARE;

-- name: PlaceUnplacedCanvasArtifacts :many
WITH candidates AS (
 SELECT a.id,v.id AS version_id,row_number() OVER(ORDER BY a.created_at,a.id)-1 AS ordinal
 FROM project_artifacts a
 JOIN LATERAL (
  SELECT pv.id FROM canvas_version_sources pv WHERE pv.artifact_id=a.id
  AND (pv.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=pv.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((pv.source_type='wiki' AND pv.wiki_revision_id IS NOT NULL) OR (pv.source_type='file' AND pv.asset_version_id IS NOT NULL)
  OR (pv.source_type='output' AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=pv.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid)))
  ORDER BY pv.number DESC LIMIT 1
 ) v ON true
 WHERE a.company_id=sqlc.arg(company_id)::uuid AND a.project_id=sqlc.arg(project_id)::uuid AND a.archived_at IS NULL
 AND NOT EXISTS(
  SELECT 1 FROM canvas_items i JOIN canvas_version_sources placed ON placed.id=CASE WHEN i.version_policy='accepted' THEN a.accepted_version_id ELSE i.pinned_version_id END
  WHERE i.artifact_id=a.id AND i.deleted_at IS NULL
  AND (placed.access_output_id IS NULL OR EXISTS(SELECT 1 FROM output_access acl WHERE acl.output_id=placed.access_output_id AND acl.user_id=sqlc.arg(actor_user_id)::uuid))
AND ((placed.source_type='wiki' AND placed.wiki_revision_id IS NOT NULL) OR (placed.source_type='file' AND placed.asset_version_id IS NOT NULL)
  OR (placed.source_type='output' AND EXISTS(SELECT 1 FROM output_access oa WHERE oa.output_id=placed.output_id AND oa.user_id=sqlc.arg(actor_user_id)::uuid)))
 )
), bounds AS (
 SELECT COALESCE(max(y+height)+80,40)::double precision AS bottom FROM canvas_items WHERE project_id=sqlc.arg(project_id)::uuid AND deleted_at IS NULL
)
INSERT INTO canvas_items(company_id,project_id,canvas_id,item_type,artifact_id,pinned_version_id,x,y,width,height,created_by_user_id)
SELECT sqlc.arg(company_id)::uuid,sqlc.arg(project_id)::uuid,sqlc.arg(canvas_id)::uuid,'artifact',c.id,c.version_id,
 40+(c.ordinal%5)*360,b.bottom+(c.ordinal/5)*320,320,280,sqlc.arg(actor_user_id)::uuid
FROM candidates c CROSS JOIN bounds b RETURNING id;

-- name: ListPageCommentAnchors :many
SELECT id, anchor FROM review_threads
WHERE company_id=$1 AND page_id=$2 AND anchor->>'type'='textQuote'
ORDER BY id FOR UPDATE;

-- name: UpdatePageCommentAnchor :exec
UPDATE review_threads SET anchor=$4,version=version+1
WHERE company_id=$1 AND page_id=$2 AND id=$3;

-- name: CanvasArtifactEverPlaced :one
SELECT EXISTS(SELECT 1 FROM canvas_items WHERE company_id=$1 AND project_id=$2 AND artifact_id=$3);
