-- name: ListMessageConnectorAgents :many
SELECT agents.id, agents.name FROM agents
WHERE agents.company_id = sqlc.arg(company_id)::uuid AND agents.archived_at IS NULL
AND (
  (sqlc.arg(conversation_id)::uuid = '00000000-0000-0000-0000-000000000000' AND agents.id = sqlc.arg(agent_id)::uuid)
  OR EXISTS (
    SELECT 1 FROM conversations c
    WHERE c.company_id = agents.company_id AND c.id = sqlc.arg(conversation_id)::uuid
      AND c.archived_at IS NULL AND c.document_thread_id IS NULL
      AND EXISTS (SELECT 1 FROM conversation_readers reader WHERE reader.company_id = c.company_id AND reader.conversation_id = c.id AND reader.user_id = sqlc.arg(actor_user_id)::uuid)
      AND (
        (c.kind = 'direct' AND c.agent_id = agents.id AND c.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
        OR (c.kind IN ('channel', 'human_direct')
          AND EXISTS (SELECT 1 FROM conversation_members m WHERE m.conversation_id = c.id AND m.user_id = sqlc.arg(actor_user_id)::uuid AND m.status = 'active')
          AND EXISTS (SELECT 1 FROM conversation_agents g WHERE g.conversation_id = c.id AND g.agent_id = agents.id AND g.status = 'active'))
      )
  )
)
ORDER BY agents.id;

-- name: ListExecutableMessageConnectors :many
SELECT catalog.id, catalog.source, catalog.native_id, observations.name,
       observations.icon_url, policies.version AS policy_revision, COALESCE(observations.contract_digest, '')::text AS contract_digest
FROM connector_catalog_items AS catalog
JOIN connector_policies AS policies ON policies.connector_id = catalog.id
JOIN connector_runtime_observations AS observations
 ON observations.connector_id = catalog.id AND observations.company_id = catalog.company_id
 AND observations.runtime_id = sqlc.arg(runtime_id)::uuid
JOIN connector_discovery_generations AS generations
 ON generations.runtime_id = observations.runtime_id AND generations.generation = observations.generation
WHERE catalog.company_id = sqlc.arg(company_id)::uuid
 AND catalog.provider = sqlc.arg(provider)::text
 AND generations.observed_at >= clock_timestamp() - sqlc.arg(fresh_milliseconds)::bigint * interval '1 millisecond'
 AND policies.desired_enabled AND NOT policies.review_required
 AND policies.reviewed_contract_digest = observations.contract_digest
 AND catalog.status = 'connected' AND catalog.can_execute
 AND catalog.contract_digest = observations.contract_digest
 AND catalog.tool_namespace = observations.tool_namespace
 AND observations.status = 'connected' AND observations.can_execute
 AND CASE catalog.provider WHEN 'codex' THEN catalog.source = 'codex_app'
   WHEN 'claude' THEN catalog.source IN ('claude_plugin', 'claude_mcp') ELSE false END
ORDER BY observations.name, catalog.id;

-- name: InsertMessageConnector :exec
INSERT INTO message_connectors (company_id, conversation_id, request_key, selected_by_user_id,
 position, connector_id, name, policy_revision, source, native_id, contract_digest, start_offset, end_offset)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- name: InsertMessageConnectorBinding :exec
INSERT INTO message_connector_bindings (company_id, conversation_id, request_key, selected_by_user_id, agent_id, runtime_id)
VALUES ($1,$2,$3,$4,$5,$6);

-- name: ListMessageConnectors :many
SELECT * FROM message_connectors WHERE company_id=$1 AND conversation_id=$2
 AND request_key=$3 AND selected_by_user_id=$4 ORDER BY position;

-- name: GetMessageConnectorBinding :one
SELECT * FROM message_connector_bindings WHERE company_id=$1 AND conversation_id=$2
 AND request_key=$3 AND selected_by_user_id=$4 AND agent_id=$5;

-- name: GetInvocationConnectorMessage :one
SELECT conversation_items.conversation_id, conversation_items.request_key, conversation_items.author_id
FROM channel_invocations JOIN conversation_items ON conversation_items.id = channel_invocations.trigger_message_id
WHERE channel_invocations.company_id=$1 AND channel_invocations.id=$2;

-- name: ListMessageConnectorReferencesBulk :many
SELECT conversation_items.id AS message_id, message_connectors.* FROM conversation_items
JOIN message_connectors ON message_connectors.conversation_id=conversation_items.conversation_id
 AND message_connectors.request_key=conversation_items.request_key
 AND message_connectors.selected_by_user_id=conversation_items.author_id
WHERE conversation_items.company_id=$1 AND conversation_items.id=ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY conversation_items.id, message_connectors.position;

-- name: SelectMessageRunConnectors :one
UPDATE runs SET connector_selection_mode='exact', context=context || jsonb_build_object('messageConnectorSelection', true, 'messageConnectorRuntimeId', sqlc.arg(runtime_id)::uuid) WHERE id=$1 AND company_id=$2 RETURNING *;

-- name: InsertMessageRunConnector :exec
INSERT INTO run_connector_selections (run_id, connector_id, policy_revision, source, native_id, contract_digest)
VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (run_id, connector_id) DO NOTHING;

-- name: ListSelectedRunConnectorIDs :many
SELECT connector_id FROM run_connector_selections WHERE run_id=$1;
