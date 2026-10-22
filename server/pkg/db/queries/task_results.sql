-- name: ListTaskRunOutputs :many
SELECT outputs.run_id, runs.runtime_id, runtimes.owner_user_id AS runtime_owner_user_id,
    COALESCE(runs.status,CASE WHEN outputs.source_run_completed THEN 'completed' ELSE 'unknown' END)::text AS run_status, sqlc.embed(outputs)
FROM outputs
LEFT JOIN runs ON runs.id=outputs.run_id AND runs.company_id=outputs.company_id
LEFT JOIN runtimes ON runtimes.id=runs.runtime_id
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
  AND outputs.task_id = sqlc.arg(task_id)::uuid
  AND EXISTS (SELECT 1 FROM output_access AS access
      WHERE access.company_id = outputs.company_id AND access.output_id = outputs.id
        AND access.user_id = sqlc.arg(actor_user_id)::uuid)
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (outputs.created_at, outputs.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
ORDER BY outputs.created_at DESC, outputs.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListConversationArtifacts :many
SELECT outputs.run_id, runs.runtime_id, runtimes.owner_user_id AS runtime_owner_user_id,
    COALESCE(runs.status,CASE WHEN outputs.source_run_completed THEN 'completed' ELSE 'unknown' END)::text AS run_status, sqlc.embed(outputs)
FROM outputs
LEFT JOIN runs
  ON runs.id = outputs.run_id
 AND runs.company_id = outputs.company_id
LEFT JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
 AND (sqlc.arg(run_id)::uuid<>'00000000-0000-0000-0000-000000000000'::uuid OR NOT EXISTS(SELECT 1 FROM delivery_snapshots delivery
         JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id
         JOIN artifacts work ON work.company_id=snapshot.company_id AND work.id=snapshot.artifact_id AND work.archived_at IS NULL AND NOT work.pending_creation
         JOIN artifact_access access ON access.company_id=work.company_id AND access.artifact_id=work.id AND access.user_id=sqlc.arg(actor_user_id)::uuid
         WHERE delivery.company_id=outputs.company_id AND delivery.output_id=outputs.id))
  AND (sqlc.arg(run_id)::uuid = '00000000-0000-0000-0000-000000000000'::uuid
       OR runs.id = sqlc.arg(run_id)::uuid)
  AND (
      outputs.conversation_id = sqlc.arg(conversation_id)::uuid
      OR EXISTS (
          SELECT 1 FROM channel_invocations AS invocation
          WHERE invocation.id = runs.invocation_id
            AND invocation.company_id = outputs.company_id
            AND invocation.channel_id = sqlc.arg(conversation_id)::uuid
      )
      OR EXISTS (
          SELECT 1 FROM output_conversation_grants AS grant_row
          WHERE grant_row.company_id = outputs.company_id
            AND grant_row.output_id = outputs.id
            AND grant_row.conversation_id = sqlc.arg(conversation_id)::uuid
      )
      OR EXISTS (
          SELECT 1 FROM discussion_artifacts AS link
          JOIN conversation_items AS message
            ON message.company_id = link.company_id AND message.id = link.message_id
          WHERE link.company_id = outputs.company_id AND link.output_id = outputs.id
            AND message.conversation_id = sqlc.arg(conversation_id)::uuid
            AND message.run_id IS NULL AND message.deleted_at IS NULL
      )
  )
  AND EXISTS (SELECT 1 FROM output_access AS access
      WHERE access.company_id = outputs.company_id AND access.output_id = outputs.id
        AND access.user_id = sqlc.arg(actor_user_id)::uuid)
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (outputs.created_at, outputs.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
ORDER BY outputs.created_at DESC, outputs.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- Every Artifact the actor can open in the Company, newest first. Each branch
-- seeks one source of access through its own index, so a viewer who can open
-- few Artifacts never scans the whole Company; output_access stays the one
-- authority over the union. A branch missing for a new access source hides
-- Artifacts, it never shows one. title_pattern is an escaped ILIKE pattern,
-- empty for no filter.
-- name: ListCompanyArtifacts :many
WITH source_filter AS NOT MATERIALIZED (
 SELECT output.id FROM outputs output
 LEFT JOIN runs run ON run.company_id=output.company_id AND run.id=output.run_id
LEFT JOIN channel_invocations invocation ON invocation.company_id=run.company_id AND invocation.id=run.invocation_id
LEFT JOIN conversations source ON source.company_id=output.company_id
 AND source.id=COALESCE(invocation.channel_id,output.conversation_id)
 AND (SELECT 1 FROM conversation_readers reader WHERE reader.company_id=source.company_id
      AND reader.conversation_id=source.id AND reader.user_id=sqlc.arg(actor_user_id)::uuid LIMIT 1) IS NOT NULL
 WHERE output.company_id=sqlc.arg(company_id)::uuid
   AND CASE WHEN source.id IS NULL THEN 'restricted' WHEN source.kind='human_direct' THEN 'direct' ELSE source.kind END=sqlc.arg(source_type)::text
), candidate AS (
    (SELECT output.id FROM outputs AS output
     WHERE output.company_id = sqlc.arg(company_id)::uuid
       AND NOT EXISTS(SELECT 1 FROM delivery_snapshots delivery
         JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id
         JOIN artifacts work ON work.company_id=snapshot.company_id AND work.id=snapshot.artifact_id AND work.archived_at IS NULL AND NOT work.pending_creation
         JOIN artifact_access access ON access.company_id=work.company_id AND access.artifact_id=work.id AND access.user_id=sqlc.arg(actor_user_id)::uuid
         WHERE delivery.company_id=output.company_id AND delivery.output_id=output.id)
       AND output.owner_user_id = sqlc.arg(actor_user_id)::uuid
       AND (sqlc.arg(ownership)::text='' OR
            (sqlc.arg(ownership)::text='owned' AND output.owner_user_id=sqlc.arg(actor_user_id)::uuid) OR
            (sqlc.arg(ownership)::text='shared' AND output.owner_user_id IS DISTINCT FROM sqlc.arg(actor_user_id)::uuid))
       AND (sqlc.arg(source_type)::text='' OR (SELECT 1 FROM source_filter WHERE source_filter.id=output.id LIMIT 1) IS NOT NULL)
       AND (sqlc.arg(title_pattern)::text = '' OR output.title ILIKE sqlc.arg(title_pattern)::text ESCAPE '\')
       AND (sqlc.narg(before_created_at)::timestamptz IS NULL
            OR (output.created_at, output.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
     ORDER BY output.created_at DESC, output.id DESC
     LIMIT sqlc.arg(row_limit)::integer)
    UNION
    -- An Artifact made in a restricted Project is Company-visible only to
    -- that Project's readers (ADR 0017), as output_access says. The branch
    -- asks the same, one probe per row, so its page never fills with
    -- Artifacts the final check then drops and ends the list early.
    (SELECT output.id FROM outputs AS output
     LEFT JOIN runs AS source_run
       ON source_run.company_id = output.company_id AND source_run.id = output.run_id
     LEFT JOIN conversations AS source_conversation
       ON source_conversation.company_id = output.company_id
      AND source_conversation.id = output.conversation_id
     WHERE output.company_id = sqlc.arg(company_id)::uuid
       AND NOT EXISTS(SELECT 1 FROM delivery_snapshots delivery
         JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id
         JOIN artifacts work ON work.company_id=snapshot.company_id AND work.id=snapshot.artifact_id AND work.archived_at IS NULL AND NOT work.pending_creation
         JOIN artifact_access access ON access.company_id=work.company_id AND access.artifact_id=work.id AND access.user_id=sqlc.arg(actor_user_id)::uuid
         WHERE delivery.company_id=output.company_id AND delivery.output_id=output.id)
       AND output.visibility = 'company'
       AND (output.project_id IS NULL
            OR (SELECT 1 FROM project_access AS reader
                WHERE reader.company_id = output.company_id
                  AND reader.project_id = output.project_id
                  AND reader.user_id = sqlc.arg(actor_user_id)::uuid
                LIMIT 1) IS NOT NULL)
       AND (sqlc.arg(ownership)::text='' OR
            (sqlc.arg(ownership)::text='owned' AND output.owner_user_id=sqlc.arg(actor_user_id)::uuid) OR
            (sqlc.arg(ownership)::text='shared' AND output.owner_user_id IS DISTINCT FROM sqlc.arg(actor_user_id)::uuid))
       AND (sqlc.arg(source_type)::text='' OR (SELECT 1 FROM source_filter WHERE source_filter.id=output.id LIMIT 1) IS NOT NULL)
       AND (sqlc.arg(title_pattern)::text = '' OR output.title ILIKE sqlc.arg(title_pattern)::text ESCAPE '\')
       AND (sqlc.narg(before_created_at)::timestamptz IS NULL
            OR (output.created_at, output.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
     ORDER BY output.created_at DESC, output.id DESC
     LIMIT sqlc.arg(row_limit)::integer)
    UNION
    (SELECT output.id FROM output_grants AS grant_row
     JOIN outputs AS output
       ON output.company_id = grant_row.company_id AND output.id = grant_row.output_id
     WHERE grant_row.company_id = sqlc.arg(company_id)::uuid
       AND grant_row.user_id = sqlc.arg(actor_user_id)::uuid
       AND NOT EXISTS(SELECT 1 FROM delivery_snapshots delivery
         JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id
         JOIN artifacts work ON work.company_id=snapshot.company_id AND work.id=snapshot.artifact_id AND work.archived_at IS NULL AND NOT work.pending_creation
         JOIN artifact_access access ON access.company_id=work.company_id AND access.artifact_id=work.id AND access.user_id=sqlc.arg(actor_user_id)::uuid
         WHERE delivery.company_id=output.company_id AND delivery.output_id=output.id)
       AND (sqlc.arg(ownership)::text='' OR
            (sqlc.arg(ownership)::text='owned' AND output.owner_user_id=sqlc.arg(actor_user_id)::uuid) OR
            (sqlc.arg(ownership)::text='shared' AND output.owner_user_id IS DISTINCT FROM sqlc.arg(actor_user_id)::uuid))
       AND (sqlc.arg(source_type)::text='' OR (SELECT 1 FROM source_filter WHERE source_filter.id=output.id LIMIT 1) IS NOT NULL)
       AND (sqlc.arg(title_pattern)::text = '' OR output.title ILIKE sqlc.arg(title_pattern)::text ESCAPE '\')
       AND (sqlc.narg(before_created_at)::timestamptz IS NULL
            OR (output.created_at, output.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
     ORDER BY output.created_at DESC, output.id DESC
     LIMIT sqlc.arg(row_limit)::integer)
    UNION
    (SELECT output.id FROM conversation_members AS participant
     JOIN output_conversation_grants AS conversation_grant
       ON conversation_grant.company_id = participant.company_id
      AND conversation_grant.conversation_id = participant.conversation_id
     JOIN outputs AS output
       ON output.company_id = conversation_grant.company_id AND output.id = conversation_grant.output_id
     WHERE participant.company_id = sqlc.arg(company_id)::uuid
       AND participant.user_id = sqlc.arg(actor_user_id)::uuid
       AND participant.status = 'active'
       AND NOT EXISTS(SELECT 1 FROM delivery_snapshots delivery
         JOIN artifact_snapshots snapshot ON snapshot.company_id=delivery.company_id AND snapshot.id=delivery.snapshot_id
         JOIN artifacts work ON work.company_id=snapshot.company_id AND work.id=snapshot.artifact_id AND work.archived_at IS NULL AND NOT work.pending_creation
         JOIN artifact_access access ON access.company_id=work.company_id AND access.artifact_id=work.id AND access.user_id=sqlc.arg(actor_user_id)::uuid
         WHERE delivery.company_id=output.company_id AND delivery.output_id=output.id)
       AND (sqlc.arg(ownership)::text='' OR
            (sqlc.arg(ownership)::text='owned' AND output.owner_user_id=sqlc.arg(actor_user_id)::uuid) OR
            (sqlc.arg(ownership)::text='shared' AND output.owner_user_id IS DISTINCT FROM sqlc.arg(actor_user_id)::uuid))
       AND (sqlc.arg(source_type)::text='' OR (SELECT 1 FROM source_filter WHERE source_filter.id=output.id LIMIT 1) IS NOT NULL)
       AND (sqlc.arg(title_pattern)::text = '' OR output.title ILIKE sqlc.arg(title_pattern)::text ESCAPE '\')
       AND (sqlc.narg(before_created_at)::timestamptz IS NULL
            OR (output.created_at, output.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
     ORDER BY output.created_at DESC, output.id DESC
     LIMIT sqlc.arg(row_limit)::integer)
)
SELECT outputs.run_id, runs.runtime_id, runtimes.owner_user_id AS runtime_owner_user_id,
    COALESCE(runs.status,CASE WHEN outputs.source_run_completed THEN 'completed' ELSE 'unknown' END)::text AS run_status, sqlc.embed(outputs)
FROM candidate
JOIN outputs ON outputs.id = candidate.id
LEFT JOIN runs
  ON runs.id = outputs.run_id
 AND runs.company_id = outputs.company_id
LEFT JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE EXISTS (SELECT 1 FROM output_access AS access
      WHERE access.company_id = outputs.company_id AND access.output_id = outputs.id
        AND access.user_id = sqlc.arg(actor_user_id)::uuid)
ORDER BY outputs.created_at DESC, outputs.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- The named Artifacts the actor can open; the rest are silently absent.
-- name: ListArtifactsByID :many
SELECT outputs.run_id, runs.runtime_id, runtimes.owner_user_id AS runtime_owner_user_id,
    COALESCE(runs.status,CASE WHEN outputs.source_run_completed THEN 'completed' ELSE 'unknown' END)::text AS run_status, sqlc.embed(outputs)
FROM outputs
LEFT JOIN runs
  ON runs.id = outputs.run_id
 AND runs.company_id = outputs.company_id
LEFT JOIN runtimes ON runtimes.id = runs.runtime_id
WHERE outputs.company_id = sqlc.arg(company_id)::uuid
  AND outputs.id = ANY(sqlc.arg(output_ids)::uuid[])
  AND EXISTS (SELECT 1 FROM output_access AS access
      WHERE access.company_id = outputs.company_id AND access.output_id = outputs.id
        AND access.user_id = sqlc.arg(actor_user_id)::uuid)
ORDER BY outputs.created_at DESC, outputs.id DESC;

-- name: GetConversationForArtifactList :one
SELECT conversations.id, conversations.kind, conversations.task_id, conversations.agent_id
FROM conversations
JOIN companies
  ON companies.id = conversations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
  ON company_members.company_id = conversations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
WHERE conversations.id = sqlc.arg(conversation_id)::uuid
  AND conversations.company_id = sqlc.arg(company_id)::uuid
  AND (
      -- A Task chat is read by its Project's readers, a channel or group DM
      -- by its active members (ADR 0017): both through conversation_readers.
      (conversations.kind IN ('task', 'channel', 'human_direct') AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind NOT IN ('task', 'channel', 'human_direct')
          AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  );

-- name: ListOutputDeliverables :many
SELECT * FROM deliverables
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = ANY(sqlc.arg(output_ids)::uuid[]);

-- name: ListOutputPublications :many
SELECT * FROM output_publications
WHERE company_id = sqlc.arg(company_id)::uuid
  AND output_id = ANY(sqlc.arg(output_ids)::uuid[]);

-- name: ListTaskResultSources :many
-- The files members attached in a Task chat that the person may open
-- (attachment_access, row by row): a file that is also another Project's
-- file shows only to that Project's readers (ADR 0017).
SELECT message_attachments.message_id, sqlc.embed(attachments)
FROM conversation_items
JOIN message_attachments
  ON message_attachments.message_id = conversation_items.id
 AND message_attachments.company_id = conversation_items.company_id
JOIN attachments
  ON attachments.id = message_attachments.attachment_id
 AND attachments.company_id = message_attachments.company_id
  AND attachments.status = 'ready'
CROSS JOIN LATERAL (
    SELECT 1 FROM attachment_access AS access
    WHERE access.company_id = message_attachments.company_id
      AND access.attachment_id = message_attachments.attachment_id
      AND access.user_id = sqlc.arg(actor_user_id)::uuid
    LIMIT 1
) AS readable
WHERE conversation_items.company_id = sqlc.arg(company_id)::uuid
  AND conversation_items.conversation_id = sqlc.arg(conversation_id)::uuid
  AND conversation_items.type = 'user_message'
  AND conversation_items.author_kind = 'user'
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (attachments.created_at, attachments.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid))
ORDER BY attachments.created_at DESC, attachments.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: ListTaskRunInputAttachments :many
-- The files a Task Run may read, frozen by the enqueue kernel: every ready
-- Attachment on a member's message in the Task's Conversation, plus the ones
-- the triggering write is about to link. Those have no message yet because the
-- Run row is inserted first, so nothing has vetted them: only the member who
-- authorized the Run may have uploaded them, as linking will require. The
-- colleagues' queued messages the Run carries into its transcript (ADR 0014)
-- have no message yet either; their files are the reservations their queue
-- items still hold, which reserving vetted as the colleague's own uploads.
-- The Run answers into the Task chat, which only its Project's readers see,
-- so it reads only inside that Project (ADR 0017): a file another Project
-- holds is kept only when the Task's own Project holds it too, and a chat
-- file or a trigger file only when the authorizer may open it
-- (attachment_access, row by row). Newest first so a bounded Run keeps the
-- most recent inputs; the caller restores upload order.
SELECT candidate.id, candidate.filename, candidate.content_type,
    candidate.size_bytes, candidate.created_at
FROM (
    SELECT attachments.id, attachments.filename, attachments.content_type,
        attachments.size_bytes, attachments.created_at
    FROM tasks
    JOIN conversation_items
      ON conversation_items.conversation_id = tasks.conversation_id
     AND conversation_items.company_id = tasks.company_id
    JOIN message_attachments
      ON message_attachments.message_id = conversation_items.id
     AND message_attachments.company_id = conversation_items.company_id
    JOIN attachments
      ON attachments.id = message_attachments.attachment_id
     AND attachments.company_id = message_attachments.company_id
    CROSS JOIN LATERAL (
        SELECT 1 FROM attachment_access AS access
        WHERE access.company_id = message_attachments.company_id
          AND access.attachment_id = message_attachments.attachment_id
          AND access.user_id = sqlc.arg(authorized_by_user_id)::uuid
        LIMIT 1
    ) AS readable
    WHERE tasks.id = sqlc.arg(task_id)::uuid
      AND tasks.company_id = sqlc.arg(company_id)::uuid
      AND conversation_items.type = 'user_message'
      AND conversation_items.author_kind = 'user'
      AND attachments.status = 'ready'
    UNION
    SELECT attachments.id, attachments.filename, attachments.content_type,
        attachments.size_bytes, attachments.created_at
    FROM attachments
    CROSS JOIN LATERAL (
        SELECT 1 FROM attachment_access AS access
        WHERE access.company_id = attachments.company_id
          AND access.attachment_id = attachments.id
          AND access.user_id = sqlc.arg(authorized_by_user_id)::uuid
        LIMIT 1
    ) AS readable
    WHERE attachments.id = ANY(sqlc.arg(trigger_attachment_ids)::uuid[])
      AND attachments.company_id = sqlc.arg(company_id)::uuid
      AND attachments.uploaded_by_user_id = sqlc.arg(authorized_by_user_id)::uuid
      AND attachments.status = 'ready'
    UNION
    SELECT attachments.id, attachments.filename, attachments.content_type,
        attachments.size_bytes, attachments.created_at
    FROM conversation_run_queue_attachments AS reserved
    JOIN attachments
      ON attachments.id = reserved.attachment_id
     AND attachments.company_id = reserved.company_id
    WHERE reserved.queue_item_id = ANY(sqlc.arg(carried_queue_item_ids)::uuid[])
      AND reserved.company_id = sqlc.arg(company_id)::uuid
      AND reserved.released_at IS NULL
      AND attachments.status = 'ready'
) AS candidate
JOIN tasks AS task
  ON task.id = sqlc.arg(task_id)::uuid
 AND task.company_id = sqlc.arg(company_id)::uuid
-- Scalar LIMIT 1 probes, so each candidate is one indexed lookup and never
-- a hashed scan of every Project file.
WHERE (
        SELECT true FROM project_files AS held
        WHERE held.company_id = task.company_id
          AND held.attachment_id = candidate.id
          AND held.removed_at IS NULL
        LIMIT 1
    ) IS NULL
   OR COALESCE((
        SELECT true FROM project_files AS held
        WHERE held.company_id = task.company_id
          AND held.attachment_id = candidate.id
          AND held.project_id = task.project_id
          AND held.removed_at IS NULL
        LIMIT 1
    ), false)
ORDER BY candidate.created_at DESC, candidate.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: TaskHasUnappliedResultSuggestions :one
-- A delivery is evidence, not proof that its proposed changes were applied.
-- Completion checks the current generation; later edits never revoke a receipt.
SELECT EXISTS (
 SELECT 1 FROM tasks t
 JOIN outputs o ON o.company_id=t.company_id AND o.task_id=t.id AND o.task_execution_generation=t.execution_generation
 JOIN delivery_snapshots delivery ON delivery.company_id=o.company_id AND delivery.output_id=o.id
 JOIN project_presentations capture ON capture.company_id=delivery.company_id AND capture.id=delivery.snapshot_id
 JOIN project_page_changes change ON change.company_id=capture.company_id AND change.id=capture.change_id
 WHERE t.company_id=$1 AND t.id=$2 AND NOT t.result_proposal_only
 AND (NOT EXISTS(SELECT 1 FROM task_result_sets rs WHERE rs.task_id=t.id AND rs.execution_generation=t.execution_generation)
 OR o.id IN (SELECT i.output_id FROM task_result_items i WHERE i.result_set_id=(SELECT rs.id FROM task_result_sets rs WHERE rs.task_id=t.id AND rs.execution_generation=t.execution_generation ORDER BY rs.task_revision DESC LIMIT 1)))
 AND (
   change.status IN ('superseded','undone','rejected','needs_update')
   OR EXISTS (
     SELECT 1 FROM jsonb_array_elements(change.operations) WITH ORDINALITY AS op(value,position)
     WHERE COALESCE(capture.change_snapshot->'decisions'->>((op.position-1)::text),'') NOT IN ('applied','rejected','already_reflected')
       AND COALESCE(change.decisions->>((op.position-1)::text),'') <> 'applied'
   )
   OR (change.after_theme IS NOT NULL AND COALESCE(change.decisions->>'0','') <> 'applied')
 )
)::boolean;

-- name: GetTaskResultState :one
SELECT t.result_proposal_only,t.result_review_required,t.version,t.execution_generation,t.status,
 COALESCE(s.id,'00000000-0000-0000-0000-000000000000'::uuid)::uuid AS result_set_id,COALESCE(s.task_revision,0)::bigint AS task_revision,s.approved_at,s.source_run_id,
 COALESCE((SELECT array_agg(i.output_id ORDER BY i.ordinal) FROM task_result_items i WHERE i.result_set_id=s.id),'{}'::uuid[])::uuid[] AS output_ids
FROM tasks t LEFT JOIN LATERAL (
 SELECT * FROM task_result_sets rs WHERE rs.company_id=t.company_id AND rs.task_id=t.id
 AND rs.execution_generation=t.execution_generation ORDER BY rs.task_revision DESC LIMIT 1
) s ON true WHERE t.company_id=$1 AND t.id=$2;

-- name: SetTaskResultPolicy :exec
UPDATE tasks SET result_proposal_only=$3,result_review_required=$4 WHERE company_id=$1 AND id=$2;

-- name: CreateTaskResultSet :one
INSERT INTO task_result_sets(company_id,task_id,task_revision,execution_generation,actor_user_id,request_key,request_hash,source_run_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;

-- name: CreateTaskResultItem :exec
INSERT INTO task_result_items(company_id,result_set_id,ordinal,output_id) VALUES($1,$2,$3,$4);

-- name: TaskResultOutputEligible :one
SELECT EXISTS(SELECT 1 FROM outputs o LEFT JOIN runs r ON r.company_id=o.company_id AND r.id=o.run_id
 JOIN tasks t ON t.company_id=o.company_id AND t.id=o.task_id
 JOIN output_access a ON a.company_id=o.company_id AND a.output_id=o.id AND a.user_id=sqlc.arg(actor_user_id)::uuid
 WHERE o.company_id=sqlc.arg(company_id)::uuid AND o.id=sqlc.arg(output_id)::uuid
 AND t.id=sqlc.arg(task_id)::uuid AND o.task_execution_generation=t.execution_generation
 AND (o.actor_type='user' OR r.status='completed'
      OR (o.run_id IS NULL AND o.source_run_completed)))::boolean;

-- name: ApproveTaskResultSet :exec
UPDATE task_result_sets SET approved_at=COALESCE(approved_at,clock_timestamp()),approved_by_user_id=COALESCE(approved_by_user_id,$3)
WHERE company_id=$1 AND id=$2;

-- name: GetTaskResultOutputSuggestions :many
SELECT DISTINCT c.id,c.project_id,c.page_id FROM project_presentations p
JOIN delivery_snapshots delivery ON delivery.company_id=p.company_id AND delivery.snapshot_id=p.id
JOIN project_page_changes c ON c.company_id=p.company_id AND c.id=p.change_id
WHERE p.company_id=$1 AND delivery.output_id=ANY(sqlc.arg(output_ids)::uuid[])
ORDER BY c.project_id,c.page_id,c.id;

-- name: TaskNeedsResultSelection :one
SELECT EXISTS(SELECT 1 FROM outputs o
 JOIN tasks t ON t.company_id=o.company_id AND t.id=o.task_id
 WHERE t.company_id=$1 AND t.id=$2 AND o.task_execution_generation=t.execution_generation
 AND NOT EXISTS(SELECT 1 FROM task_result_sets s WHERE s.task_id=t.id AND s.execution_generation=t.execution_generation))::boolean;

-- name: TaskResultPolicyFrozen :one
SELECT (EXISTS(SELECT 1 FROM runs r WHERE r.company_id=$1 AND r.task_id=$2)
 OR EXISTS(SELECT 1 FROM outputs o WHERE o.company_id=$1 AND o.task_id=$2)
 OR EXISTS(SELECT 1 FROM task_result_sets s WHERE s.company_id=$1 AND s.task_id=$2))::boolean;

-- name: GetTaskResultReceipt :one
SELECT * FROM task_result_sets WHERE company_id=$1 AND actor_user_id=$2 AND request_key=$3;

-- name: TaskResultOutputFromRunningRun :one
SELECT EXISTS(SELECT 1 FROM outputs o
 JOIN runs r ON r.company_id=o.company_id AND r.id=o.run_id
 JOIN tasks t ON t.company_id=o.company_id AND t.id=o.task_id
 JOIN output_access a ON a.company_id=o.company_id AND a.output_id=o.id AND a.user_id=sqlc.arg(actor_user_id)::uuid
 WHERE o.company_id=sqlc.arg(company_id)::uuid AND o.id=sqlc.arg(output_id)::uuid
 AND t.id=sqlc.arg(task_id)::uuid AND o.task_execution_generation=t.execution_generation
 AND r.id=sqlc.arg(run_id)::uuid AND r.status='running')::boolean;
