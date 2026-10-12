-- name: GetConversationBoard :one
-- The Board a tagged Run works on: its Agent chat's channel is that Board's
-- chat. Frozen into each Run, so retries and queued tags keep it.
SELECT boards.id, boards.title
FROM conversations AS chat
JOIN conversations AS channel
  ON channel.company_id = chat.company_id AND channel.id = chat.channel_id
JOIN boards ON boards.company_id = channel.company_id AND boards.id = channel.board_id
WHERE chat.company_id = sqlc.arg(company_id)::uuid
  AND chat.id = sqlc.arg(conversation_id)::uuid
  AND chat.kind = 'channel_agent';

-- name: LockBoardChannel :one
-- A Board's chat, locked as its other writers lock a discussion.
SELECT * FROM conversations
WHERE company_id = sqlc.arg(company_id)::uuid AND board_id = sqlc.arg(board_id)::uuid
FOR UPDATE;

-- name: InsertBoardChannel :one
-- Starts a Board's chat, named by the person who first opens it. A
-- concurrent start wins the race and this inserts nothing.
INSERT INTO conversations (company_id, kind, created_by_user_id, title, request_key, board_id)
SELECT boards.company_id, 'channel', sqlc.arg(actor_user_id)::uuid, 'whiteboard', gen_random_uuid(), boards.id
FROM boards
WHERE boards.company_id = sqlc.arg(company_id)::uuid AND boards.id = sqlc.arg(board_id)::uuid
ON CONFLICT (board_id) WHERE board_id IS NOT NULL DO NOTHING
RETURNING *;

-- name: GetBoardChannelID :one
-- A Board's chat, unlocked: its Board never changes.
SELECT id FROM conversations
WHERE company_id = sqlc.arg(company_id)::uuid AND board_id = sqlc.arg(board_id)::uuid;

-- name: GetDiscussionBoard :one
-- The Board a chat belongs to; NULL for any other chat. Unlocked: a chat's
-- Board never changes.
SELECT board_id FROM conversations
WHERE company_id = sqlc.arg(company_id)::uuid AND id = sqlc.arg(conversation_id)::uuid;

-- name: BoardChannelReaders :many
-- Which of these people can read the Board now.
SELECT access.user_id FROM board_access AS access
WHERE access.company_id = sqlc.arg(company_id)::uuid AND access.board_id = sqlc.arg(board_id)::uuid
  AND access.user_id = ANY(sqlc.arg(user_ids)::uuid[])
ORDER BY access.user_id;

-- name: ListBoardChannelPeople :many
-- The people who can read the Board, and whether they joined its chat.
SELECT access.user_id, users.display_name, COALESCE(users.avatar_url, '')::text AS avatar_url,
    (member.user_id IS NOT NULL)::boolean AS joined
FROM board_access AS access
JOIN users ON users.id = access.user_id
LEFT JOIN conversation_members AS member
  ON member.conversation_id = sqlc.narg(conversation_id)::uuid
 AND member.user_id = access.user_id AND member.status = 'active'
WHERE access.company_id = sqlc.arg(company_id)::uuid AND access.board_id = sqlc.arg(board_id)::uuid
ORDER BY lower(users.display_name), access.user_id
LIMIT sqlc.arg(page_limit)::int;

-- name: ListBoardChannelStaleMembers :many
-- Joined people who can no longer read the Board.
SELECT member.user_id FROM conversation_members AS member
WHERE member.conversation_id = sqlc.arg(conversation_id)::uuid AND member.status = 'active'
  AND NOT EXISTS (
      SELECT 1 FROM board_access AS access
      WHERE access.company_id = member.company_id AND access.board_id = sqlc.arg(board_id)::uuid
        AND access.user_id = member.user_id
  )
ORDER BY member.user_id;

-- name: ListBoardChannelAgentGrants :many
-- The Agents invited to the Board, the Board chat's roster.
SELECT grants.agent_id FROM board_agent_access AS grants
JOIN agents ON agents.company_id = grants.company_id AND agents.id = grants.agent_id AND agents.archived_at IS NULL
WHERE grants.company_id = sqlc.arg(company_id)::uuid AND grants.board_id = sqlc.arg(board_id)::uuid
ORDER BY grants.agent_id;

-- name: ListDiscussionActiveAgents :many
SELECT agent_id FROM conversation_agents
WHERE conversation_id = sqlc.arg(conversation_id)::uuid AND status = 'active'
ORDER BY agent_id;
