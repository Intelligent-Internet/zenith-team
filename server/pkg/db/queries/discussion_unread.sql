-- Read counts are derived from live messages, so edits/deletions cannot leave
-- stale counters. The caller holds current content authority in this transaction.
-- name: SetDiscussionReadState :one
UPDATE conversation_user_states SET main_read_seq=sqlc.arg(read_seq)::bigint,
 manual_unread_seq=sqlc.narg(manual_unread_seq)::bigint,version=version+1,updated_at=clock_timestamp()
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid
 AND grant_generation=sqlc.arg(grant_generation)::bigint RETURNING *;

-- name: SetDiscussionThreadReadState :exec
INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow,read_seq,manual_unread_seq)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(root_id)::uuid,sqlc.arg(user_id)::uuid,
 sqlc.arg(grant_generation)::bigint,'unfollowed',sqlc.arg(read_seq)::bigint,sqlc.narg(manual_unread_seq)::bigint)
ON CONFLICT(root_id,user_id) DO UPDATE SET
 grant_generation=EXCLUDED.grant_generation,
 follow=CASE WHEN conversation_thread_states.grant_generation=EXCLUDED.grant_generation THEN conversation_thread_states.follow ELSE 'unfollowed' END,
 read_seq=EXCLUDED.read_seq,manual_unread_seq=EXCLUDED.manual_unread_seq,
 version=conversation_thread_states.version+1,updated_at=clock_timestamp();

-- name: RefreshDiscussionRootUnread :exec
UPDATE conversation_user_states s SET unread_count=(SELECT count(*) FROM conversation_items i
 WHERE i.conversation_id=s.conversation_id AND i.thread_root_id IS NULL AND i.run_id IS NULL
 AND i.type IN ('user_message','agent_text') AND i.deleted_at IS NULL AND i.author_id<>s.user_id
 AND i.seq>GREATEST(m.joined_seq,LEAST(s.main_read_seq,COALESCE(s.manual_unread_seq-1,s.main_read_seq))))
FROM conversation_members m WHERE s.conversation_id=sqlc.arg(conversation_id)::uuid AND s.user_id=sqlc.arg(user_id)::uuid
 AND m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.generation=s.grant_generation AND m.status='active';

-- name: RefreshDiscussionThreadUnread :exec
UPDATE conversation_thread_states s SET unread_count=(SELECT count(*) FROM conversation_items i
 WHERE i.thread_root_id=s.root_id AND i.run_id IS NULL AND i.type IN ('user_message','agent_text') AND i.deleted_at IS NULL
 AND i.author_id<>s.user_id AND i.seq>GREATEST(m.joined_seq,LEAST(s.read_seq,COALESCE(s.manual_unread_seq-1,s.read_seq)))
 AND (s.follow<>'unfollowed' OR c.kind='human_direct' OR s.manual_unread_seq IS NOT NULL))
FROM conversation_members m, conversations c WHERE s.conversation_id=sqlc.arg(conversation_id)::uuid AND s.user_id=sqlc.arg(user_id)::uuid
 AND c.id=s.conversation_id AND m.conversation_id=s.conversation_id AND m.user_id=s.user_id
 AND m.generation=s.grant_generation AND m.status='active';

-- name: EnsureDirectThreadReadStates :exec
INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow,read_seq)
SELECT m.company_id,m.conversation_id,sqlc.arg(root_id)::uuid,m.user_id,m.generation,'auto',m.joined_seq
FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id AND c.kind='human_direct'
WHERE m.conversation_id=sqlc.arg(conversation_id)::uuid AND m.status='active'
ON CONFLICT(root_id,user_id) DO UPDATE SET
 grant_generation=EXCLUDED.grant_generation,follow='auto',read_seq=EXCLUDED.read_seq,
 manual_unread_seq=NULL,unread_count=0,version=conversation_thread_states.version+1,updated_at=clock_timestamp()
WHERE conversation_thread_states.grant_generation<>EXCLUDED.grant_generation;

-- name: GetDiscussionReadPosition :one
SELECT seq FROM conversation_items WHERE conversation_id=sqlc.arg(conversation_id)::uuid
 AND thread_root_id IS NOT DISTINCT FROM sqlc.narg(root_id)::uuid AND seq=sqlc.arg(seq)::bigint AND run_id IS NULL;

-- name: ListDiscussionThreadReadStates :many
SELECT s.root_id,s.unread_count,s.manual_unread_seq FROM conversation_thread_states s
JOIN conversation_members m ON m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.generation=s.grant_generation AND m.status='active'
WHERE s.company_id=sqlc.arg(company_id)::uuid AND s.conversation_id=sqlc.arg(conversation_id)::uuid
 AND s.user_id=sqlc.arg(user_id)::uuid AND s.root_id=ANY(sqlc.arg(root_ids)::uuid[]);

-- The Conversation lock serializes send/delete with read and grant mutations.
-- Increment only counters whose current grant and effective cursor include this item.
-- name: ApplyDiscussionRootUnreadDelta :exec
UPDATE conversation_user_states s SET unread_count=GREATEST(0,s.unread_count+sqlc.arg(delta)::bigint)
FROM conversation_members m
WHERE s.conversation_id=sqlc.arg(conversation_id)::uuid AND s.user_id<>sqlc.arg(author_id)::uuid
 AND m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.status='active' AND m.generation=s.grant_generation
 AND sqlc.arg(seq)::bigint>GREATEST(m.joined_seq,LEAST(s.main_read_seq,COALESCE(s.manual_unread_seq-1,s.main_read_seq)));

-- name: ApplyDiscussionThreadUnreadDelta :exec
UPDATE conversation_thread_states s SET unread_count=GREATEST(0,s.unread_count+sqlc.arg(delta)::bigint)
FROM conversation_members m,conversations c
WHERE s.conversation_id=sqlc.arg(conversation_id)::uuid AND s.root_id=sqlc.arg(root_id)::uuid AND s.user_id<>sqlc.arg(author_id)::uuid
 AND c.id=s.conversation_id AND (s.follow<>'unfollowed' OR c.kind='human_direct' OR s.manual_unread_seq IS NOT NULL)
 AND m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.status='active' AND m.generation=s.grant_generation
 AND sqlc.arg(seq)::bigint>GREATEST(m.joined_seq,LEAST(s.read_seq,COALESCE(s.manual_unread_seq-1,s.read_seq)));
