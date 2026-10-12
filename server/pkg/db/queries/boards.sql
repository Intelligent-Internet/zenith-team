-- name: CreateBoard :one
INSERT INTO boards(company_id,title,purpose,created_by) VALUES($1,$2,$3,$4) RETURNING *;
-- name: LockBoard :one
SELECT * FROM boards WHERE company_id=$1 AND id=$2 FOR UPDATE;
-- Reads run in REPEATABLE READ snapshots, so they never lock the Board row.
-- name: ReadBoard :one
SELECT * FROM boards WHERE company_id=$1 AND id=$2;
-- name: ListBoards :many
SELECT b.*, access.access, EXISTS(SELECT 1 FROM board_favorites f WHERE f.board_id=b.id AND f.user_id=sqlc.arg(actor_user_id))::boolean AS favorite
FROM boards b JOIN board_access access ON access.company_id=b.company_id AND access.board_id=b.id AND access.user_id=sqlc.arg(actor_user_id) WHERE b.company_id=sqlc.arg(company_id)
 AND ((b.archived_at IS NOT NULL)=sqlc.arg(archived)::boolean)
 AND (NOT sqlc.arg(favorites)::boolean OR EXISTS(SELECT 1 FROM board_favorites f WHERE f.board_id=b.id AND f.user_id=sqlc.arg(actor_user_id)))
 AND (sqlc.arg(search)::text='' OR b.title ILIKE '%'||sqlc.arg(search)||'%' ESCAPE '\' OR b.purpose ILIKE '%'||sqlc.arg(search)||'%' ESCAPE '\'
  OR (sqlc.arg(note_query)::text<>'' AND b.id IN (SELECT n.board_id FROM board_notes n JOIN board_items i ON i.id=n.item_id
   WHERE n.company_id=sqlc.arg(company_id) AND i.deleted_at IS NULL AND to_tsvector('simple',n.plain_text) @@ to_tsquery('simple',sqlc.arg(note_query)))))
ORDER BY b.updated_at DESC,b.id LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;
-- name: UpdateBoard :one
UPDATE boards SET title=$3,purpose=$4,archived_at=$5,revision=revision+1,updated_at=now() WHERE company_id=$1 AND id=$2 AND revision=$6 RETURNING *;
-- name: BoardFavorite :one
SELECT EXISTS(SELECT 1 FROM board_favorites WHERE board_id=$1 AND user_id=$2)::boolean;
-- name: SetBoardFavorite :exec
INSERT INTO board_favorites(company_id,board_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING;
-- name: DeleteBoardFavorite :exec
DELETE FROM board_favorites WHERE board_id=$1 AND user_id=$2;
-- name: GetBoardCreateReceipt :one
SELECT * FROM board_create_receipts WHERE company_id=$1 AND actor_user_id=$2 AND request_key=$3;
-- name: InsertBoardCreateReceipt :exec
INSERT INTO board_create_receipts(company_id,actor_user_id,request_key,board_id,request_hash) VALUES($1,$2,$3,$4,$5);
-- name: GetBoardReceipt :one
SELECT request_hash,seq,ids FROM board_operations WHERE board_id=$1 AND actor_user_id=$2 AND actor_run_id=$3 AND request_key=$4;
-- name: InsertBoardReceipt :exec
INSERT INTO board_operations(company_id,board_id,actor_user_id,actor_run_id,request_key,request_hash,seq,ids) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: GetBoardChange :one
SELECT changes FROM board_changes WHERE board_id=$1 AND seq=$2;
-- Recent activity orders the library to the minute, so a burst of commits
-- updates only head_seq and stays a heap-only update.
-- name: AdvanceBoardHead :one
UPDATE boards SET head_seq=head_seq+1,updated_at=CASE WHEN updated_at < now()-interval '1 minute' THEN now() ELSE updated_at END
WHERE company_id=$1 AND id=$2 RETURNING head_seq;
-- name: InsertBoardChange :exec
INSERT INTO board_changes(company_id,board_id,seq,changes) VALUES($1,$2,$3,$4);
-- The feed keeps at least the last recent window and the latest records; a
-- hard floor bounds a Board under continuous typing.
-- name: CompactBoardChanges :exec
DELETE FROM board_changes WHERE board_id=sqlc.arg(board_id)::uuid
 AND (seq <= sqlc.arg(hard_floor)::bigint OR (seq <= sqlc.arg(floor)::bigint AND created_at < sqlc.arg(cutoff)::timestamptz));
-- name: ListBoardChanges :many
SELECT seq,changes FROM board_changes WHERE board_id=$1 AND seq>$2 ORDER BY seq LIMIT 5001;
-- name: CountBoardItems :one
SELECT count(*) FROM board_items WHERE board_id=$1 AND deleted_at IS NULL;
-- name: CountBoardEdges :one
SELECT count(*) FROM board_edges WHERE board_id=$1 AND deleted_at IS NULL;
-- name: CreateBoardItem :one
INSERT INTO board_items(company_id,board_id,type,x,y,width,height,rotation,stack_order,parent_frame_id,reading_order,properties,created_by,updated_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13) RETURNING *;
-- name: GetBoardItem :one
SELECT * FROM board_items WHERE company_id=$1 AND board_id=$2 AND id=$3;
-- name: MoveBoardItem :one
UPDATE board_items SET x=$4,y=$5,width=$6,height=$7,rotation=$8,stack_order=$9,parent_frame_id=$10,reading_order=$11,geometry_revision=geometry_revision+1,updated_by=$12,updated_at=now()
WHERE company_id=$1 AND board_id=$2 AND id=$3 AND deleted_at IS NULL AND geometry_revision=$13 RETURNING *;
-- name: UpdateBoardItemContent :one
UPDATE board_items SET properties=$4,content_revision=content_revision+1,updated_by=$5,updated_at=now()
WHERE company_id=$1 AND board_id=$2 AND id=$3 AND deleted_at IS NULL AND content_revision=$6 RETURNING *;
-- name: SetBoardItemDeleted :one
UPDATE board_items SET deleted_at=$4,lifecycle_revision=lifecycle_revision+1,content_revision=content_revision+1,geometry_revision=geometry_revision+1,updated_by=$5,updated_at=now()
WHERE company_id=$1 AND board_id=$2 AND id=$3 AND content_revision=$6 RETURNING *;
-- name: CreateBoardNote :exec
INSERT INTO board_notes(company_id,board_id,item_id,state,plain_text) VALUES($1,$2,$3,$4,$5);
-- name: GetBoardNote :one
SELECT * FROM board_notes WHERE company_id=$1 AND board_id=$2 AND item_id=$3;
-- name: UpdateBoardNote :one
UPDATE board_notes SET state=$4,plain_text=$5,text_revision=text_revision+1,updated_at=now() WHERE company_id=$1 AND board_id=$2 AND item_id=$3 RETURNING text_revision;
-- name: CreateBoardStroke :exec
INSERT INTO board_strokes(company_id,board_id,item_id,points) VALUES($1,$2,$3,$4);
-- name: CreateBoardEdge :one
INSERT INTO board_edges(company_id,board_id,source_id,target_id,label,relation,created_by,updated_by,
    line,start_marker,end_marker,route,anchor_y,source_label,target_label)
VALUES($1,$2,$3,$4,$5,$6,$7,$7,sqlc.arg(line),sqlc.arg(start_marker),sqlc.arg(end_marker),sqlc.arg(route),
    sqlc.narg(anchor_y),sqlc.arg(source_label),sqlc.arg(target_label)) RETURNING *;
-- name: GetBoardEdge :one
SELECT * FROM board_edges WHERE company_id=$1 AND board_id=$2 AND id=$3;
-- name: UpdateBoardEdge :one
UPDATE board_edges SET label=$4,relation=$5,revision=revision+1,updated_by=$6,updated_at=now(),
    line=sqlc.arg(line),start_marker=sqlc.arg(start_marker),end_marker=sqlc.arg(end_marker),route=sqlc.arg(route),
    anchor_y=sqlc.narg(anchor_y),source_label=sqlc.arg(source_label),target_label=sqlc.arg(target_label)
WHERE company_id=$1 AND board_id=$2 AND id=$3 AND revision=$7 AND deleted_at IS NULL RETURNING *;
-- name: SetBoardEdgeDeleted :one
UPDATE board_edges SET deleted_at=$4,revision=revision+1,updated_by=$5,updated_at=now() WHERE company_id=$1 AND board_id=$2 AND id=$3 AND revision=$6 RETURNING *;
-- name: DeleteBoardItemEdges :many
UPDATE board_edges SET deleted_at=now(),revision=revision+1,updated_by=$4,updated_at=now() WHERE company_id=$1 AND board_id=$2 AND (source_id=$3 OR target_id=$3) AND deleted_at IS NULL RETURNING id;
-- name: DetachBoardChildren :many
UPDATE board_items SET parent_frame_id=NULL,geometry_revision=geometry_revision+1,updated_by=$4,updated_at=now() WHERE company_id=$1 AND board_id=$2 AND parent_frame_id=$3 AND deleted_at IS NULL RETURNING id;
-- name: ListBoardItems :many
SELECT i.*,coalesce(n.text_revision,0)::bigint AS text_revision,coalesce(left(n.plain_text,256),'')::text AS text,(length(coalesce(n.plain_text,''))>256)::boolean AS text_truncated,
coalesce(ca.name,cu.display_name,'Former member')::text AS creator_name,
coalesce(ua.name,uu.display_name,'Former member')::text AS updater_name,
fa.filename AS file_filename,fa.content_type AS file_content_type,fa.size_bytes AS file_size_bytes,
f.width AS file_width,f.height AS file_height,(f.preview_attachment_id IS NOT NULL)::boolean AS file_has_preview
FROM board_items i LEFT JOIN board_notes n ON n.item_id=i.id
LEFT JOIN board_files f ON f.board_id=i.board_id AND f.attachment_id=i.attachment_id
LEFT JOIN attachments fa ON fa.company_id=f.company_id AND fa.id=f.attachment_id
LEFT JOIN users cu ON cu.id=i.created_by AND cu.deleted_at IS NULL
LEFT JOIN users uu ON uu.id=i.updated_by AND uu.deleted_at IS NULL
LEFT JOIN agents ca ON ca.company_id=i.company_id AND ca.id=i.created_by_agent_id
LEFT JOIN agents ua ON ua.company_id=i.company_id AND ua.id=i.updated_by_agent_id WHERE i.company_id=$1 AND i.board_id=$2 AND (sqlc.arg(include_deleted)::boolean OR i.deleted_at IS NULL) AND (cardinality(sqlc.arg(item_ids)::uuid[])=0 OR i.id=ANY(sqlc.arg(item_ids)::uuid[])) ORDER BY i.reading_order,i.stack_order,i.id;
-- name: ListBoardEdges :many
SELECT e.*,coalesce(ca.name,cu.display_name,'Former member')::text AS creator_name,
coalesce(ua.name,uu.display_name,'Former member')::text AS updater_name
FROM board_edges e
LEFT JOIN users cu ON cu.id=e.created_by AND cu.deleted_at IS NULL
LEFT JOIN users uu ON uu.id=e.updated_by AND uu.deleted_at IS NULL
LEFT JOIN agents ca ON ca.company_id=e.company_id AND ca.id=e.created_by_agent_id
LEFT JOIN agents ua ON ua.company_id=e.company_id AND ua.id=e.updated_by_agent_id
WHERE e.company_id=$1 AND e.board_id=$2 AND (sqlc.arg(include_deleted)::boolean OR e.deleted_at IS NULL) AND (cardinality(sqlc.arg(item_ids)::uuid[])=0 OR e.id=ANY(sqlc.arg(item_ids)::uuid[])) ORDER BY e.id;

-- name: GetBoardMemberAccess :one
SELECT access FROM board_access WHERE company_id=$1 AND board_id=$2 AND user_id=$3;
-- name: SetBoardMemberAccess :exec
INSERT INTO board_member_access(company_id,board_id,user_id,access) VALUES($1,$2,$3,$4)
ON CONFLICT (board_id,user_id) DO UPDATE SET access=excluded.access;
-- name: DeleteBoardMemberAccess :exec
DELETE FROM board_member_access WHERE company_id=$1 AND board_id=$2 AND user_id=$3;
-- The member's own grant on one Board, without the creator or Company arms.
-- name: GetBoardMemberGrant :one
SELECT access FROM board_member_access WHERE company_id=$1 AND board_id=$2 AND user_id=$3;
-- Board grants go with the membership (ADR 0017): a rejoin restores none.
-- name: DeleteMemberBoardGrants :many
DELETE FROM board_member_access WHERE company_id=$1 AND user_id=$2 RETURNING board_id;
-- A Board nobody active manages: its creator left and no active member holds
-- a manage grant. Company Whiteboard managers may claim it.
-- name: ListUnownedBoards :many
SELECT b.* FROM boards b WHERE b.company_id=sqlc.arg(company_id)::uuid
 AND NOT EXISTS(SELECT 1 FROM board_access a WHERE a.company_id=b.company_id AND a.board_id=b.id AND a.access='manage')
ORDER BY b.updated_at DESC,b.id LIMIT sqlc.arg(row_limit)::int OFFSET sqlc.arg(row_offset)::int;
-- name: ClaimUnownedBoard :one
UPDATE boards SET revision=revision+1,updated_at=now()
WHERE boards.company_id=sqlc.arg(company_id)::uuid AND boards.id=sqlc.arg(id)::uuid AND boards.revision=sqlc.arg(revision)::bigint
 AND NOT EXISTS(SELECT 1 FROM board_access a WHERE a.company_id=boards.company_id AND a.board_id=boards.id AND a.access='manage')
RETURNING *;
-- name: SetBoardAgentAccess :exec
INSERT INTO board_agent_access(company_id,board_id,agent_id,access)
VALUES($1,$2,$3,$4) ON CONFLICT (board_id,agent_id) DO UPDATE SET access=excluded.access;
-- name: DeleteBoardAgentAccess :exec
DELETE FROM board_agent_access WHERE company_id=$1 AND board_id=$2 AND agent_id=$3;
-- name: GetBoardAgentAccess :one
SELECT access FROM board_agent_access WHERE company_id=$1 AND board_id=$2 AND agent_id=$3;
-- name: CountBoardAgentGrants :one
-- An archived Agent leaves the Board's chat, so it holds no place in it.
SELECT count(*) FROM board_agent_access g
JOIN agents a ON a.company_id=g.company_id AND a.id=g.agent_id AND a.archived_at IS NULL
WHERE g.company_id=$1 AND g.board_id=$2;
-- name: ListBoardAccess :many
SELECT g.user_id::uuid AS actor_id,'user'::text AS actor_type,coalesce(u.display_name,'Former member')::text AS name,g.access,u.avatar_url
FROM board_member_access g LEFT JOIN users u ON u.id=g.user_id AND u.deleted_at IS NULL
WHERE g.company_id=$1 AND g.board_id=$2
UNION ALL
SELECT g.agent_id::uuid,'agent'::text,a.name,g.access,NULL::text AS avatar_url FROM board_agent_access g
JOIN agents a ON a.company_id=g.company_id AND a.id=g.agent_id
WHERE g.company_id=$1 AND g.board_id=$2 ORDER BY name,actor_type,actor_id;
-- name: BoardOwnerProfile :one
SELECT coalesce(u.display_name,'Former member')::text AS name,u.avatar_url FROM boards b LEFT JOIN users u ON u.id=b.created_by AND u.deleted_at IS NULL WHERE b.company_id=$1 AND b.id=$2;
-- name: ListBoardsForAgent :many
SELECT sqlc.embed(b),g.access,access.access AS member_access FROM boards b
JOIN board_agent_access g ON g.company_id=b.company_id AND g.board_id=b.id
JOIN board_access access ON access.company_id=b.company_id AND access.board_id=b.id AND access.user_id=sqlc.arg(actor_user_id)
WHERE b.company_id=sqlc.arg(company_id) AND g.agent_id=sqlc.arg(agent_id) AND b.archived_at IS NULL ORDER BY b.updated_at DESC,b.id LIMIT 101 OFFSET sqlc.arg(row_offset)::int;
-- name: ListBoardAudience :many
SELECT access.user_id FROM board_access access JOIN company_members m ON m.company_id=access.company_id AND m.user_id=access.user_id
WHERE access.company_id=$1 AND access.board_id=$2 AND m.role=ANY(sqlc.arg(actor_roles)::text[]) ORDER BY access.user_id;
-- name: AdvanceBoardAccessRevision :exec
UPDATE boards SET revision=revision+1,updated_at=now() WHERE company_id=$1 AND id=$2;
-- name: SetBoardVisibility :exec
UPDATE boards SET visibility=$3,company_access=$4,
public_token=CASE WHEN visibility='public' AND $3='public' THEN public_token WHEN $3='public' THEN gen_random_uuid() ELSE NULL END,
updated_at=now() WHERE company_id=$1 AND id=$2;
-- name: ReadPublicBoard :one
SELECT b.* FROM boards b JOIN companies c ON c.id=b.company_id AND c.deleting_at IS NULL WHERE b.public_token=$1 AND b.visibility='public' AND b.archived_at IS NULL;
-- One statement stamps the actual actor on every row a batch touched.
-- name: StampBoardItemsAttribution :exec
UPDATE board_items SET updated_by=sqlc.arg(updated_by)::uuid,updated_at=now(),
updated_by_agent_id=sqlc.narg(agent_id)::uuid,updated_by_run_id=sqlc.narg(run_id)::uuid,
created_by_agent_id=CASE WHEN id=ANY(sqlc.arg(created_ids)::uuid[]) THEN sqlc.narg(agent_id)::uuid ELSE created_by_agent_id END,
created_by_run_id=CASE WHEN id=ANY(sqlc.arg(created_ids)::uuid[]) THEN sqlc.narg(run_id)::uuid ELSE created_by_run_id END
WHERE company_id=sqlc.arg(company_id)::uuid AND board_id=sqlc.arg(board_id)::uuid AND id=ANY(sqlc.arg(ids)::uuid[]);
-- name: StampBoardEdgesAttribution :exec
UPDATE board_edges SET updated_by_agent_id=sqlc.narg(agent_id)::uuid,updated_by_run_id=sqlc.narg(run_id)::uuid,
created_by_agent_id=CASE WHEN id=ANY(sqlc.arg(created_ids)::uuid[]) THEN sqlc.narg(agent_id)::uuid ELSE created_by_agent_id END,
created_by_run_id=CASE WHEN id=ANY(sqlc.arg(created_ids)::uuid[]) THEN sqlc.narg(run_id)::uuid ELSE created_by_run_id END
WHERE company_id=sqlc.arg(company_id)::uuid AND board_id=sqlc.arg(board_id)::uuid AND id=ANY(sqlc.arg(ids)::uuid[]);
-- name: ListBoardHydration :many
SELECT i.id,i.type,i.deleted_at,i.lifecycle_revision,n.state,n.plain_text,n.text_revision,s.points
FROM board_items i LEFT JOIN board_notes n ON n.item_id=i.id LEFT JOIN board_strokes s ON s.item_id=i.id
WHERE i.company_id=$1 AND i.board_id=$2 AND i.id=ANY(sqlc.arg(item_ids)::uuid[]);
-- name: ListBoardNoteTextSizes :many
SELECT item_id,octet_length(plain_text)::bigint AS text_bytes FROM board_notes WHERE company_id=$1 AND board_id=$2;
-- name: ListBoardNoteTexts :many
SELECT item_id,plain_text FROM board_notes
WHERE company_id=$1 AND board_id=$2 AND item_id=ANY(sqlc.arg(item_ids)::uuid[]);
-- Run-backed reads check the same live Run fence without write locks.
-- name: GetBoardRunReader :one
SELECT r.agent_id,r.status,r.execution_authorized_by_user_id,r.task_id,r.task_execution_generation,
 c.kind AS conversation_kind,t.assignee_user_id,t.execution_generation
FROM runs r JOIN conversations c ON c.company_id=r.company_id AND c.id=r.conversation_id
LEFT JOIN tasks t ON t.company_id=r.company_id AND t.id=r.task_id
WHERE r.company_id=$1 AND r.id=$2;
-- A Run writing a Board holds its Task and Run against a handoff or a status
-- change for the length of the write, without their write locks: the Run's
-- transcript and its channel's queue keep moving. Task before Run, as in
-- execution.
-- name: LockBoardRunTask :one
SELECT assignee_user_id,execution_generation FROM tasks WHERE company_id=$1 AND id=$2 FOR SHARE;
-- name: LockBoardRun :one
SELECT r.agent_id,r.status,r.execution_authorized_by_user_id,r.task_id,r.task_execution_generation,c.kind AS conversation_kind
FROM runs r JOIN conversations c ON c.company_id=r.company_id AND c.id=r.conversation_id
WHERE r.company_id=$1 AND r.id=$2 FOR SHARE OF r;
