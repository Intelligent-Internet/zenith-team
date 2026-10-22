-- name: CreateRetainedArtifact :one
INSERT INTO artifacts(company_id,project_id,owner_user_id,type,title,created_by_user_id,conversation_id,task_id,source_run_id)
VALUES(sqlc.arg(company_id),sqlc.narg(project_id),sqlc.narg(owner_user_id),sqlc.arg(type),sqlc.arg(title),
 sqlc.arg(created_by_user_id),sqlc.narg(conversation_id),sqlc.narg(task_id),sqlc.narg(source_run_id))
RETURNING *;

-- name: ListRetainedPresentationProjects :many
SELECT project_id FROM (
 SELECT task.project_id FROM runs run
 JOIN tasks task ON task.company_id=run.company_id AND task.id=run.task_id
 WHERE run.company_id=sqlc.arg(company_id)::uuid AND run.id=sqlc.arg(run_id)::uuid
 UNION
 SELECT artifact.project_id FROM artifacts artifact
 WHERE artifact.company_id=sqlc.arg(company_id)::uuid AND artifact.id=sqlc.narg(artifact_id)::uuid
 AND artifact.project_id IS NOT NULL
) scopes ORDER BY project_id;

-- name: CreateRetainedSnapshot :one
WITH allocated AS (
 UPDATE artifacts SET next_number=next_number+1,version=version+1,updated_at=statement_timestamp(),title=sqlc.arg(title)
 WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(artifact_id)::uuid
 RETURNING *
), snapshot AS (
 INSERT INTO artifact_snapshots(company_id,project_id,artifact_id,number,source_type,title,created_by_user_id,asset_version_id)
 SELECT company_id,project_id,id,next_number-1,'retained',title,sqlc.arg(actor_user_id)::uuid,sqlc.narg(primary_asset_version_id)::uuid
 FROM allocated RETURNING *
), content AS (
 INSERT INTO retained_snapshots(company_id,id,artifact_id,content_type,summary,payload)
 SELECT company_id,id,artifact_id,sqlc.arg(content_type)::text,sqlc.arg(summary)::text,sqlc.arg(payload)::jsonb FROM snapshot
)
SELECT * FROM snapshot;

-- name: RetainDeliverySnapshotItems :exec
INSERT INTO artifact_snapshot_items(company_id,snapshot_id,ordinal,role,label,asset_version_id)
SELECT company_id,sqlc.arg(snapshot_id)::uuid,ordinal,role,label,asset_version_id
FROM output_items WHERE company_id=sqlc.arg(company_id)::uuid AND output_id=sqlc.arg(output_id)::uuid;

-- name: LinkDeliverySnapshot :exec
INSERT INTO delivery_snapshots(company_id,output_id,snapshot_id,ordinal) VALUES($1,$2,$3,$4);

-- name: ListDeliverySnapshots :many
SELECT snapshot.* FROM delivery_snapshots link
JOIN artifact_snapshots snapshot ON snapshot.company_id=link.company_id AND snapshot.id=link.snapshot_id
WHERE link.company_id=$1 AND link.output_id=$2 ORDER BY link.ordinal;

-- name: ReadRetainedSnapshot :one
SELECT sqlc.embed(snapshot),sqlc.embed(content) FROM artifact_snapshots snapshot
JOIN retained_snapshots content ON content.company_id=snapshot.company_id AND content.id=snapshot.id
WHERE snapshot.company_id=sqlc.arg(company_id)::uuid AND snapshot.artifact_id=sqlc.arg(artifact_id)::uuid
 AND (sqlc.narg(snapshot_id)::uuid IS NULL OR snapshot.id=sqlc.narg(snapshot_id)::uuid)
ORDER BY snapshot.number DESC LIMIT 1;

-- name: ListRetainedSnapshotItems :many
SELECT item.id,item.ordinal,item.role,item.label,sqlc.embed(version)
FROM artifact_snapshot_items item JOIN asset_versions version ON version.company_id=item.company_id AND version.id=item.asset_version_id
WHERE item.company_id=$1 AND item.snapshot_id=$2 ORDER BY item.ordinal;

-- name: ReadRetainedSnapshotAttachment :one
SELECT version.attachment_id FROM artifact_snapshot_items item
JOIN retained_snapshots snapshot ON snapshot.company_id=item.company_id AND snapshot.id=item.snapshot_id
JOIN asset_versions version ON version.company_id=item.company_id AND version.id=item.asset_version_id
WHERE item.company_id=$1 AND snapshot.artifact_id=$2 AND item.snapshot_id=$3 AND item.id=$4;
