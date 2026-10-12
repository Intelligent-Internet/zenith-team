-- Discussion reads use a single scoped projection under a repeatable-read
-- transaction. Management authorization never becomes content authorization.
-- name: ListDiscussionAccess :many
SELECT c.*, ch.topic, ch.configuration_revision, ch.create_fingerprint,
    pair.user_low_id, pair.user_high_id,
    cm.role AS company_role, cm.version AS company_membership_revision,
    m.generation AS grant_generation, m.version AS grant_revision,
    COALESCE(s.main_read_seq,m.joined_seq,0)::bigint AS main_read_seq, s.manual_unread_seq,
    COALESCE(s.unread_count,0)::bigint AS unread_count,
    s.version AS state_revision, s.pinned_at AS state_pinned_at,
    s.hidden_at, s.hidden_through_seq, COALESCE(s.notification_level,'all')::text AS notification_level,
    (m.user_id IS NOT NULL)::boolean AS content_access,
    (c.kind='channel' AND c.board_id IS NULL AND cm.role=ANY(sqlc.arg(manage_roles)::text[]))::boolean AS management_access,
    board.archived_at AS board_archived_at,
    (SELECT count(*) FROM conversation_members members WHERE members.conversation_id=c.id AND members.status='active')::bigint AS member_count,
    (SELECT count(*) FROM conversation_agents agents WHERE agents.conversation_id=c.id AND agents.status='active')::bigint AS agent_count,
    CASE WHEN pair.participant_ids IS NOT NULL THEN COALESCE((
        SELECT string_agg(person.name, ', ' ORDER BY person.kind,person.id) FROM (
            SELECT gm.user_id AS id, 'human' AS kind, left(u.display_name,256) AS name
            FROM conversation_members gm JOIN users u ON u.id=gm.user_id
            JOIN company_members gcm ON gcm.company_id=gm.company_id AND gcm.user_id=gm.user_id
            WHERE gm.conversation_id=c.id AND gm.company_id=c.company_id AND gm.user_id<>cm.user_id
                AND gm.status='active' AND gcm.status='active' AND u.deleted_at IS NULL AND u.disabled_at IS NULL
            UNION ALL
            SELECT ga.agent_id AS id, 'agent' AS kind, left(a.name,256) AS name
            FROM conversation_agents ga JOIN agents a ON a.id=ga.agent_id AND a.company_id=ga.company_id
            WHERE ga.conversation_id=c.id AND ga.company_id=c.company_id AND ga.status='active' AND a.archived_at IS NULL
        ) person),'Group direct message')
    WHEN peer.deleted_at IS NOT NULL OR peer.disabled_at IS NOT NULL THEN 'Deleted user'
    ELSE COALESCE(peer.display_name,'') END::text AS peer_display_name,
    COALESCE(CASE WHEN peer.deleted_at IS NULL AND peer.disabled_at IS NULL THEN peer.avatar_url END,'')::text AS peer_avatar_url,
    CASE WHEN pair.participant_ids IS NOT NULL THEN EXISTS(
        SELECT 1 FROM conversation_members gm JOIN users u ON u.id=gm.user_id
        JOIN company_members gcm ON gcm.company_id=gm.company_id AND gcm.user_id=gm.user_id
        WHERE gm.conversation_id=c.id AND gm.user_id<>cm.user_id AND gm.status='active'
            AND gcm.status='active' AND u.deleted_at IS NULL AND u.disabled_at IS NULL)
        OR EXISTS(SELECT 1 FROM conversation_agents ga JOIN agents a ON a.id=ga.agent_id
            WHERE ga.conversation_id=c.id AND ga.status='active' AND a.archived_at IS NULL)
    ELSE COALESCE(peer.deleted_at IS NULL AND peer.disabled_at IS NULL AND peer_member.status='active',false) END::boolean AS peer_active
FROM conversations c
JOIN companies company ON company.id=c.company_id AND company.deleting_at IS NULL
JOIN company_members cm ON cm.company_id=c.company_id AND cm.user_id=sqlc.arg(actor_user_id)::uuid AND cm.status='active'
JOIN users actor ON actor.id=cm.user_id AND actor.disabled_at IS NULL AND actor.deleted_at IS NULL
LEFT JOIN conversation_channels ch ON ch.conversation_id=c.id
LEFT JOIN conversation_human_directs pair ON pair.conversation_id=c.id
LEFT JOIN users peer ON peer.id=CASE WHEN pair.user_low_id=cm.user_id THEN pair.user_high_id ELSE pair.user_low_id END
LEFT JOIN company_members peer_member ON peer_member.company_id=c.company_id AND peer_member.user_id=peer.id
-- A Board's channel is read by its joined people while they can still read
-- the Board, so access lost on the Board is lost here before membership
-- catches up.
LEFT JOIN conversation_members m ON m.conversation_id=c.id AND m.user_id=cm.user_id AND m.status='active'
    AND (c.board_id IS NULL OR EXISTS(SELECT 1 FROM board_access ba
        WHERE ba.company_id=c.company_id AND ba.board_id=c.board_id AND ba.user_id=cm.user_id))
LEFT JOIN conversation_user_states s ON s.conversation_id=c.id AND s.user_id=cm.user_id AND s.grant_generation=m.generation
LEFT JOIN boards board ON board.company_id=c.company_id AND board.id=c.board_id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND (c.kind IN ('human_direct','channel') OR (c.kind='huddle' AND cardinality(sqlc.arg(conversation_ids)::uuid[])>0))
  -- Like a Huddle chat, a Board's channel lists only when asked for by ID: it
  -- lives on its Board, not in Chat.
  AND (c.board_id IS NULL OR cardinality(sqlc.arg(conversation_ids)::uuid[])>0)
  AND (cardinality(sqlc.arg(conversation_ids)::uuid[])=0 OR c.id=ANY(sqlc.arg(conversation_ids)::uuid[]))
  AND (sqlc.arg(type_filter)::text='' OR c.kind=sqlc.arg(type_filter)::text)
  AND (sqlc.arg(include_archived)::boolean OR c.archived_at IS NULL)
  AND (NOT sqlc.arg(only_archived)::boolean OR c.archived_at IS NOT NULL)
  AND (sqlc.arg(name_query)::text='' OR strpos(lower(c.title),lower(sqlc.arg(name_query)::text))>0)
  AND ((NOT sqlc.arg(manage_view)::boolean AND
        (m.user_id IS NOT NULL))
       OR (sqlc.arg(manage_view)::boolean AND c.kind='channel' AND c.board_id IS NULL
           AND cm.role=ANY(sqlc.arg(manage_roles)::text[])))
  AND (sqlc.arg(include_hidden)::boolean OR s.hidden_at IS NULL)
  AND (sqlc.narg(seek_time)::timestamptz IS NULL OR (c.updated_at,c.id)<(sqlc.narg(seek_time)::timestamptz,sqlc.narg(seek_id)::uuid))
ORDER BY c.updated_at DESC,c.id DESC LIMIT sqlc.arg(page_limit)::int;

-- name: LockDiscussionConversation :one
SELECT * FROM conversations WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(conversation_id)::uuid
    AND kind IN ('human_direct','channel','huddle') FOR UPDATE;

-- name: GetHumanDirectParticipants :one
SELECT conversation_id FROM conversation_human_directs
WHERE company_id=sqlc.arg(company_id)::uuid AND NOT membership_expanded
  AND agent_ids=sqlc.arg(agent_ids)::uuid[]
  AND ((cardinality(sqlc.arg(participant_ids)::uuid[])=2
    AND user_low_id=(sqlc.arg(participant_ids)::uuid[])[1]
    AND user_high_id=(sqlc.arg(participant_ids)::uuid[])[2])
    OR participant_ids=sqlc.arg(participant_ids)::uuid[]);

-- name: GetDiscussionCreationReceipt :one
SELECT c.*, ch.create_fingerprint FROM conversations c
LEFT JOIN conversation_channels ch ON ch.conversation_id=c.id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND c.created_by_user_id=sqlc.arg(actor_user_id)::uuid AND c.request_key=sqlc.arg(request_key)::uuid;

-- name: InsertDiscussionConversation :one
INSERT INTO conversations(company_id,kind,created_by_user_id,title,request_key)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(kind)::text,sqlc.arg(actor_user_id)::uuid,sqlc.narg(title)::text,sqlc.narg(request_key)::uuid) RETURNING *;

-- name: InsertHumanDirectParticipants :exec
INSERT INTO conversation_human_directs(company_id,conversation_id,user_low_id,user_high_id,participant_ids,agent_ids)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,
    CASE WHEN cardinality(sqlc.arg(participant_ids)::uuid[])=2 AND cardinality(sqlc.arg(agent_ids)::uuid[])=0 THEN (sqlc.arg(participant_ids)::uuid[])[1] END,
    CASE WHEN cardinality(sqlc.arg(participant_ids)::uuid[])=2 AND cardinality(sqlc.arg(agent_ids)::uuid[])=0 THEN (sqlc.arg(participant_ids)::uuid[])[2] END,
    CASE WHEN cardinality(sqlc.arg(participant_ids)::uuid[])>2 OR cardinality(sqlc.arg(agent_ids)::uuid[])>0 THEN sqlc.arg(participant_ids)::uuid[] END,
    sqlc.arg(agent_ids)::uuid[]);

-- name: InsertChannelDetail :exec
INSERT INTO conversation_channels(company_id,conversation_id,topic,create_fingerprint)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(topic)::text,sqlc.arg(create_fingerprint)::bytea);

-- name: GetDiscussionEligibleUser :one
SELECT u.id FROM users u JOIN company_members m ON m.user_id=u.id
WHERE m.company_id=sqlc.arg(company_id)::uuid AND u.id=sqlc.arg(user_id)::uuid
  AND m.status='active' AND u.disabled_at IS NULL AND u.deleted_at IS NULL;

-- name: LockDiscussionEligibleAgents :many
SELECT * FROM agents WHERE company_id=sqlc.arg(company_id)::uuid AND id=ANY(sqlc.arg(agent_ids)::uuid[])
  AND archived_at IS NULL ORDER BY id FOR SHARE;

-- name: GrantDiscussionMember :one
INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type,joined_seq,joined_by_user_id)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(user_id)::uuid,sqlc.arg(source_type)::text,sqlc.arg(joined_seq)::bigint,sqlc.narg(actor_user_id)::uuid)
ON CONFLICT(conversation_id,user_id) DO UPDATE SET status='active', generation=conversation_members.generation+1,
    version=conversation_members.version+1, joined_at=now(), joined_seq=EXCLUDED.joined_seq,
    joined_by_user_id=EXCLUDED.joined_by_user_id, revoked_at=NULL, revoked_by_user_id=NULL
WHERE conversation_members.status='revoked' RETURNING *;

-- name: GetDiscussionMember :one
SELECT * FROM conversation_members WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid;

-- name: RevokeDiscussionMember :one
UPDATE conversation_members SET status='revoked',version=version+1,revoked_at=now(),revoked_by_user_id=sqlc.narg(actor_user_id)::uuid
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid AND status='active' RETURNING *;

-- name: GrantDiscussionAgent :one
INSERT INTO conversation_agents(company_id,conversation_id,agent_id,joined_by_user_id)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(agent_id)::uuid,sqlc.narg(actor_user_id)::uuid)
ON CONFLICT(conversation_id,agent_id) DO UPDATE SET status='active',generation=conversation_agents.generation+1,
    version=conversation_agents.version+1,joined_at=now(),joined_by_user_id=EXCLUDED.joined_by_user_id,revoked_at=NULL,revoked_by_user_id=NULL
WHERE conversation_agents.status='revoked' RETURNING *;

-- name: GetDiscussionAgent :one
SELECT * FROM conversation_agents WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND agent_id=sqlc.arg(agent_id)::uuid;

-- name: RevokeDiscussionAgent :one
UPDATE conversation_agents SET status='revoked',version=version+1,revoked_at=now(),revoked_by_user_id=sqlc.narg(actor_user_id)::uuid
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND agent_id=sqlc.arg(agent_id)::uuid AND status='active' RETURNING *;

-- name: ListDiscussionMembers :many
SELECT m.*, CASE WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE u.display_name END::text AS display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS avatar_url,
    (u.disabled_at IS NULL AND u.deleted_at IS NULL AND cm.status='active' AND m.status='active')::boolean AS active
FROM conversation_members m JOIN users u ON u.id=m.user_id
JOIN company_members cm ON cm.company_id=m.company_id AND cm.user_id=m.user_id
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.conversation_id=sqlc.arg(conversation_id)::uuid
  AND (m.source_type='human_direct' OR m.status='active') ORDER BY m.user_id;

-- name: ListDiscussionAgents :many
SELECT g.*,a.name FROM conversation_agents g JOIN agents a ON a.id=g.agent_id AND a.company_id=g.company_id
WHERE g.company_id=sqlc.arg(company_id)::uuid AND g.conversation_id=sqlc.arg(conversation_id)::uuid
  AND g.status='active' AND a.archived_at IS NULL ORDER BY g.agent_id LIMIT 20;

-- name: GetDiscussionAgentHistoryGrant :one
SELECT g.* FROM conversation_agents g JOIN agents a ON a.id=g.agent_id AND a.company_id=g.company_id
JOIN conversations c ON c.id=g.conversation_id AND c.company_id=g.company_id AND c.kind IN ('channel','human_direct')
JOIN companies company ON company.id=g.company_id AND company.deleting_at IS NULL
WHERE g.company_id=sqlc.arg(company_id)::uuid AND g.conversation_id=sqlc.arg(conversation_id)::uuid
  AND g.agent_id=sqlc.arg(agent_id)::uuid AND g.status='active' AND a.archived_at IS NULL;

-- name: AllocateDiscussionSequence :one
UPDATE conversations SET head_seq=head_seq+1 WHERE id=sqlc.arg(conversation_id)::uuid AND company_id=sqlc.arg(company_id)::uuid
  AND kind IN ('human_direct','channel','huddle') AND head_seq<9007199254740991 RETURNING head_seq;

-- name: BumpDiscussionConversation :one
UPDATE conversations SET version=version+1,updated_at=clock_timestamp()
WHERE id=sqlc.arg(conversation_id)::uuid AND company_id=sqlc.arg(company_id)::uuid AND kind IN ('human_direct','channel','huddle') RETURNING *;

-- name: UpdateChannelConfiguration :one
UPDATE conversation_channels SET topic=COALESCE(sqlc.narg(topic)::text,topic),configuration_revision=configuration_revision+1
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND company_id=sqlc.arg(company_id)::uuid RETURNING *;

-- name: SetChannelMetadata :exec
UPDATE conversations SET title=COALESCE(sqlc.narg(title)::text,title),
    archived_at=CASE WHEN sqlc.narg(archived)::boolean IS NULL THEN archived_at WHEN sqlc.narg(archived)::boolean THEN COALESCE(archived_at,now()) END
WHERE id=sqlc.arg(conversation_id)::uuid AND company_id=sqlc.arg(company_id)::uuid AND kind='channel';

-- name: InsertDiscussionMessage :one
INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,request_key,body,content,send_fingerprint,thread_root_id)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(seq)::bigint,'user_message','user',sqlc.arg(author_id)::uuid,sqlc.arg(request_key)::uuid,sqlc.arg(body)::jsonb,sqlc.arg(content)::text,sqlc.arg(send_fingerprint)::bytea,sqlc.narg(thread_root_id)::uuid) RETURNING *;

-- name: InsertDiscussionSystemItem :exec
INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(seq)::bigint,'discussion_system','system',sqlc.arg(metadata)::jsonb);

-- name: GetDiscussionSendReceipt :one
SELECT * FROM conversation_items WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid
 AND author_id=sqlc.arg(author_id)::uuid AND request_key=sqlc.arg(request_key)::uuid AND run_id IS NULL;

-- name: ListDiscussionMessages :many
WITH page AS (
    (SELECT i.* FROM conversation_items i
     WHERE sqlc.narg(thread_root_id)::uuid IS NULL AND i.thread_root_id IS NULL
       AND i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid AND i.run_id IS NULL
       AND i.seq>sqlc.arg(after_seq)::bigint AND i.seq<=sqlc.arg(through_seq)::bigint
     ORDER BY i.seq DESC LIMIT sqlc.arg(page_limit)::int)
    UNION ALL
    (SELECT i.* FROM conversation_items i
     WHERE sqlc.narg(thread_root_id)::uuid IS NOT NULL AND i.thread_root_id=sqlc.narg(thread_root_id)::uuid
       AND i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid AND i.run_id IS NULL
       AND i.seq>sqlc.arg(after_seq)::bigint AND i.seq<=sqlc.arg(through_seq)::bigint
     ORDER BY i.seq DESC LIMIT sqlc.arg(page_limit)::int)
)
SELECT i.*, CASE WHEN i.author_kind='agent' THEN COALESCE((SELECT name FROM agents WHERE id=i.author_id),'Archived Agent') WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE COALESCE(u.display_name,'System') END::text AS author_display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS author_avatar_url
FROM page i LEFT JOIN users u ON u.id=i.author_id AND i.author_kind='user'
ORDER BY i.seq DESC LIMIT sqlc.arg(page_limit)::int;

-- name: ListDiscussionMessagesForward :many
WITH page AS (
    (SELECT i.* FROM conversation_items i
     WHERE sqlc.narg(thread_root_id)::uuid IS NULL AND i.thread_root_id IS NULL
       AND i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid AND i.run_id IS NULL
       AND i.seq>sqlc.arg(after_seq)::bigint AND i.seq<=sqlc.arg(through_seq)::bigint
     ORDER BY i.seq ASC LIMIT sqlc.arg(page_limit)::int)
    UNION ALL
    (SELECT i.* FROM conversation_items i
     WHERE sqlc.narg(thread_root_id)::uuid IS NOT NULL AND i.thread_root_id=sqlc.narg(thread_root_id)::uuid
       AND i.company_id=sqlc.arg(company_id)::uuid AND i.conversation_id=sqlc.arg(conversation_id)::uuid AND i.run_id IS NULL
       AND i.seq>sqlc.arg(after_seq)::bigint AND i.seq<=sqlc.arg(through_seq)::bigint
     ORDER BY i.seq ASC LIMIT sqlc.arg(page_limit)::int)
)
SELECT i.*, CASE WHEN i.author_kind='agent' THEN COALESCE((SELECT name FROM agents WHERE id=i.author_id),'Archived Agent') WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE COALESCE(u.display_name,'System') END::text AS author_display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS author_avatar_url
FROM page i LEFT JOIN users u ON u.id=i.author_id AND i.author_kind='user'
ORDER BY i.seq ASC LIMIT sqlc.arg(page_limit)::int;

-- name: ResetDiscussionUserState :exec
INSERT INTO conversation_user_states(company_id,conversation_id,user_id,grant_generation,main_read_seq,notification_level)
SELECT sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(user_id)::uuid,sqlc.arg(grant_generation)::bigint,joined_seq,
    COALESCE(sqlc.narg(notification_level)::text,'all') FROM conversation_members WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid
ON CONFLICT(conversation_id,user_id) DO UPDATE SET grant_generation=EXCLUDED.grant_generation,
    main_read_seq=EXCLUDED.main_read_seq,unread_count=0,manual_unread_seq=NULL,pinned_at=NULL,hidden_at=NULL,hidden_through_seq=0,notification_level=EXCLUDED.notification_level,version=conversation_user_states.version+1,updated_at=now();

-- name: UpdateDiscussionUserState :one
UPDATE conversation_user_states SET
    notification_level=COALESCE(sqlc.narg(notification_level)::text,notification_level),
    pinned_at=CASE WHEN sqlc.narg(pinned)::boolean IS NULL THEN pinned_at WHEN sqlc.narg(pinned)::boolean THEN COALESCE(pinned_at,now()) END,
    hidden_at=CASE WHEN sqlc.narg(hidden)::boolean IS NULL THEN hidden_at WHEN sqlc.narg(hidden)::boolean THEN COALESCE(hidden_at,now()) END,
    hidden_through_seq=CASE WHEN sqlc.narg(hidden)::boolean THEN sqlc.arg(head_seq)::bigint ELSE hidden_through_seq END,
    version=version+1,updated_at=now()
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND user_id=sqlc.arg(user_id)::uuid
    AND grant_generation=sqlc.arg(grant_generation)::bigint AND version=sqlc.arg(expected_revision)::bigint RETURNING *;

-- name: RevealDiscussionForPeerMessage :many
UPDATE conversation_user_states s SET hidden_at=NULL,version=s.version+1,updated_at=now()
FROM conversation_members m WHERE s.conversation_id=sqlc.arg(conversation_id)::uuid AND s.user_id<>sqlc.arg(author_id)::uuid
  AND m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.status='active' AND m.generation=s.grant_generation
  AND s.hidden_at IS NOT NULL AND s.hidden_through_seq<sqlc.arg(seq)::bigint RETURNING s.*;

-- name: LockDiscussionsForUserRevocation :many
SELECT c.* FROM conversations c JOIN conversation_members m ON m.conversation_id=c.id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND m.user_id=sqlc.arg(user_id)::uuid AND m.status='active'
ORDER BY c.id FOR UPDATE OF c;

-- name: LockDiscussionsForAgentRevocation :many
SELECT c.* FROM conversations c JOIN conversation_agents g ON g.conversation_id=c.id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND g.agent_id=sqlc.arg(agent_id)::uuid AND g.status='active'
ORDER BY c.id FOR UPDATE OF c;

-- name: CountActiveDiscussionMembers :one
SELECT count(*)::bigint FROM conversation_members WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND status='active';

-- name: ListDiscussionRecipients :many
SELECT m.user_id FROM conversation_members m JOIN company_members cm ON cm.company_id=m.company_id AND cm.user_id=m.user_id AND cm.status='active'
JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
JOIN companies company ON company.id=m.company_id AND company.deleting_at IS NULL
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.conversation_id=sqlc.arg(conversation_id)::uuid AND m.status='active'
ORDER BY m.user_id;

-- name: SetChannelTopic :exec
UPDATE conversation_channels SET topic=sqlc.arg(topic)::text
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid;

-- The caller holds the Conversation lock and already authorized its mutation.
-- A successful self-removal may return this receipt after losing read access.
-- name: GetChannelMutationReceipt :one
SELECT c.title,c.archived_at,ch.topic,ch.configuration_revision
FROM conversations c JOIN conversation_channels ch ON ch.conversation_id=c.id AND ch.company_id=c.company_id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND c.id=sqlc.arg(conversation_id)::uuid;

-- name: ListDiscussionMembersBulk :many
SELECT m.*, CASE WHEN u.deleted_at IS NOT NULL OR u.disabled_at IS NOT NULL THEN 'Deleted user' ELSE u.display_name END::text AS display_name,
    COALESCE(CASE WHEN u.deleted_at IS NULL AND u.disabled_at IS NULL THEN u.avatar_url END,'')::text AS avatar_url,
    (u.disabled_at IS NULL AND u.deleted_at IS NULL AND cm.status='active' AND m.status='active')::boolean AS active
FROM conversation_members m JOIN users u ON u.id=m.user_id
JOIN company_members cm ON cm.company_id=m.company_id AND cm.user_id=m.user_id
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.conversation_id=ANY(sqlc.arg(conversation_ids)::uuid[])
  AND (m.source_type='human_direct' OR m.status='active') ORDER BY m.conversation_id,m.user_id;

-- name: ListDiscussionAgentsBulk :many
SELECT g.*,a.name FROM conversation_agents g JOIN agents a ON a.id=g.agent_id AND a.company_id=g.company_id
WHERE g.company_id=sqlc.arg(company_id)::uuid AND g.conversation_id=ANY(sqlc.arg(conversation_ids)::uuid[])
  AND g.status='active' AND a.archived_at IS NULL ORDER BY g.conversation_id,g.agent_id LIMIT 2000;

-- name: GetGeneralChannelForUpdate :one
SELECT * FROM conversations WHERE company_id=sqlc.arg(company_id)::uuid AND is_general FOR UPDATE;

-- name: CreateGeneralChannel :one
INSERT INTO conversations(company_id,kind,title,request_key,is_general)
VALUES(sqlc.arg(company_id)::uuid,'channel','general',gen_random_uuid(),true) RETURNING *;

-- name: ListGeneralChannelEligibleUsers :many
SELECT m.user_id FROM company_members m JOIN users u ON u.id=m.user_id
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.status='active'
  AND u.disabled_at IS NULL AND u.deleted_at IS NULL ORDER BY m.user_id;

-- name: TouchCompanyMemberPresence :exec
UPDATE company_members SET
    presence_active_until=CASE WHEN sqlc.arg(active)::boolean THEN clock_timestamp()+interval '30 seconds' ELSE presence_active_until END,
    presence_manual_away=COALESCE(sqlc.narg(manual_away)::boolean, presence_manual_away)
WHERE company_id=sqlc.arg(company_id)::uuid AND user_id=sqlc.arg(user_id)::uuid AND status='active';

-- name: ListCompanyMemberPresence :many
SELECT m.user_id, m.presence_manual_away AS manual_away,
    (NOT m.presence_manual_away AND COALESCE(m.presence_active_until > clock_timestamp(),false))::boolean AS online
FROM company_members m JOIN users u ON u.id=m.user_id
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.user_id=ANY(sqlc.arg(user_ids)::uuid[])
    AND m.status='active' AND u.deleted_at IS NULL AND u.disabled_at IS NULL;

-- name: ListDiscussionNotificationPreferences :many
SELECT m.user_id, s.notification_level
FROM conversation_members m
JOIN conversation_user_states s ON s.conversation_id=m.conversation_id AND s.user_id=m.user_id AND s.grant_generation=m.generation
JOIN company_members cm ON cm.company_id=m.company_id AND cm.user_id=m.user_id AND cm.status='active'
JOIN users u ON u.id=m.user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.conversation_id=sqlc.arg(conversation_id)::uuid AND m.status='active';

-- name: GetHumanDirectIdentity :one
SELECT * FROM conversation_human_directs
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid;

-- name: ExpandHumanDirectParticipants :exec
UPDATE conversation_human_directs SET participant_ids=sqlc.arg(participant_ids)::uuid[],
    agent_ids=sqlc.arg(agent_ids)::uuid[], membership_expanded=true
WHERE company_id=sqlc.arg(company_id)::uuid AND conversation_id=sqlc.arg(conversation_id)::uuid
    AND participant_ids IS NOT NULL;
