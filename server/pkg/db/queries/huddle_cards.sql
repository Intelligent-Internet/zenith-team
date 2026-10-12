-- name: InsertHuddleSourceCard :one
INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata)
VALUES(sqlc.arg(company_id)::uuid,sqlc.arg(conversation_id)::uuid,sqlc.arg(seq)::bigint,
    'discussion_system','system','{"action":"huddle_call","actorUserId":null}'::jsonb)
RETURNING id;

-- name: SetHuddleSourceCard :exec
UPDATE huddles SET source_item_id=sqlc.arg(item_id)::uuid
WHERE company_id=sqlc.arg(company_id)::uuid AND id=sqlc.arg(huddle_id)::uuid
    AND source_item_id IS NULL;

-- name: ListHuddleSourceCards :many
SELECT h.id,h.source_item_id,h.chat_conversation_id,h.state,h.created_at,h.ended_at,
    stats.participant_count,
    COALESCE(GREATEST(0,floor(extract(epoch FROM (
        stats.last_presence_at-stats.first_connected_at)))),0)::bigint AS duration_seconds,
    COALESCE(stats.first_connected_at,h.created_at)::timestamptz AS started_at
FROM huddles h
JOIN conversation_items item ON item.company_id=h.company_id AND item.id=h.source_item_id
    AND item.conversation_id=h.source_conversation_id
CROSS JOIN LATERAL (
    SELECT count(DISTINCT c.user_id)::bigint AS participant_count,
        min(c.connected_at) AS first_connected_at,
        max(LEAST(COALESCE(c.ended_at,h.ended_at,now()),COALESCE(h.ended_at,now()))) AS last_presence_at
    FROM huddle_connections c
    WHERE c.company_id=h.company_id AND c.huddle_id=h.id AND c.connected_at IS NOT NULL
) stats
WHERE h.company_id=sqlc.arg(company_id)::uuid
    AND h.source_conversation_id=sqlc.arg(conversation_id)::uuid
    AND h.source_item_id=ANY(sqlc.arg(message_ids)::uuid[]);
