-- name: ListArtifactLibraryMetadata :many
-- Artifact access and source access are independent: an explicit share does
-- not disclose its private source Conversation or Project.
SELECT output.id,
 COALESCE(owner_account.display_name, 'Former member')::text AS owner_name,
 COALESCE(output.owner_user_id=sqlc.arg(actor_user_id)::uuid, false)::boolean AS owned,
 (access.access_level>=3)::boolean AS can_share,
 CASE WHEN source.id IS NULL THEN 'restricted' WHEN source.kind='human_direct' THEN 'direct' ELSE source.kind END::text AS source_type,
 COALESCE(task.title, source.title, source_agent.name,
   (SELECT string_agg(participant.name, ', ' ORDER BY participant.name) FROM (
      SELECT person.display_name AS name FROM conversation_members member
      JOIN users person ON person.id=member.user_id
      WHERE member.company_id=source.company_id AND member.conversation_id=source.id
        AND member.status='active' AND member.user_id<>sqlc.arg(actor_user_id)::uuid
      UNION ALL
      SELECT agent.name FROM conversation_agents member
      JOIN agents agent ON agent.company_id=member.company_id AND agent.id=member.agent_id
      WHERE member.company_id=source.company_id AND member.conversation_id=source.id AND member.status='active'
   ) participant), '')::text AS source_title,
 COALESCE(project.title, '')::text AS source_detail
FROM outputs output
JOIN output_access access ON access.company_id=output.company_id AND access.output_id=output.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
LEFT JOIN runs run ON run.company_id=output.company_id AND run.id=output.run_id
LEFT JOIN channel_invocations invocation ON invocation.company_id=run.company_id AND invocation.id=run.invocation_id
LEFT JOIN conversations source ON source.company_id=output.company_id
 AND source.id=COALESCE(invocation.channel_id,output.conversation_id)
 AND (SELECT 1 FROM conversation_readers reader WHERE reader.company_id=source.company_id
      AND reader.conversation_id=source.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid LIMIT 1) IS NOT NULL
LEFT JOIN tasks task ON task.company_id=source.company_id AND task.id=source.task_id
LEFT JOIN projects project ON project.company_id=source.company_id AND project.id=source.project_id
LEFT JOIN agents source_agent ON source_agent.company_id=source.company_id AND source_agent.id=source.agent_id
LEFT JOIN users owner_account ON owner_account.id=output.owner_user_id
WHERE output.company_id=sqlc.arg(company_id)::uuid AND output.id=ANY(sqlc.arg(output_ids)::uuid[]);

-- name: GetArtifactForMessage :one
SELECT output.id
FROM outputs output
JOIN output_access access ON access.company_id=output.company_id AND access.output_id=output.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid AND access.access_level>=3
WHERE output.company_id=sqlc.arg(company_id)::uuid AND output.id=sqlc.arg(output_id)::uuid
FOR SHARE OF output;

-- name: ListRunChannelArtifacts :many
-- The Artifact references a channel or group-DM Run freezes: those its
-- caller, the Run's Authorizer, may read. The Run uses its Authorizer's
-- access and nothing else narrows it (D31): the person who tagged the Agent
-- answers for what it shares, as for the chat files it freezes.
WITH artifact_references AS (
    SELECT link.company_id, link.message_id, link.output_id
    FROM discussion_artifacts link
    WHERE link.company_id=sqlc.arg(company_id)::uuid
      AND link.message_id=ANY(sqlc.arg(message_ids)::uuid[])
    UNION
    SELECT message.company_id, message.id AS message_id, output.id AS output_id
    FROM conversation_items message
    JOIN runs run ON run.company_id=message.company_id
      AND run.invocation_id=message.invocation_id
      AND run.id::text=message.metadata->>'runId'
    JOIN outputs output ON output.company_id=run.company_id AND output.run_id=run.id
    WHERE message.company_id=sqlc.arg(company_id)::uuid
      AND message.id=ANY(sqlc.arg(message_ids)::uuid[])
      AND message.type='agent_text' AND message.run_id IS NULL
)
SELECT reference.message_id, output.id
FROM artifact_references reference
JOIN conversation_items message ON message.company_id=reference.company_id AND message.id=reference.message_id
JOIN outputs output ON output.company_id=reference.company_id AND output.id=reference.output_id
JOIN output_access access ON access.company_id=output.company_id AND access.output_id=output.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE message.deleted_at IS NULL
ORDER BY reference.message_id, output.created_at, output.id;

-- name: ListRunChannelWhiteboards :many
-- The Whiteboards shared into the chat that tagged a Run and its caller, the
-- Run's Authorizer, may open, newest first: the Run's prompt names them.
SELECT output.id, output.title
FROM outputs output
JOIN output_conversation_grants shared ON shared.company_id=output.company_id AND shared.output_id=output.id
 AND shared.conversation_id=sqlc.arg(conversation_id)::uuid
JOIN output_access access ON access.company_id=output.company_id AND access.output_id=output.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE output.company_id=sqlc.arg(company_id)::uuid AND output.type='whiteboard'
ORDER BY output.created_at DESC, output.id DESC
LIMIT 20;

-- name: ListRunConversationWhiteboards :many
-- The Whiteboards made in the Agent or Task chat a Run answers that the Run's
-- Authorizer may open, newest first: the Run's prompt names them.
SELECT output.id, output.title
FROM outputs output
JOIN output_access access ON access.company_id=output.company_id AND access.output_id=output.id
 AND access.user_id=sqlc.arg(actor_user_id)::uuid
WHERE output.company_id=sqlc.arg(company_id)::uuid
  AND output.conversation_id=sqlc.arg(conversation_id)::uuid AND output.type='whiteboard'
ORDER BY output.created_at DESC, output.id DESC
LIMIT 20;
