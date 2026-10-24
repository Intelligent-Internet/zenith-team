-- name: RegisterBoardFile :exec
INSERT INTO board_files(company_id,board_id,attachment_id,preview_attachment_id,width,height,created_by)
VALUES(sqlc.arg(company_id),sqlc.arg(board_id),sqlc.arg(attachment_id),sqlc.narg(preview_attachment_id),
 sqlc.narg(width),sqlc.narg(height),sqlc.arg(created_by))
ON CONFLICT (company_id,board_id,attachment_id) DO NOTHING;
-- name: GetBoardFile :one
SELECT f.*,a.filename,a.content_type,a.size_bytes FROM board_files f
JOIN attachments a ON a.company_id=f.company_id AND a.id=f.attachment_id
WHERE f.company_id=$1 AND f.board_id=$2 AND f.attachment_id=$3;
-- A public link serves only files that a live item still places.
-- name: GetPublicBoardFile :one
SELECT f.*,a.filename,a.content_type,a.size_bytes FROM board_files f
JOIN attachments a ON a.company_id=f.company_id AND a.id=f.attachment_id
WHERE f.company_id=$1 AND f.board_id=$2 AND f.attachment_id=$3
 AND EXISTS (SELECT 1 FROM board_items i WHERE i.company_id=f.company_id AND i.board_id=f.board_id
  AND i.attachment_id=f.attachment_id AND i.deleted_at IS NULL);
-- A member's own ready upload that no chat, Project, Output or Board shares yet.
-- name: BoardUploadIsUnshared :one
SELECT EXISTS(SELECT 1 FROM attachments a WHERE a.company_id=$1 AND a.id=$2 AND a.uploaded_by_user_id=$3 AND a.status='ready'
AND NOT EXISTS(SELECT 1 FROM project_attachment_scopes s WHERE s.company_id=a.company_id AND s.attachment_id=a.id)
AND NOT EXISTS(SELECT 1 FROM message_attachments m WHERE m.company_id=a.company_id AND m.attachment_id=a.id)
AND NOT EXISTS(SELECT 1 FROM board_files f WHERE f.company_id=a.company_id AND (f.attachment_id=a.id OR f.preview_attachment_id=a.id))
AND NOT EXISTS(SELECT 1 FROM asset_versions v WHERE v.company_id=a.company_id AND v.attachment_id=a.id))::boolean;
-- A file already placed on another Board this member can read may be referenced
-- again (copy/paste between Boards). The copy keeps its preview and dimensions.
-- name: GetReadableBoardFileSource :one
SELECT f.* FROM board_files f
JOIN artifact_access access ON access.company_id=f.company_id AND access.artifact_id=f.board_id AND access.user_id=sqlc.arg(user_id)
JOIN attachment_access readable ON readable.company_id=f.company_id AND readable.attachment_id=f.attachment_id
 AND readable.user_id=sqlc.arg(user_id)
JOIN attachments a ON a.company_id=f.company_id AND a.id=f.attachment_id AND a.status='ready'
WHERE f.company_id=sqlc.arg(company_id) AND f.attachment_id=sqlc.arg(attachment_id)
ORDER BY f.created_at LIMIT 1;
-- name: ListBoardVisualHydration :many
SELECT i.id,coalesce(n.plain_text,'')::text AS plain_text,s.points
FROM board_items i LEFT JOIN board_notes n ON n.item_id=i.id LEFT JOIN board_strokes s ON s.item_id=i.id
WHERE i.company_id=$1 AND i.board_id=$2 AND i.deleted_at IS NULL
 AND (cardinality(sqlc.arg(item_ids)::uuid[])=0 OR i.id=ANY(sqlc.arg(item_ids)::uuid[]));
-- name: BoardVisualHydrationBytes :one
SELECT coalesce(sum(coalesce(octet_length(n.plain_text),0)+coalesce(octet_length(s.points::text),0)),0)::bigint AS bytes
FROM board_items i LEFT JOIN board_notes n ON n.item_id=i.id LEFT JOIN board_strokes s ON s.item_id=i.id
WHERE i.company_id=$1 AND i.board_id=$2 AND i.deleted_at IS NULL
 AND (cardinality(sqlc.arg(item_ids)::uuid[])=0 OR i.id=ANY(sqlc.arg(item_ids)::uuid[]));
-- Retention, in bounded batches that skip rows another replica or a live
-- transaction holds. Tombstones older than the undo horizon leave the Board
-- (a purged frame's children keep their place outside it), then files no item
-- places any longer drop their reference so ordinary attachment cleanup can
-- reclaim the bytes. The outer condition rechecks a row a restore may have
-- revived while the batch was chosen.
-- name: PurgeBoardTombstones :execrows
DELETE FROM board_items WHERE id IN (
 SELECT id FROM board_items WHERE deleted_at < sqlc.arg(cutoff)::timestamptz
 ORDER BY deleted_at LIMIT sqlc.arg(batch_size)::integer FOR UPDATE SKIP LOCKED)
 AND deleted_at < sqlc.arg(cutoff)::timestamptz;
-- name: PurgeBoardEdgeTombstones :execrows
DELETE FROM board_edges WHERE id IN (
 SELECT id FROM board_edges WHERE deleted_at < sqlc.arg(cutoff)::timestamptz
 ORDER BY deleted_at LIMIT sqlc.arg(batch_size)::integer FOR UPDATE SKIP LOCKED)
 AND deleted_at < sqlc.arg(cutoff)::timestamptz;
-- name: ReleaseUnplacedBoardFiles :execrows
DELETE FROM board_files f WHERE (f.company_id,f.board_id,f.attachment_id) IN (
 SELECT x.company_id,x.board_id,x.attachment_id FROM board_files x WHERE x.created_at < sqlc.arg(cutoff)::timestamptz
 AND NOT EXISTS (SELECT 1 FROM board_items i WHERE i.board_id=x.board_id AND i.attachment_id=x.attachment_id)
 LIMIT sqlc.arg(batch_size)::integer FOR UPDATE OF x SKIP LOCKED)
 AND NOT EXISTS (SELECT 1 FROM board_items i WHERE i.board_id=f.board_id AND i.attachment_id=f.attachment_id);
-- name: PurgeBoardReceipts :execrows
DELETE FROM board_operations WHERE (board_id,actor_user_id,actor_run_id,request_key) IN (
 SELECT board_id,actor_user_id,actor_run_id,request_key FROM board_operations
 WHERE created_at < sqlc.arg(cutoff)::timestamptz LIMIT sqlc.arg(batch_size)::integer FOR UPDATE SKIP LOCKED);
-- A file a Board places, or its preview. Another surface (a Project) refuses
-- it rather than leave its file readable by the Board's audience.
-- name: AttachmentOnBoard :one
SELECT EXISTS(SELECT 1 FROM board_files WHERE company_id=$1 AND (attachment_id=$2 OR preview_attachment_id=$2))::boolean;
