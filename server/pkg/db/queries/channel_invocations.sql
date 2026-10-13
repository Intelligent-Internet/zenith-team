-- name: EnsureChannelAgentConversation :one
INSERT INTO conversations(company_id,kind,channel_id,agent_id,created_by_user_id,title)
SELECT a.company_id,'channel_agent',sqlc.arg(channel_id)::uuid,a.id,a.created_by_user_id,a.name
FROM agents a WHERE a.id=sqlc.arg(agent_id)::uuid AND a.company_id=sqlc.arg(company_id)::uuid
  AND a.archived_at IS NULL AND a.created_by_user_id IS NOT NULL
ON CONFLICT(channel_id,agent_id) WHERE kind='channel_agent' DO UPDATE SET title=EXCLUDED.title
RETURNING *;

-- name: GetChannelAgentParent :one
SELECT channel_id::uuid FROM conversations
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(conversation_id)::uuid AND kind = 'channel_agent';

-- name: InsertChannelInvocation :one
INSERT INTO channel_invocations(company_id,channel_id,conversation_id,agent_id,trigger_message_id,thread_root_id,
    caller_user_id,agent_generation,member_generation,channel_generation,trigger_content,trigger_revision)
SELECT c.company_id,c.id,sqlc.arg(conversation_id)::uuid,g.agent_id,i.id,COALESCE(i.thread_root_id,i.id),
    m.user_id,g.generation,m.generation,c.channel_execution_generation,i.content,i.revision
FROM conversations c
JOIN conversation_agents g ON g.conversation_id=c.id AND g.agent_id=sqlc.arg(agent_id)::uuid AND g.status='active'
JOIN conversation_members m ON m.conversation_id=c.id AND m.user_id=sqlc.arg(caller_user_id)::uuid AND m.status='active'
JOIN conversation_items i ON i.conversation_id=c.id AND i.id=sqlc.arg(message_id)::uuid AND i.author_id=m.user_id AND i.author_kind='user'
WHERE c.id=sqlc.arg(channel_id)::uuid AND c.company_id=sqlc.arg(company_id)::uuid AND c.kind IN ('channel','human_direct') AND c.archived_at IS NULL
RETURNING *;

-- name: ChannelInvocationAuthorized :one
SELECT EXISTS(
    SELECT 1 FROM channel_invocations v
    JOIN conversations c ON c.id=v.channel_id AND c.archived_at IS NULL AND c.channel_execution_generation=v.channel_generation
    JOIN conversation_agents g ON g.conversation_id=c.id AND g.agent_id=v.agent_id AND g.status='active' AND g.generation=v.agent_generation
    JOIN conversation_members m ON m.conversation_id=c.id AND m.user_id=v.caller_user_id AND m.status='active' AND m.generation=v.member_generation
    JOIN company_members cm ON cm.company_id=v.company_id AND cm.user_id=v.caller_user_id AND cm.status='active' AND cm.role=ANY(sqlc.arg(actor_roles)::text[])
    JOIN users u ON u.id=v.caller_user_id AND u.disabled_at IS NULL AND u.deleted_at IS NULL
    JOIN agents a ON a.id=v.agent_id AND a.archived_at IS NULL
    WHERE v.id=sqlc.arg(invocation_id)::uuid AND v.status<>'cancelled'
)::boolean;

-- name: GetChannelInvocation :one
SELECT * FROM channel_invocations WHERE id=sqlc.arg(invocation_id)::uuid AND company_id=sqlc.arg(company_id)::uuid;

-- name: GetActiveChannelInvocation :one
SELECT * FROM channel_invocations WHERE conversation_id=$1 AND status IN ('active','waiting_input') LIMIT 1;

-- name: NextChannelInvocation :one
SELECT * FROM channel_invocations WHERE conversation_id=$1 AND status='queued' ORDER BY queued_at,id LIMIT 1 FOR UPDATE;

-- name: SetChannelInvocationState :one
UPDATE channel_invocations SET status=sqlc.arg(status)::text,publication_status=sqlc.arg(publication_status)::text,
    failure_reason=sqlc.narg(failure_reason)::text,updated_at=clock_timestamp()
WHERE id=sqlc.arg(invocation_id)::uuid RETURNING *;

-- name: ListChannelInvocations :many
-- runtime_status is the status of the machine the latest Run was placed on,
-- and only its registrant reads it: a tag runs on the tagger's computer, which
-- is theirs alone (ADR 0014), so every other viewer gets NULL.
SELECT v.*,a.name AS agent_name,a.created_by_user_id AS manager_user_id,
    COALESCE(u.display_name,'Deleted user')::text AS manager_name,
    COALESCE(r.status,'')::text AS run_status,rt.status AS runtime_status,
    ARRAY(SELECT question.id FROM questions question JOIN runs source ON source.id=question.origin_run_id
          WHERE source.invocation_id=v.id AND question.status='pending' AND question.origin_user_id=sqlc.arg(actor_user_id)::uuid
          UNION ALL
          SELECT approval.id FROM approvals approval JOIN runs source ON source.id=approval.origin_run_id
          WHERE source.invocation_id=v.id AND approval.status='pending' AND approval.origin_user_id=sqlc.arg(actor_user_id)::uuid)::uuid[] AS input_request_ids
FROM channel_invocations v JOIN agents a ON a.id=v.agent_id
LEFT JOIN users u ON u.id=a.created_by_user_id
LEFT JOIN LATERAL (SELECT rr.* FROM runs rr WHERE rr.invocation_id=v.id ORDER BY rr.created_at DESC,rr.id DESC LIMIT 1) r ON true
LEFT JOIN runtimes rt ON rt.id=r.runtime_id AND rt.owner_user_id=sqlc.arg(actor_user_id)::uuid
WHERE v.company_id=sqlc.arg(company_id)::uuid AND v.channel_id=sqlc.arg(channel_id)::uuid
ORDER BY v.created_at DESC,v.id DESC LIMIT 200;

-- name: ListChannelInvocationConversations :many
SELECT * FROM conversations WHERE channel_id=$1 ORDER BY id FOR NO KEY UPDATE;

-- name: ListUnfinishedChannelInvocations :many
SELECT * FROM channel_invocations WHERE channel_id=$1 AND (status IN ('queued','active','waiting_input') OR publication_status='pending') ORDER BY conversation_id,id;

-- name: ChannelInvocationHasPendingInput :one
SELECT EXISTS(
    SELECT 1 FROM questions q JOIN runs r ON r.id=q.origin_run_id
    WHERE r.invocation_id=$1 AND (q.status='pending' OR q.continuation_status='pending')
    UNION ALL
    SELECT 1 FROM approvals a JOIN runs r ON r.id=a.origin_run_id
    WHERE r.invocation_id=$1 AND (a.status='pending' OR a.continuation_status='pending')
)::boolean;

-- name: GetChannelFinalAnswer :one
-- The transcript append records the Run's latest proven final answer; the
-- (run_id, item_key) index finds its item.
SELECT i.* FROM conversation_items i
WHERE i.run_id=sqlc.arg(run_id)::uuid AND i.type='agent_text'
    AND i.item_key=(SELECT r.final_answer_item_key FROM runs r WHERE r.id=sqlc.arg(run_id)::uuid);

-- name: InsertChannelAnswer :one
INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,content,body,thread_root_id,invocation_id,metadata)
SELECT v.company_id,v.channel_id,sqlc.arg(seq)::bigint,'agent_text','agent',v.agent_id,sqlc.arg(content)::text,
    jsonb_build_object('version',1,'markdown',sqlc.arg(content)::text,'references','[]'::jsonb),v.thread_root_id,v.id,
    jsonb_build_object('invocationId',v.id,'runId',sqlc.arg(run_id)::uuid,'finalItemKey',sqlc.arg(final_item_key)::text)
FROM channel_invocations v WHERE v.id=sqlc.arg(invocation_id)::uuid
RETURNING *;

-- name: ListChannelContext :many
SELECT i.id,i.seq,i.revision,i.content,i.created_at,i.author_kind,i.author_id,
    COALESCE(a.name,u.display_name,'Deleted user')::text AS author_name
FROM conversation_items i
LEFT JOIN users u ON u.id=i.author_id AND i.author_kind='user'
LEFT JOIN agents a ON a.id=i.author_id AND i.author_kind='agent'
WHERE i.conversation_id=sqlc.arg(channel_id)::uuid AND i.deleted_at IS NULL
    AND i.type IN ('user_message','agent_text') AND i.id<>sqlc.arg(trigger_message_id)::uuid
ORDER BY (i.id=sqlc.arg(thread_root_id)::uuid OR i.thread_root_id=sqlc.arg(thread_root_id)::uuid) DESC,i.seq DESC LIMIT 64;

-- name: RetryChannelInvocation :one
UPDATE channel_invocations SET status='queued',publication_status='pending',failure_reason=NULL,
    request_key=sqlc.arg(request_key)::uuid,queued_at=clock_timestamp(),updated_at=clock_timestamp()
WHERE id=sqlc.arg(invocation_id)::uuid AND status='failed' AND publication_status<>'published'
RETURNING *;

-- name: ListActiveInvocationRuns :many
SELECT * FROM runs WHERE invocation_id=$1 AND status IN ('queued','deferred','dispatched','waiting_local_directory','running') ORDER BY conversation_id,id;

-- name: GetChannelContextHead :one
SELECT head_seq,channel_context_revision,channel_execution_generation FROM conversations WHERE id=$1;

-- name: GetChannelPriorSession :one
SELECT session_id,context,runtime_id FROM runs
WHERE conversation_id=sqlc.arg(conversation_id)::uuid AND id<>sqlc.arg(run_id)::uuid
  AND session_id IS NOT NULL AND NOT session_rollout_missing AND status IN ('completed','failed') AND invocation_id IS NOT NULL
ORDER BY updated_at DESC,id DESC LIMIT 1;

-- name: InsertChannelInvocationPrompt :exec
INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,content)
SELECT v.company_id,v.conversation_id,sqlc.arg(run_id)::uuid,
    COALESCE((SELECT max(seq) FROM conversation_items WHERE conversation_id=v.conversation_id),0)+1,
    'user_message','user',v.caller_user_id,v.trigger_content
FROM channel_invocations v WHERE v.id=sqlc.arg(invocation_id)::uuid;

-- name: ListChannelAgentActivity :many
-- A tagged Run works for the person who tagged the Agent, with their access
-- (D31), so each viewer reads only the items of the Runs they started: never
-- another tagger's requests or tool output, whoever created the Agent.
SELECT i.id,i.run_id,r.invocation_id,i.type,i.content,i.parts,i.created_at
FROM conversation_items i JOIN runs r ON r.id=i.run_id
JOIN conversations c ON c.id=i.conversation_id
WHERE c.company_id=sqlc.arg(company_id)::uuid AND c.channel_id=sqlc.arg(channel_id)::uuid AND c.agent_id=sqlc.arg(agent_id)::uuid
  AND r.company_id=c.company_id AND r.execution_authorized_by_user_id=sqlc.arg(viewer_user_id)::uuid
  AND i.type<>'reasoning'
ORDER BY i.seq DESC LIMIT 200;

-- name: ListChannelAgentRoster :many
-- The runtime columns are the viewer's own setup of each Agent (ADR 0014):
-- viewer_bound is GetAgentRuntimeBindingForRun's predicate, so the roster
-- offers a tag exactly when the tag would enqueue, and runtime_status is the
-- viewer's own computer, never another member's.
SELECT a.id,a.name,a.created_by_user_id,COALESCE(u.display_name,'Deleted user')::text AS manager_name,
    r.status AS runtime_status,
    (r.id IS NOT NULL AND owners.id IS NOT NULL AND memberships.user_id IS NOT NULL)::boolean AS viewer_bound
FROM conversation_agents g JOIN agents a ON a.id=g.agent_id
LEFT JOIN users u ON u.id=a.created_by_user_id
LEFT JOIN agent_runtime_bindings binding ON binding.company_id=a.company_id AND binding.agent_id=a.id
    AND binding.user_id=sqlc.arg(viewer_user_id)::uuid
LEFT JOIN runtimes r ON r.company_id=binding.company_id AND r.id=binding.runtime_id
    AND r.removed_at IS NULL AND r.owner_user_id=binding.user_id
LEFT JOIN users owners ON owners.id=binding.user_id AND owners.disabled_at IS NULL
LEFT JOIN company_members memberships ON memberships.company_id=binding.company_id
    AND memberships.user_id=binding.user_id AND memberships.status='active'
WHERE g.conversation_id=sqlc.arg(channel_id)::uuid AND g.status='active' AND a.archived_at IS NULL ORDER BY a.name,a.id;

-- name: RefreshChannelRunContext :one
UPDATE runs SET context=sqlc.arg(context)::jsonb
WHERE id=sqlc.arg(run_id)::uuid AND invocation_id IS NOT NULL AND status='dispatched' RETURNING *;
