-- name: SearchCompany :many
WITH search_input AS MATERIALIZED (
    SELECT
        lower(sqlc.arg(query)::text) AS query,
        to_tsquery('simple', sqlc.arg(ts_query)::text) AS ts_query
), task_matches AS MATERIALIZED (
    SELECT
        'task'::text AS result_kind,
        tasks.id AS entity_id,
        tasks.id AS task_id,
        tasks.conversation_id,
        NULL::bigint AS message_seq,
        'task'::text AS conversation_kind,
        tasks.key,
        tasks.number AS task_number,
        tasks.title,
        tasks.status,
        tasks.priority,
        assignee_agents.id AS agent_id,
        assignee_agents.name AS agent_name,
        NULL::text AS author_kind,
        NULL::uuid AS author_id,
        NULL::text AS author_display_name,
        NULL::text AS author_avatar_url,
        NULL::text AS snippet_source,
        tasks.created_at,
        tasks.updated_at,
        'task'::text AS route_kind,
        -- The label a person reads off the board ({task_prefix}-{number})
        -- counts like the key: an exact hit ranks first, a prefix hit next.
        CASE
            WHEN lower(tasks.key) = search_input.query
              OR lower(companies.task_prefix || '-' || tasks.number::text) = search_input.query
              OR lower(tasks.title) = search_input.query THEN 0
            WHEN lower(tasks.key) LIKE search_input.query || '%'
              OR lower(companies.task_prefix || '-' || tasks.number::text) LIKE search_input.query || '%'
              OR lower(tasks.title) LIKE search_input.query || '%' THEN 1
            ELSE 2
        END::integer AS rank_bucket,
        0::real AS rank_score
    FROM tasks
    CROSS JOIN search_input
    JOIN companies ON companies.id = tasks.company_id AND companies.deleting_at IS NULL
    -- Only Tasks of Projects the caller reads (ADR 0017); project_access also
    -- asserts the active membership. Joined on the Task's own Project, so the
    -- caller's readable Projects are reached through the tasks row already
    -- in hand instead of a second pass over tasks.
    JOIN project_access
      ON project_access.company_id = tasks.company_id
     AND project_access.project_id = tasks.project_id
     AND project_access.user_id = sqlc.arg(actor_user_id)::uuid
    LEFT JOIN agents AS assignee_agents
      ON assignee_agents.id = tasks.executed_by_agent_id
     AND assignee_agents.company_id = tasks.company_id
     AND assignee_agents.archived_at IS NULL
    WHERE sqlc.arg(include_tasks)::boolean
      AND tasks.company_id = sqlc.arg(company_id)::uuid
      AND assignee_agents.id IS NOT NULL
      AND strpos(lower(concat_ws(' ', tasks.key, companies.task_prefix || '-' || tasks.number::text, tasks.title, tasks.description, tasks.status, tasks.priority)), search_input.query) > 0
), conversation_matches AS MATERIALIZED (
    SELECT
        'conversation'::text AS result_kind,
        conversations.id AS entity_id,
        NULL::uuid AS task_id,
        conversations.id AS conversation_id,
        NULL::bigint AS message_seq,
        conversations.kind AS conversation_kind,
        ''::text AS key,
        NULL::bigint AS task_number,
        COALESCE(NULLIF(conversations.title, ''), agents.name, 'Conversation') AS title,
        ''::text AS status,
        ''::text AS priority,
        agents.id AS agent_id,
        agents.name AS agent_name,
        NULL::text AS author_kind,
        NULL::uuid AS author_id,
        NULL::text AS author_display_name,
        NULL::text AS author_avatar_url,
        NULL::text AS snippet_source,
        conversations.created_at,
        conversations.updated_at,
        'direct_conversation'::text AS route_kind,
        CASE
            WHEN lower(COALESCE(NULLIF(conversations.title, ''), agents.name, 'Conversation')) = search_input.query THEN 0
            WHEN lower(COALESCE(NULLIF(conversations.title, ''), agents.name, 'Conversation')) LIKE search_input.query || '%' THEN 1
            ELSE 2
        END::integer AS rank_bucket,
        0::real AS rank_score
    FROM conversations
    CROSS JOIN search_input
    JOIN companies ON companies.id = conversations.company_id AND companies.deleting_at IS NULL
    JOIN company_members AS actor_membership
      ON actor_membership.company_id = conversations.company_id
     AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
     AND actor_membership.status = 'active'
    LEFT JOIN agents
      ON agents.id = conversations.agent_id
     AND agents.company_id = conversations.company_id
    WHERE sqlc.arg(include_conversations)::boolean
      AND conversations.company_id = sqlc.arg(company_id)::uuid
      AND conversations.kind = 'direct'
      AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
      AND agents.id IS NOT NULL
      AND agents.archived_at IS NULL
      AND (sqlc.arg(include_archived)::boolean OR conversations.archived_at IS NULL)
      AND strpos(lower(concat_ws(' ', conversations.title, agents.name, agents.title)), search_input.query) > 0
), active_message_conversations AS MATERIALIZED (
    SELECT
        conversations.id AS conversation_id,
        conversations.kind AS conversation_kind,
        tasks.id AS task_id,
        tasks.key,
        tasks.number AS task_number,
        CASE
            WHEN conversations.kind = 'task' THEN COALESCE(tasks.title, conversations.title, 'Task conversation')
            ELSE COALESCE(NULLIF(conversations.title, ''), direct_agents.name, 'Conversation')
        END AS title,
        COALESCE(tasks.status, '') AS status,
        COALESCE(tasks.priority, '') AS priority,
        COALESCE(direct_agents.id, assignee_agents.id) AS agent_id,
        COALESCE(direct_agents.name, assignee_agents.name) AS agent_name,
        conversations.updated_at,
        CASE
            WHEN conversations.kind IN ('channel','human_direct') THEN 'discussion'
            WHEN conversations.kind = 'direct' THEN 'direct_conversation'
            ELSE 'task_conversation'
        END::text AS route_kind
    FROM conversations
    JOIN companies ON companies.id = conversations.company_id AND companies.deleting_at IS NULL
    JOIN company_members AS actor_membership
      ON actor_membership.company_id = conversations.company_id
     AND actor_membership.user_id = sqlc.arg(actor_user_id)::uuid
     AND actor_membership.status = 'active'
    JOIN users actor ON actor.id=actor_membership.user_id AND actor.disabled_at IS NULL AND actor.deleted_at IS NULL
    LEFT JOIN conversation_members discussion_member ON discussion_member.conversation_id=conversations.id AND discussion_member.user_id=actor_membership.user_id AND discussion_member.status='active'
    LEFT JOIN tasks
      ON tasks.conversation_id = conversations.id
     AND tasks.company_id = conversations.company_id
    LEFT JOIN agents AS direct_agents
      ON direct_agents.id = conversations.agent_id
     AND direct_agents.company_id = conversations.company_id
     AND direct_agents.archived_at IS NULL
    LEFT JOIN agents AS assignee_agents
      ON assignee_agents.id = tasks.executed_by_agent_id
     AND assignee_agents.company_id = tasks.company_id
     AND assignee_agents.archived_at IS NULL
    WHERE sqlc.arg(include_messages)::boolean
      AND conversations.company_id = sqlc.arg(company_id)::uuid
      -- Message search is an active-work surface, not a history search. Task
      -- history stays addressable by its canonical route/context endpoint.
      AND (
          (
              conversations.kind = 'task'
              AND tasks.id IS NOT NULL
              -- A Task chat is searched only by readers of its Task's Project
              -- (ADR 0017), checked on the Task's own Project: the lookup
              -- never scans tasks again.
              AND EXISTS (
                  SELECT 1
                  FROM project_access
                  WHERE project_access.company_id = tasks.company_id
                    AND project_access.project_id = tasks.project_id
                    AND project_access.user_id = sqlc.arg(actor_user_id)::uuid
              )
              AND tasks.status NOT IN ('done', 'failed', 'cancelled')
              AND assignee_agents.id IS NOT NULL
          )
          -- A Board's chat stays on its Board, out of search.
          OR (conversations.kind IN ('channel','human_direct') AND conversations.board_id IS NULL
              AND discussion_member.user_id IS NOT NULL)
          OR (
              conversations.kind = 'direct'
              AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid
              AND direct_agents.id IS NOT NULL
          )
      )
      AND (conversations.kind = 'task' OR conversations.archived_at IS NULL OR (sqlc.arg(include_archived)::boolean AND conversations.kind IN ('channel','human_direct')))
      AND (sqlc.narg(conversation_filter)::uuid IS NULL OR conversations.id=sqlc.narg(conversation_filter)::uuid)
), message_body_matches AS MATERIALIZED (
    -- The type predicate is written exactly as
    -- conversation_items_content_search_idx declares it, so the planner can
    -- prove the partial index applies: search returns only what a person could
    -- already read in the chat, never a Run's tool or reasoning output.
    SELECT
        conversation_items.id AS message_id,
        conversation_items.content AS snippet_source,
        ts_rank_cd(
            to_tsvector('simple', conversation_items.content),
            search_input.ts_query
        )::real AS rank_score,
        0::integer AS source_priority,
        conversation_items.seq AS source_seq
    FROM conversation_items
    JOIN active_message_conversations
      ON active_message_conversations.conversation_id = conversation_items.conversation_id
    CROSS JOIN search_input
    WHERE conversation_items.deleted_at IS NULL
      AND (sqlc.narg(author_filter)::uuid IS NULL OR conversation_items.author_id=sqlc.narg(author_filter)::uuid)
      AND (sqlc.narg(after_time)::timestamptz IS NULL OR conversation_items.created_at>=sqlc.narg(after_time)::timestamptz)
      AND (sqlc.narg(before_time)::timestamptz IS NULL OR conversation_items.created_at<sqlc.narg(before_time)::timestamptz)
      -- A file counts only when the caller may open it: ready bytes and an
      -- attachment_access row, probed per file exactly as the message list
      -- does (ADR 0017). A message whose only file is a Project file the
      -- caller cannot read is not a hasFile match.
      AND (
          NOT sqlc.arg(has_file)::boolean
          OR EXISTS (
              SELECT 1
              FROM message_attachments AS message_file
              JOIN attachments AS message_file_bytes
                ON message_file_bytes.id = message_file.attachment_id
               AND message_file_bytes.company_id = message_file.company_id
               AND message_file_bytes.status = 'ready'
              WHERE message_file.message_id = conversation_items.id
                AND message_file.company_id = conversation_items.company_id
                AND (
                    SELECT 1
                    FROM attachment_access AS access
                    WHERE access.company_id = message_file.company_id
                      AND access.attachment_id = message_file.attachment_id
                      AND access.user_id = sqlc.arg(actor_user_id)::uuid
                    LIMIT 1
                ) IS NOT NULL
          )
      )
      AND conversation_items.content <> ''
      AND conversation_items.type IN ('user_message', 'agent_text')
      AND to_tsvector('simple', conversation_items.content) @@ search_input.ts_query
), message_content_matches AS MATERIALIZED (
    SELECT DISTINCT ON (content_matches.message_id)
        content_matches.message_id,
        content_matches.snippet_source,
        content_matches.rank_score
    FROM message_body_matches AS content_matches
    ORDER BY
        content_matches.message_id,
        content_matches.source_priority,
        content_matches.rank_score DESC,
        content_matches.source_seq DESC
), message_candidates AS MATERIALIZED (
    SELECT
        'message'::text AS result_kind,
        conversation_items.id AS entity_id,
        active_conversations.task_id,
        active_conversations.conversation_id,
        conversation_items.seq AS message_seq,
        active_conversations.conversation_kind,
        active_conversations.key,
        active_conversations.task_number,
        active_conversations.title,
        active_conversations.status,
        active_conversations.priority,
        active_conversations.agent_id,
        active_conversations.agent_name,
        conversation_items.author_kind::text AS author_kind,
        conversation_items.author_id,
        COALESCE(author_users.display_name, author_agents.name, 'Unknown') AS author_display_name,
        author_users.avatar_url AS author_avatar_url,
        content_match.snippet_source,
        conversation_items.created_at,
        active_conversations.updated_at,
        active_conversations.route_kind,
        3::integer AS rank_bucket,
        content_match.rank_score,
        row_number() OVER (
            PARTITION BY active_conversations.conversation_id
            ORDER BY content_match.rank_score DESC,
                     conversation_items.created_at DESC,
                     conversation_items.id DESC
        ) AS conversation_match_position
    FROM message_content_matches AS content_match
    JOIN conversation_items ON conversation_items.id = content_match.message_id
    JOIN active_message_conversations AS active_conversations
      ON active_conversations.conversation_id = conversation_items.conversation_id
    LEFT JOIN users AS author_users
      ON conversation_items.author_kind = 'user' AND author_users.id = conversation_items.author_id
    LEFT JOIN agents AS author_agents
      ON conversation_items.author_kind = 'agent'
     AND author_agents.company_id = conversation_items.company_id
     AND author_agents.id = conversation_items.author_id
), all_results AS MATERIALIZED (
    SELECT * FROM task_matches
    UNION ALL
    SELECT * FROM conversation_matches
    UNION ALL
    SELECT
        result_kind, entity_id, task_id, conversation_id, message_seq,
        conversation_kind, key, task_number, title, status, priority, agent_id, agent_name,
        author_kind, author_id, author_display_name, author_avatar_url, snippet_source, created_at, updated_at,
        route_kind, rank_bucket, rank_score
    FROM message_candidates
    WHERE sqlc.narg(conversation_filter)::uuid IS NOT NULL OR conversation_match_position <= 2
), fairly_ranked AS (
    SELECT all_results.*,
           row_number() OVER (
               PARTITION BY result_kind
               ORDER BY rank_bucket, rank_score DESC, updated_at DESC, entity_id DESC
           ) AS kind_position
    FROM all_results
)
SELECT
    result_kind,
    entity_id,
    COALESCE(task_id, entity_id) AS task_id,
    (task_id IS NOT NULL)::boolean AS has_task_id,
    conversation_id,
    message_seq,
    conversation_kind,
    COALESCE(key, '') AS key,
    COALESCE(task_number, 0)::bigint AS task_number,
    title,
    status,
    priority,
    agent_id,
    agent_name,
    author_kind, author_id, author_display_name, author_avatar_url, snippet_source, created_at, updated_at,
    route_kind,
    (SELECT i.thread_root_id FROM conversation_items i WHERE i.id=fairly_ranked.entity_id AND fairly_ranked.result_kind='message') AS thread_root_id
FROM fairly_ranked
ORDER BY kind_position, rank_bucket, result_kind, rank_score DESC, updated_at DESC, entity_id DESC
LIMIT sqlc.arg(page_size)::integer;
