-- Unread counts are derived: COUNT(*) over the partial unread index. The
-- realtime counts ordering token is max(inbox_items.updated_at) per
-- recipient — there is no counter table to maintain.

-- name: CreateInboxItem :one
INSERT INTO inbox_items (
    company_id, recipient_user_id, conversation_id,
    kind, entity_type, entity_id,
    title, body, deep_link, dedupe_key, discussion_grant_generation,
    project_id
)
VALUES (
    NULLIF(sqlc.arg(company_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    sqlc.arg(recipient_user_id)::uuid,
    sqlc.narg(conversation_id)::uuid,
    sqlc.arg(kind)::text,
    sqlc.arg(entity_type)::text,
    sqlc.arg(entity_id)::text,
    sqlc.arg(title)::text,
    sqlc.arg(body)::text,
    sqlc.narg(deep_link)::text,
    sqlc.arg(dedupe_key)::text,
    CASE WHEN sqlc.arg(kind)::text='discussion_mention' THEN (
        SELECT generation FROM conversation_members
        WHERE conversation_id=sqlc.narg(conversation_id)::uuid AND user_id=sqlc.arg(recipient_user_id)::uuid AND status='active'
    ) END,
    sqlc.narg(project_id)::uuid
)
ON CONFLICT (company_id, recipient_user_id, dedupe_key) DO NOTHING
RETURNING *;

-- name: ResolveInboxItemProject :one
-- The Project an Inbox item is about (ADR 0017), and whether its recipient
-- reads it: the chat the item links to, else the Task, Run, Approval,
-- Question or Deliverable it names, else the Project it names itself
-- (project_access_granted). No row means it is about no Project.
-- Every arm reads one row by its key. An item about the Project itself also
-- reads for a recipient whose active Company role holds the management
-- bridge (Go binds auth.RolesWith(projects.manage)), as the read predicates
-- below say.
SELECT scope.project_id::uuid AS project_id,
       (EXISTS (
           SELECT 1 FROM project_access AS access
           WHERE access.company_id = sqlc.arg(company_id)::uuid
             AND access.project_id = scope.project_id
             AND access.user_id = sqlc.arg(recipient_user_id)::uuid
       ) OR (sqlc.arg(entity_type)::text = 'project' AND EXISTS (
           SELECT 1 FROM company_members AS project_manager
           WHERE project_manager.company_id = sqlc.arg(company_id)::uuid
             AND project_manager.user_id = sqlc.arg(recipient_user_id)::uuid
             AND project_manager.status = 'active'
             AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
       )))::boolean AS recipient_reads
FROM (
    SELECT 0 AS precedence, conversation.project_id
    FROM conversations AS conversation
    WHERE conversation.company_id = sqlc.arg(company_id)::uuid
      AND conversation.id = sqlc.narg(conversation_id)::uuid
    UNION ALL
    SELECT 1, project.id
    FROM projects AS project
    WHERE sqlc.arg(entity_type)::text = 'project'
      AND project.company_id = sqlc.arg(company_id)::uuid AND project.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 1, task.project_id
    FROM tasks AS task
    WHERE sqlc.arg(entity_type)::text = 'task'
      AND task.company_id = sqlc.arg(company_id)::uuid AND task.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 1, conversation.project_id
    FROM runs AS run
    JOIN conversations AS conversation
      ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'run'
      AND run.company_id = sqlc.arg(company_id)::uuid AND run.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 1, conversation.project_id
    FROM approvals AS approval
    JOIN conversations AS conversation
      ON conversation.company_id = approval.company_id AND conversation.id = approval.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'approval'
      AND approval.company_id = sqlc.arg(company_id)::uuid AND approval.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 1, conversation.project_id
    FROM questions AS question
    JOIN conversations AS conversation
      ON conversation.company_id = question.company_id AND conversation.id = question.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'question'
      AND question.company_id = sqlc.arg(company_id)::uuid AND question.id = sqlc.narg(subject_id)::uuid
    UNION ALL
    SELECT 1, conversation.project_id
    FROM deliverables AS deliverable
    JOIN outputs AS output
      ON output.company_id = deliverable.company_id AND output.id = deliverable.output_id
    JOIN runs AS run
      ON run.company_id = output.company_id AND run.id = output.run_id
    JOIN conversations AS conversation
      ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
    WHERE sqlc.arg(entity_type)::text = 'deliverable'
      AND deliverable.company_id = sqlc.arg(company_id)::uuid AND deliverable.id = sqlc.narg(subject_id)::uuid
) AS scope
WHERE scope.project_id IS NOT NULL
ORDER BY scope.precedence
LIMIT 1;

-- name: GetVisibleConversationForInboxRead :one
SELECT conversations.id
FROM conversations
JOIN companies ON companies.id = conversations.company_id
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND companies.deleting_at IS NULL
  AND (
      -- A Task chat follows its Project (ADR 0017).
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(user_id)::uuid))
      OR (conversations.kind <> 'task' AND (
          conversations.created_by_user_id = sqlc.arg(user_id)::uuid
          OR EXISTS (SELECT 1 FROM conversation_members cm
                     WHERE cm.conversation_id=conversations.id
                       AND cm.user_id=sqlc.arg(user_id)::uuid AND cm.status='active')))
  );

-- name: MarkConversationInboxItemsRead :many
UPDATE inbox_items
SET read_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND conversation_id = sqlc.arg(conversation_id)::uuid
  AND read_at IS NULL
  AND archived_at IS NULL
RETURNING *;

-- name: GetInboxItemByDedupeKey :one
SELECT *
FROM inbox_items
WHERE company_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(company_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  AND recipient_user_id = sqlc.arg(recipient_user_id)::uuid
  AND dedupe_key = sqlc.arg(dedupe_key)::text
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )));

-- name: ListInboxItems :many
SELECT inbox_items.*
FROM inbox_items
WHERE (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR inbox_items.company_id = sqlc.arg(company_id)::uuid)
  AND (inbox_items.company_id IS NULL OR EXISTS (
      SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id
      WHERE m.company_id=inbox_items.company_id AND m.user_id=inbox_items.recipient_user_id
        AND m.status='active' AND c.deleting_at IS NULL
  ))
  AND inbox_items.recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND (inbox_items.kind <> 'discussion_mention' OR EXISTS (
    SELECT 1 FROM conversation_members cm JOIN conversation_items ci
      ON ci.conversation_id=cm.conversation_id AND ci.id::text=inbox_items.entity_id AND ci.deleted_at IS NULL
    WHERE cm.conversation_id=inbox_items.conversation_id AND cm.user_id=inbox_items.recipient_user_id
      AND cm.status='active' AND cm.generation=inbox_items.discussion_grant_generation
  ))

  AND (
      sqlc.arg(status)::text = 'all'
      OR (sqlc.arg(status)::text = 'active' AND inbox_items.archived_at IS NULL)
      OR (
          sqlc.arg(status)::text = 'unread'
          AND inbox_items.read_at IS NULL
          AND inbox_items.archived_at IS NULL
      )
      OR (sqlc.arg(status)::text = 'archived' AND inbox_items.archived_at IS NOT NULL)
  )
  AND (NOT sqlc.arg(has_kind)::boolean OR inbox_items.kind = sqlc.arg(kind)::text)
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (inbox_items.created_at, inbox_items.id) < (
          sqlc.arg(before_created_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY inbox_items.created_at DESC, inbox_items.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: GetInboxCounts :one
-- Derived unread count plus the counts ordering token. The count reads only
-- the items the recipient may see. The token is taken over every item of
-- theirs in scope, seen or hidden, and the access changes of the Projects
-- those items name (ADR 0017): the newest of those times. Hiding an item
-- (a lost grant, a left channel) therefore never lowers it, and the access
-- change that hides or shows an item raises it, so it only moves forward.
-- NULL (no items) is the initial version.
SELECT
    count(inbox_items.id) FILTER (
        WHERE inbox_items.read_at IS NULL AND inbox_items.archived_at IS NULL
    )::bigint AS unread_count,
    (
        SELECT GREATEST(max(every_item.updated_at), max(item_project.access_changed_at))
        FROM inbox_items AS every_item
        LEFT JOIN projects AS item_project
          ON item_project.id = every_item.project_id
         AND item_project.company_id = every_item.company_id
        WHERE every_item.recipient_user_id = users.id
          AND (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR every_item.company_id = sqlc.arg(company_id)::uuid)
    )::timestamptz AS ordering_token
FROM users
LEFT JOIN inbox_items ON inbox_items.recipient_user_id=users.id
  AND (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR inbox_items.company_id=sqlc.arg(company_id)::uuid)
  AND (inbox_items.company_id IS NULL OR EXISTS (
      SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id
      WHERE m.company_id=inbox_items.company_id AND m.user_id=inbox_items.recipient_user_id
        AND m.status='active' AND c.deleting_at IS NULL
  ))
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))

  AND (inbox_items.kind <> 'discussion_mention' OR EXISTS (
    SELECT 1 FROM conversation_members cm JOIN conversation_items ci
      ON ci.conversation_id=cm.conversation_id AND ci.id::text=inbox_items.entity_id AND ci.deleted_at IS NULL
    WHERE cm.conversation_id=inbox_items.conversation_id AND cm.user_id=inbox_items.recipient_user_id
      AND cm.status='active' AND cm.generation=inbox_items.discussion_grant_generation
  ))
WHERE users.id=sqlc.arg(user_id)::uuid AND users.disabled_at IS NULL
  AND (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR EXISTS (
    SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id
    WHERE m.company_id=sqlc.arg(company_id)::uuid AND m.user_id=users.id AND m.status='active' AND c.deleting_at IS NULL
  ))
GROUP BY users.id;

-- name: GetInboxCountsForRecipient :one
-- Transaction participants have already established the recipient through
-- their own active-membership fence or through an Inbox row they just wrote.
-- Reading the stored projection directly keeps terminal cleanup possible
-- after that recipient's membership has been revoked. The ordering token
-- follows GetInboxCounts: every item in scope, seen or hidden, and the access
-- changes of their Projects.
SELECT
    count(inbox_items.id) FILTER (
        WHERE inbox_items.read_at IS NULL AND inbox_items.archived_at IS NULL
    )::bigint AS unread_count,
    (
        SELECT GREATEST(max(every_item.updated_at), max(item_project.access_changed_at))
        FROM inbox_items AS every_item
        LEFT JOIN projects AS item_project
          ON item_project.id = every_item.project_id
         AND item_project.company_id = every_item.company_id
        WHERE every_item.company_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(company_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
          AND every_item.recipient_user_id = sqlc.arg(user_id)::uuid
    )::timestamptz AS ordering_token
FROM inbox_items
WHERE inbox_items.company_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(company_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  AND inbox_items.recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND (inbox_items.kind <> 'discussion_mention' OR EXISTS (
    SELECT 1 FROM conversation_members cm JOIN conversation_items ci
      ON ci.conversation_id=cm.conversation_id AND ci.id::text=inbox_items.entity_id AND ci.deleted_at IS NULL
    WHERE cm.conversation_id=inbox_items.conversation_id AND cm.user_id=inbox_items.recipient_user_id
      AND cm.status='active' AND cm.generation=inbox_items.discussion_grant_generation
  ));

-- name: GetInboxItem :one
SELECT inbox_items.*
FROM inbox_items
WHERE (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR inbox_items.company_id = sqlc.arg(company_id)::uuid)
  AND (inbox_items.company_id IS NULL OR EXISTS (
      SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id
      WHERE m.company_id=inbox_items.company_id AND m.user_id=inbox_items.recipient_user_id
        AND m.status='active' AND c.deleting_at IS NULL
  ))
  AND inbox_items.recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND (inbox_items.kind <> 'discussion_mention' OR EXISTS (
    SELECT 1 FROM conversation_members cm JOIN conversation_items ci
      ON ci.conversation_id=cm.conversation_id AND ci.id::text=inbox_items.entity_id AND ci.deleted_at IS NULL
    WHERE cm.conversation_id=inbox_items.conversation_id AND cm.user_id=inbox_items.recipient_user_id
      AND cm.status='active' AND cm.generation=inbox_items.discussion_grant_generation
  ))

  AND inbox_items.id = sqlc.arg(item_id)::uuid;

-- name: GetInboxItemForUpdate :one
SELECT inbox_items.*
FROM inbox_items
WHERE (sqlc.arg(company_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR inbox_items.company_id = sqlc.arg(company_id)::uuid)
  AND (inbox_items.company_id IS NULL OR EXISTS (
      SELECT 1 FROM company_members m JOIN companies c ON c.id=m.company_id
      WHERE m.company_id=inbox_items.company_id AND m.user_id=inbox_items.recipient_user_id
        AND m.status='active' AND c.deleting_at IS NULL
  ))
  AND inbox_items.recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND (inbox_items.kind <> 'discussion_mention' OR EXISTS (
    SELECT 1 FROM conversation_members cm JOIN conversation_items ci
      ON ci.conversation_id=cm.conversation_id AND ci.id::text=inbox_items.entity_id AND ci.deleted_at IS NULL
    WHERE cm.conversation_id=inbox_items.conversation_id AND cm.user_id=inbox_items.recipient_user_id
      AND cm.status='active' AND cm.generation=inbox_items.discussion_grant_generation
  ))

  AND inbox_items.id = sqlc.arg(item_id)::uuid
FOR UPDATE OF inbox_items;

-- name: MarkInboxItemRead :one
UPDATE inbox_items
SET read_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(item_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND read_at IS NULL
RETURNING *;

-- name: MarkInboxItemUnread :one
UPDATE inbox_items
SET read_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(item_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND read_at IS NOT NULL
RETURNING *;

-- name: ArchiveInboxItem :one
UPDATE inbox_items
SET archived_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(item_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND archived_at IS NULL
RETURNING *;

-- name: RestoreInboxItem :one
UPDATE inbox_items
SET archived_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(item_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND archived_at IS NOT NULL
RETURNING *;

-- name: MarkAllInboxItemsRead :many
UPDATE inbox_items
SET read_at = statement_timestamp(),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(company_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
  AND recipient_user_id = sqlc.arg(user_id)::uuid
  AND (inbox_items.entity_type <> 'deliverable' OR EXISTS (
      SELECT 1 FROM deliverable_inbox_access AS access
      WHERE access.company_id = inbox_items.company_id
        AND access.entity_id = inbox_items.entity_id
        AND access.user_id = inbox_items.recipient_user_id
  ))
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
  AND read_at IS NULL
  AND archived_at IS NULL
RETURNING *;

-- name: MarkInboxEntityRead :many
-- Marks every unread item this recipient holds about one entity. Replaces
-- paging the whole inbox, filtering client-side, and issuing one write per
-- match. Served by inbox_items_recipient_entity_idx.
UPDATE inbox_items
SET read_at = clock_timestamp(),
    updated_at = clock_timestamp(),
    version = inbox_items.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND recipient_user_id = sqlc.arg(recipient_user_id)::uuid
  AND entity_type = sqlc.arg(entity_type)::text
  AND entity_id = sqlc.arg(entity_id)::text
  AND read_at IS NULL
  AND archived_at IS NULL
  -- An item about a Project reads only for the Project's readers (ADR 0017),
  -- one probe per row.
  AND (inbox_items.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = inbox_items.company_id
        AND project_reader.project_id = inbox_items.project_id
        AND project_reader.user_id = inbox_items.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (inbox_items.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = inbox_items.company_id
        AND project_manager.user_id = inbox_items.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
RETURNING *;

-- name: CloseInboxEntityItems :many
-- A resolved human-input request closes every recipient copy atomically with
-- the terminal request row. It remains durable in the archived view.
UPDATE inbox_items
SET read_at = COALESCE(read_at, statement_timestamp()),
    archived_at = COALESCE(archived_at, statement_timestamp()),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND entity_type = sqlc.arg(entity_type)::text
  AND entity_id = sqlc.arg(entity_id)::text
  AND (read_at IS NULL OR archived_at IS NULL)
RETURNING *;

-- name: InboxRecipientReadsProject :one
-- Whether an item's recipient still reads the Project it is about (ADR 0017),
-- for the frames of a set write that does not filter by recipient.
SELECT EXISTS (
    SELECT 1 FROM project_access AS access
    WHERE access.company_id = sqlc.arg(company_id)::uuid
      AND access.project_id = sqlc.arg(project_id)::uuid
      AND access.user_id = sqlc.arg(recipient_user_id)::uuid
)::boolean AS reads;

-- name: CloseTaskRunReviewInboxItems :many
-- A confirmed handoff advances a Task's execution generation, so none of the
-- Runs it had can be reviewed or merged any more (ADR 0014). Their "Ready to
-- review" notices close in the handoff's transaction and stay durable in the
-- archived view. A Task Run's notice is its authorizer's, so the join serves
-- each one from inbox_items_recipient_entity_idx.
UPDATE inbox_items
SET read_at = COALESCE(inbox_items.read_at, statement_timestamp()),
    archived_at = COALESCE(inbox_items.archived_at, statement_timestamp()),
    updated_at = statement_timestamp(),
    version = inbox_items.version + 1
FROM runs
WHERE runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.task_id = sqlc.arg(task_id)::uuid
  AND inbox_items.company_id = runs.company_id
  AND inbox_items.recipient_user_id = runs.execution_authorized_by_user_id
  AND inbox_items.entity_type = 'run'
  AND inbox_items.entity_id = runs.id::text
  AND inbox_items.kind = 'run_completed'
  AND (inbox_items.read_at IS NULL OR inbox_items.archived_at IS NULL)
RETURNING inbox_items.*;

-- name: CloseTaskAssignedInboxItems :many
-- A Task that moves to another member is no longer its previous assignee's to
-- start (ADR 0014), so the "Task assigned" notices that told them to start it
-- close in the same write and stay durable in the archived view. Served by
-- inbox_items_recipient_entity_idx.
UPDATE inbox_items
SET read_at = COALESCE(read_at, statement_timestamp()),
    archived_at = COALESCE(archived_at, statement_timestamp()),
    updated_at = statement_timestamp(),
    version = version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND recipient_user_id = sqlc.arg(recipient_user_id)::uuid
  AND entity_type = 'task'
  AND entity_id = sqlc.arg(task_id)::uuid::text
  AND kind = 'task_assigned'
  AND (read_at IS NULL OR archived_at IS NULL)
RETURNING *;

-- name: ResolveRunInboxRecipient :one
-- The one member a terminal Run notice is for: the member whose computer ran
-- it. A Task Run's authorizer is always the Task's assignee (ADR 0014), so the
-- notice goes there and never to whoever wrote the messages it read: a
-- colleague's queued words are materialized ahead of the trigger, and an
-- assignment, handoff or continuation Run has no trigger message at all. A
-- direct chat or a Schedule is private to its creator, who authorizes every
-- one of its Runs. A channel tag's Run answers in its thread, where the member
-- who tagged the Agent reads it, and raises no notice.
SELECT company_members.user_id::uuid AS user_id
FROM runs
JOIN conversations
  ON conversations.id = runs.conversation_id
 AND conversations.company_id = runs.company_id
JOIN company_members
  ON company_members.company_id = runs.company_id
 AND company_members.user_id = CASE
        WHEN conversations.kind = 'task' THEN runs.execution_authorized_by_user_id
        WHEN conversations.kind IN ('direct', 'schedule') THEN conversations.created_by_user_id
     END
 AND company_members.status = 'active'
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.invocation_id IS NULL;

-- name: ListRunHumanInputWaits :many
-- The Questions and Approvals a completed Run raised that still wait on a
-- person, or on the continuation that carries the answer back. A Run with any
-- has not finished its work: the rule UpdateTaskForRunTerminal applies before
-- a Task moves to review. Each row says whether the Run notice's recipient
-- holds the request's own Inbox item, read or not. They may not: an Approval
-- is raised only to the members who may decide it.
SELECT waiting.entity_type::text AS entity_type,
       EXISTS (
           SELECT 1 FROM inbox_items
           WHERE inbox_items.company_id = waiting.company_id
             AND inbox_items.recipient_user_id = sqlc.arg(recipient_user_id)::uuid
             AND inbox_items.entity_type = waiting.entity_type
             AND inbox_items.entity_id = waiting.id::text
             -- An item about a Project reads only for the Project's readers (ADR 0017),
             -- one probe per row.
             AND (inbox_items.project_id IS NULL OR COALESCE((
                 SELECT true FROM project_access AS project_reader
                 WHERE project_reader.company_id = inbox_items.company_id
                   AND project_reader.project_id = inbox_items.project_id
                   AND project_reader.user_id = inbox_items.recipient_user_id
                 LIMIT 1
             ), false))
       )::boolean AS recipient_notified
FROM (
    SELECT 'approval'::text AS entity_type, approvals.id, approvals.company_id
    FROM approvals
    WHERE approvals.origin_run_id = sqlc.arg(run_id)::uuid
      AND (approvals.status = 'pending' OR approvals.continuation_status = 'pending')
    UNION ALL
    SELECT 'question'::text AS entity_type, questions.id, questions.company_id
    FROM questions
    WHERE questions.origin_run_id = sqlc.arg(run_id)::uuid
      AND (questions.status = 'pending' OR questions.continuation_status = 'pending')
) AS waiting
ORDER BY waiting.entity_type, waiting.id;

-- name: ListInboxCompaniesForUser :many
-- The Companies holding an item the person reads: an item about a Project
-- counts only while they read it (ADR 0017), one probe per row.
SELECT DISTINCT i.company_id::uuid AS company_id FROM inbox_items i
JOIN company_members m ON m.company_id=i.company_id AND m.user_id=i.recipient_user_id AND m.status='active'
JOIN companies c ON c.id=i.company_id AND c.deleting_at IS NULL
WHERE i.recipient_user_id = sqlc.arg(recipient_user_id)::uuid
  AND (i.project_id IS NULL OR COALESCE((
      SELECT true FROM project_access AS project_reader
      WHERE project_reader.company_id = i.company_id
        AND project_reader.project_id = i.project_id
        AND project_reader.user_id = i.recipient_user_id
      LIMIT 1
  ), false)
  -- An item about the Project itself (project_access_granted) also reads
  -- for a recipient whose Company role holds the management bridge.
  OR (i.entity_type = 'project' AND EXISTS (
      SELECT 1 FROM company_members AS project_manager
      WHERE project_manager.company_id = i.company_id
        AND project_manager.user_id = i.recipient_user_id
        AND project_manager.status = 'active'
        AND project_manager.role = ANY(sqlc.arg(management_roles)::text[])
  )))
ORDER BY i.company_id;
