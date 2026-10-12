-- Callers authorize content within the same snapshot, and hold the Conversation
-- lock for every mutation. IDs alone never provide access to these queries.
-- name: GetDiscussionMessage :one
SELECT i.*, CASE WHEN i.author_kind='agent' THEN COALESCE((SELECT name FROM agents WHERE id=i.author_id),'Archived Agent') WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE COALESCE(u.display_name,'System') END::text AS author_display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS author_avatar_url
FROM conversation_items i LEFT JOIN users u ON u.id=i.author_id AND i.author_kind='user'
WHERE i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid
    AND i.id=sqlc.arg(message_id)::uuid AND i.run_id IS NULL;

-- name: EditDiscussionMessage :exec
UPDATE conversation_items SET body=sqlc.arg(body)::jsonb,content=sqlc.arg(content)::text,
    edited_at=clock_timestamp(),revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid AND id=sqlc.arg(message_id)::uuid;

-- name: DeleteDiscussionMessage :exec
UPDATE conversation_items SET body=NULL,content='',deleted_at=clock_timestamp(),revision=revision+1
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid AND id=sqlc.arg(message_id)::uuid;

-- name: ListDiscussionThreadSummaries :many
SELECT thread_root_id,count(*)::bigint AS reply_count,max(created_at)::timestamptz AS last_reply_at
FROM conversation_items WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid
    AND thread_root_id=ANY(sqlc.arg(message_ids)::uuid[]) AND deleted_at IS NULL
GROUP BY thread_root_id;

-- name: ListDiscussionThreadAuthors :many
WITH authors AS (
    SELECT thread_root_id,author_id,author_kind,max(seq) AS latest FROM conversation_items
    WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid
        AND thread_root_id=ANY(sqlc.arg(message_ids)::uuid[]) AND deleted_at IS NULL
    GROUP BY thread_root_id,author_id,author_kind
), ranked AS (
    SELECT *,row_number() OVER(PARTITION BY thread_root_id ORDER BY latest DESC) AS rank FROM authors
)
SELECT r.thread_root_id,r.author_id,r.author_kind,
    CASE WHEN r.author_kind='agent' THEN COALESCE(a.name,'Archived Agent') WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE COALESCE(u.display_name,'Deleted user') END::text AS display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS avatar_url
FROM ranked r LEFT JOIN users u ON u.id=r.author_id AND r.author_kind='user' LEFT JOIN agents a ON a.id=r.author_id AND r.author_kind='agent' WHERE r.rank<=3 ORDER BY r.thread_root_id,r.rank;

-- name: ListDiscussionReactionSummaries :many
SELECT message_id,emoji,count(*)::bigint AS count,bool_or(user_id=sqlc.arg(actor_user_id)::uuid)::boolean AS reacted
FROM conversation_message_reactions
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid
    AND message_id=ANY(sqlc.arg(message_ids)::uuid[])
GROUP BY message_id,emoji ORDER BY message_id,emoji;

-- name: ListDiscussionPinnedIDs :many
SELECT message_id FROM conversation_message_pins WHERE company_id=sqlc.arg(company_id)::uuid
    AND conversation_id=sqlc.arg(conversation_id)::uuid AND message_id=ANY(sqlc.arg(message_ids)::uuid[]);

-- name: GetDiscussionAttachmentReference :one
SELECT attachments.id
FROM message_attachments
JOIN conversation_items ON conversation_items.id=message_attachments.message_id
JOIN conversations ON conversations.id=conversation_items.conversation_id
JOIN attachments ON attachments.id=message_attachments.attachment_id
WHERE message_attachments.attachment_id=sqlc.arg(attachment_id)::uuid
  AND conversations.company_id=sqlc.arg(company_id)::uuid
  AND conversations.id=sqlc.arg(conversation_id)::uuid
  AND conversation_items.deleted_at IS NULL
  AND attachments.status='ready';

-- name: PinDiscussionMessage :execrows
INSERT INTO conversation_message_pins(company_id,conversation_id,message_id,pinned_by_user_id)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(message_id)::uuid,sqlc.arg(actor_user_id)::uuid)
ON CONFLICT(conversation_id,message_id) DO NOTHING;

-- name: UnpinDiscussionMessage :execrows
DELETE FROM conversation_message_pins WHERE company_id=sqlc.arg(company_id)::uuid
    AND conversation_id=sqlc.arg(conversation_id)::uuid AND message_id=sqlc.arg(message_id)::uuid;

-- name: AddDiscussionReaction :execrows
INSERT INTO conversation_message_reactions(company_id,conversation_id,message_id,user_id,emoji)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(message_id)::uuid,sqlc.arg(user_id)::uuid,sqlc.arg(emoji)::text)
ON CONFLICT(message_id,user_id,emoji) DO NOTHING;

-- name: RemoveDiscussionReaction :execrows
DELETE FROM conversation_message_reactions WHERE company_id=sqlc.arg(company_id)::uuid
    AND conversation_id=sqlc.arg(conversation_id)::uuid AND message_id=sqlc.arg(message_id)::uuid
    AND user_id=sqlc.arg(user_id)::uuid AND emoji=sqlc.arg(emoji)::text;

-- name: ClearDiscussionReactions :exec
DELETE FROM conversation_message_reactions WHERE company_id=sqlc.arg(company_id)::uuid
    AND conversation_id=sqlc.arg(conversation_id)::uuid AND message_id=sqlc.arg(message_id)::uuid;

-- name: ListDiscussionReactionUsers :many
SELECT r.emoji,r.user_id,
    CASE WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE u.display_name END::text AS display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS avatar_url
FROM conversation_message_reactions r JOIN users u ON u.id=r.user_id
WHERE r.company_id=sqlc.arg(company_id)::uuid AND r.conversation_id=sqlc.arg(conversation_id)::uuid AND r.message_id=sqlc.arg(message_id)::uuid
    AND (sqlc.narg(seek_user_id)::uuid IS NULL OR (r.emoji,r.user_id)>(sqlc.arg(seek_emoji)::text,sqlc.narg(seek_user_id)::uuid))
ORDER BY r.emoji,r.user_id LIMIT sqlc.arg(page_limit)::int;

-- name: ListDiscussionPins :many
SELECT p.* FROM conversation_message_pins p
WHERE p.company_id=sqlc.arg(company_id)::uuid AND p.conversation_id=sqlc.arg(conversation_id)::uuid
    AND (sqlc.narg(seek_time)::timestamptz IS NULL OR (p.pinned_at,p.message_id)<(sqlc.narg(seek_time)::timestamptz,sqlc.narg(seek_id)::uuid))
ORDER BY p.pinned_at DESC,p.message_id DESC LIMIT sqlc.arg(page_limit)::int;

-- name: CountDiscussionPins :one
SELECT count(*) FROM conversation_message_pins WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid;

-- name: GetDiscussionMessagesByIDs :many
SELECT i.*, CASE WHEN i.author_kind='agent' THEN COALESCE((SELECT name FROM agents WHERE id=i.author_id),'Archived Agent') WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE COALESCE(u.display_name,'System') END::text AS author_display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS author_avatar_url
FROM conversation_items i LEFT JOIN users u ON u.id=i.author_id AND i.author_kind='user'
WHERE i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid
    AND i.id=ANY(sqlc.arg(message_ids)::uuid[]) AND i.run_id IS NULL;

-- name: GetDiscussionThreadState :one
SELECT s.* FROM conversation_thread_states s JOIN conversation_members m
    ON m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.generation=s.grant_generation AND m.status='active'
WHERE s.company_id=sqlc.arg(company_id)::uuid AND s.conversation_id=sqlc.arg(conversation_id)::uuid
    AND s.root_id=sqlc.arg(root_id)::uuid AND s.user_id=sqlc.arg(user_id)::uuid;

-- Explicit state CAS is checked by the service under the Conversation lock.
-- A stale grant is invisible; its replacement keeps a monotonic stored version.
-- name: SetDiscussionThreadState :one
INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow,read_seq)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(root_id)::uuid,sqlc.arg(user_id)::uuid,
    sqlc.arg(grant_generation)::bigint,sqlc.arg(follow)::text,sqlc.arg(read_seq)::bigint)
ON CONFLICT(root_id,user_id) DO UPDATE SET updated_at=clock_timestamp(),grant_generation=EXCLUDED.grant_generation,follow=EXCLUDED.follow,
    manual_unread_seq=CASE WHEN conversation_thread_states.grant_generation=EXCLUDED.grant_generation THEN conversation_thread_states.manual_unread_seq END,
    read_seq=CASE WHEN conversation_thread_states.grant_generation<>EXCLUDED.grant_generation THEN EXCLUDED.read_seq ELSE conversation_thread_states.read_seq END,
    version=conversation_thread_states.version+1 RETURNING *;

-- Sending a new reply explicitly opts its author back in; replay/edit never call this.
-- Nonparticipant owner sends do not fabricate a grant or subscription.
-- name: AutoFollowDiscussionAuthor :exec
INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow,read_seq)
SELECT m.company_id,m.conversation_id,sqlc.arg(root_id)::uuid,m.user_id,m.generation,'auto',
    GREATEST(m.joined_seq,sqlc.arg(previous_head_seq)::bigint)
FROM conversation_members m WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.conversation_id=sqlc.arg(conversation_id)::uuid
    AND m.user_id=sqlc.arg(user_id)::uuid AND m.status='active'
ON CONFLICT(root_id,user_id) DO UPDATE SET updated_at=clock_timestamp(),grant_generation=EXCLUDED.grant_generation,
    follow=CASE WHEN conversation_thread_states.grant_generation=EXCLUDED.grant_generation AND conversation_thread_states.follow='following' THEN 'following' ELSE 'auto' END,
    read_seq=CASE WHEN conversation_thread_states.grant_generation<>EXCLUDED.grant_generation OR conversation_thread_states.follow='unfollowed' THEN EXCLUDED.read_seq ELSE conversation_thread_states.read_seq END,
    manual_unread_seq=CASE WHEN conversation_thread_states.grant_generation<>EXCLUDED.grant_generation THEN NULL ELSE conversation_thread_states.manual_unread_seq END,
    unread_count=CASE WHEN conversation_thread_states.grant_generation<>EXCLUDED.grant_generation OR conversation_thread_states.manual_unread_seq IS NULL THEN 0 ELSE conversation_thread_states.unread_count END,
    version=conversation_thread_states.version+1
WHERE conversation_thread_states.grant_generation<>EXCLUDED.grant_generation OR (conversation_thread_states.follow='unfollowed' AND sqlc.arg(opt_back_in)::boolean);

-- name: GetDiscussionThreadHead :one
SELECT COALESCE(max(seq),sqlc.arg(root_seq)::bigint)::bigint FROM conversation_items
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid AND thread_root_id=sqlc.arg(root_id)::uuid;

-- name: TouchDiscussionUserState :one
UPDATE conversation_user_states SET version=version+1,updated_at=clock_timestamp()
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid RETURNING *;

-- name: ListDiscussionNotificationFollowers :many
SELECT s.user_id FROM conversation_thread_states s
JOIN conversation_members m ON m.conversation_id=s.conversation_id AND m.user_id=s.user_id
    AND m.generation=s.grant_generation AND m.status='active'
WHERE s.company_id=sqlc.arg(company_id)::uuid AND s.conversation_id=sqlc.arg(conversation_id)::uuid
    AND s.root_id=sqlc.arg(root_id)::uuid AND s.follow IN ('auto','following');
