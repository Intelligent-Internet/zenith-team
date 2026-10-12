-- Project access scopes (ADR 0017).
--
-- A Project is restricted by default: only people holding grant rows on it
-- can see it. A Project opened to the Company gives every active member a
-- floor of default permissions, which Go binds as a parameter. Company roles
-- stay owner|admin|member; access inside a Project is a set of permission
-- rows per person, like output_grants.
--
-- The rule is stated once here, in four views and one SQL function, and every
-- read joins them in the same statement as the data. The function and the
-- views name no permission and no role: Go binds the default set, the
-- management set and the roles holding projects.manage. Permission names
-- appear only in the table definition (the CHECK, and the generated
-- read_permission column that ties every grant to its project.read row) and
-- in the one-off owner backfill (owner decision 2, ADR 0017), which also
-- names the owner role, and in the one-off cutover at the end (section 8).
-- TestProjectGrantsCheckMatchesCatalog pins the CHECK against
-- auth.ProjectPermissions().

-- 1. Project visibility and the revision every access change advances.
ALTER TABLE projects
    ADD COLUMN visibility text NOT NULL DEFAULT 'restricted'
        CONSTRAINT projects_visibility_check CHECK (visibility IN ('company', 'restricted')),
    ADD COLUMN access_version bigint NOT NULL DEFAULT 1;

-- 2. One row per person per permission. The rejoin fence in the views and the
--    function ignores a row written before the member's latest (re)join, so a
--    Company rejoin restores no old grant. granted_by_user_id is attribution
--    only: NULL marks the rows this migration wrote.
--
--    Every row needs the person's project.read row on the same Project:
--    read_permission is always 'project.read' and the deferred foreign key
--    below points each row at that row. So a content permission without read
--    cannot be stored, and removing read while other rows stay is refused at
--    commit. Removing a person deletes all their rows in one statement.
--
--    The timestamps use clock_timestamp(), the clock company_members uses for
--    created_at and reactivated_at, so a grant written in the same statement
--    as a (re)join is not older than the join and passes the fence.
CREATE TABLE project_grants (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    permission text NOT NULL
        CONSTRAINT project_grants_permission_check CHECK (permission IN (
            'project.read',
            'project.tasks.write',
            'project.tasks.execute',
            'project.files.manage',
            'project.approvals.resolve',
            'project.results.review',
            'project.visibility.update',
            'project.settings.update',
            'project.grants.manage',
            'project.archive'
        )),
    read_permission text GENERATED ALWAYS AS ('project.read') STORED NOT NULL,
    granted_by_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, project_id, user_id, permission),
    FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE,
    CONSTRAINT project_grants_read_fkey
        FOREIGN KEY (company_id, project_id, user_id, read_permission)
        REFERENCES project_grants(company_id, project_id, user_id, permission)
        DEFERRABLE INITIALLY DEFERRED
);

-- "Which Projects can this person reach" starts from the person.
CREATE INDEX project_grants_user_idx ON project_grants (company_id, user_id, project_id);

-- Existing Projects stay restricted (the column default). Every active owner
-- holds every permission on every Project of their Company; nobody else holds
-- any row until someone with project.grants.manage adds them. This one-off
-- backfill is the only place the migration names the owner role and lists the
-- catalog (owner decision 2, ADR 0017); TestProjectGrantsCheckMatchesCatalog
-- pins the list. The account's disabled state is not checked here: every view
-- and the function already exclude disabled accounts.
INSERT INTO project_grants (company_id, project_id, user_id, permission)
SELECT project.company_id, project.id, member.user_id, catalog.permission
FROM projects AS project
JOIN company_members AS member
  ON member.company_id = project.company_id
 AND member.role = 'owner'
 AND member.status = 'active'
CROSS JOIN (VALUES
    ('project.read'),
    ('project.tasks.write'),
    ('project.tasks.execute'),
    ('project.files.manage'),
    ('project.approvals.resolve'),
    ('project.results.review'),
    ('project.visibility.update'),
    ('project.settings.update'),
    ('project.grants.manage'),
    ('project.archive')
) AS catalog(permission);

-- 3. Typed Project scope on the rows that reach people outside a Project page.
--    No foreign key: a Task chat keeps its Project after the Task is deleted,
--    and Inbox, Activity and report rows are retained history.
ALTER TABLE conversations ADD COLUMN project_id uuid;
ALTER TABLE inbox_items ADD COLUMN project_id uuid;
ALTER TABLE activity_events ADD COLUMN project_id uuid;
ALTER TABLE task_report_events ADD COLUMN project_id uuid;

-- Task chats of one Project: the Run cancel sweep and Task-chat lists look
-- them up by Project.
CREATE INDEX conversations_project_idx ON conversations (company_id, project_id)
    WHERE project_id IS NOT NULL;

-- A Task chat belongs to its Task's Project. Chats whose Task was already
-- deleted keep NULL: they are the legacy pocket the CHECK below bounds.
UPDATE conversations AS conversation
SET project_id = task.project_id
FROM tasks AS task
WHERE task.company_id = conversation.company_id
  AND task.conversation_id = conversation.id
  AND conversation.kind = 'task';

-- The update above queued the deferred discussion_shape trigger, and Postgres
-- refuses ALTER TABLE on a table with pending trigger events. Run the pending
-- checks now; the setting ends with this migration's transaction.
SET CONSTRAINTS ALL IMMEDIATE;

-- Every Task chat names its Project. The only exception is the legacy pocket:
-- a chat whose Task was deleted (task_id NULL) before this migration ran
-- (created_at before the stamped time). A live Task's chat can never lose its
-- Project, and a chat created from now on (created_at is its insert time)
-- cannot join the pocket. DetachDeletedTaskConversation clears task_id and
-- keeps project_id; no statement writes project_id back to NULL.
DO $$
BEGIN
    EXECUTE format(
        'ALTER TABLE conversations ADD CONSTRAINT conversations_task_project_check '
        'CHECK (kind <> %L OR project_id IS NOT NULL OR (task_id IS NULL AND created_at < %L::timestamptz))',
        'task',
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    );
END
$$;

UPDATE task_report_events AS report
SET project_id = task.project_id
FROM tasks AS task
WHERE task.company_id = report.company_id
  AND task.id = report.task_id;

-- Inbox rows link to a Task chat through conversation_id, and to a Task, Run,
-- Approval, Question or Deliverable through the typed entity columns. Each
-- step fills only rows the earlier steps left NULL.
UPDATE inbox_items AS item
SET project_id = conversation.project_id
FROM conversations AS conversation
WHERE conversation.company_id = item.company_id
  AND conversation.id = item.conversation_id
  AND conversation.project_id IS NOT NULL;

UPDATE inbox_items AS item
SET project_id = task.project_id
FROM tasks AS task
WHERE item.project_id IS NULL
  AND item.entity_type = 'task'
  AND task.company_id = item.company_id
  AND task.id::text = item.entity_id;

UPDATE inbox_items AS item
SET project_id = conversation.project_id
FROM runs AS run
JOIN conversations AS conversation
  ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
WHERE item.project_id IS NULL
  AND item.entity_type = 'run'
  AND run.company_id = item.company_id
  AND run.id::text = item.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE inbox_items AS item
SET project_id = conversation.project_id
FROM approvals AS approval
JOIN conversations AS conversation
  ON conversation.company_id = approval.company_id AND conversation.id = approval.conversation_id
WHERE item.project_id IS NULL
  AND item.entity_type = 'approval'
  AND approval.company_id = item.company_id
  AND approval.id::text = item.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE inbox_items AS item
SET project_id = conversation.project_id
FROM questions AS question
JOIN conversations AS conversation
  ON conversation.company_id = question.company_id AND conversation.id = question.conversation_id
WHERE item.project_id IS NULL
  AND item.entity_type = 'question'
  AND question.company_id = item.company_id
  AND question.id::text = item.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE inbox_items AS item
SET project_id = conversation.project_id
FROM deliverables AS deliverable
JOIN outputs AS output
  ON output.company_id = deliverable.company_id AND output.id = deliverable.output_id
JOIN runs AS run
  ON run.company_id = output.company_id AND run.id = output.run_id
JOIN conversations AS conversation
  ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
WHERE item.project_id IS NULL
  AND item.entity_type = 'deliverable'
  AND deliverable.company_id = item.company_id
  AND deliverable.id::text = item.entity_id
  AND conversation.project_id IS NOT NULL;

-- Activity rows carry task_id and conversation_id when the owner captured
-- them, and otherwise name their subject in entity_type/entity_id (Bus
-- evidence rows). The details jsonb is never read.
UPDATE activity_events AS activity
SET project_id = task.project_id
FROM tasks AS task
WHERE task.company_id = activity.company_id
  AND task.id = activity.task_id;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM conversations AS conversation
WHERE activity.project_id IS NULL
  AND conversation.company_id = activity.company_id
  AND conversation.id = activity.conversation_id
  AND conversation.project_id IS NOT NULL;

UPDATE activity_events AS activity
SET project_id = task.project_id
FROM tasks AS task
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'task'
  AND task.company_id = activity.company_id
  AND task.id::text = activity.entity_id;

UPDATE activity_events AS activity
SET project_id = project.id
FROM projects AS project
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'project'
  AND project.company_id = activity.company_id
  AND project.id::text = activity.entity_id;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM conversations AS conversation
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'conversation'
  AND conversation.company_id = activity.company_id
  AND conversation.id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM runs AS run
JOIN conversations AS conversation
  ON conversation.company_id = run.company_id AND conversation.id = run.conversation_id
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'run'
  AND run.company_id = activity.company_id
  AND run.id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM conversation_items AS item
JOIN conversations AS conversation
  ON conversation.company_id = item.company_id AND conversation.id = item.conversation_id
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'message'
  AND item.company_id = activity.company_id
  AND item.id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

-- Bus rows about a file (entity_type 'attachment') that name a file still
-- linked to a message take that chat's Project. The rest keep NULL: a
-- deletion row names no file in entity_id, and its chat is only in the event
-- envelope, which the recorder did not store. The Activity read step must
-- treat 'attachment' rows with no Project as Task-scoped, and the recorder
-- must store the envelope's chat from then on (ADR 0017 rollout).
UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM message_attachments AS link
JOIN conversation_items AS item
  ON item.company_id = link.company_id AND item.id = link.message_id
JOIN conversations AS conversation
  ON conversation.company_id = item.company_id AND conversation.id = item.conversation_id
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'attachment'
  AND link.company_id = activity.company_id
  AND link.attachment_id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM approvals AS approval
JOIN conversations AS conversation
  ON conversation.company_id = approval.company_id AND conversation.id = approval.conversation_id
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'approval'
  AND approval.company_id = activity.company_id
  AND approval.id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

UPDATE activity_events AS activity
SET project_id = conversation.project_id
FROM questions AS question
JOIN conversations AS conversation
  ON conversation.company_id = question.company_id AND conversation.id = question.conversation_id
WHERE activity.project_id IS NULL
  AND activity.entity_type = 'question'
  AND question.company_id = activity.company_id
  AND question.id::text = activity.entity_id
  AND conversation.project_id IS NOT NULL;

-- A person's Inbox rows in one Project: re-addressing and cleanup after a
-- grant is removed find them without scanning the person's whole Inbox.
CREATE INDEX inbox_items_recipient_project_idx ON inbox_items (company_id, recipient_user_id, project_id)
    WHERE project_id IS NOT NULL;
-- A Project's Activity in feed order (the Project page and grant history).
CREATE INDEX activity_events_project_created_idx
    ON activity_events (company_id, project_id, created_at DESC, id DESC)
    WHERE project_id IS NOT NULL;
-- task_report_events needs no new index: pending reports are read through
-- task_report_events_pending_recipient_idx and the Project filter applies to
-- that short list.

-- 4. The one per-permission predicate, for writes and the Run claim fence.
--    Go binds the Company default set, the three management permissions and
--    the roles holding projects.manage, so no permission or role name lives
--    here. Arguments are positional because a SQL function resolves a bare
--    name to a column first.
--
--    Postgres does not inline this function (its body has an EXISTS), so the
--    planner sees one opaque call per row. Use it where the candidate set is
--    already small: one write, or the Run claim fence. List queries join the
--    views below instead.
CREATE FUNCTION project_permission_held(
    company_id uuid,
    project_id uuid,
    user_id uuid,
    permission text,
    company_default_permissions text[],
    management_permissions text[],
    management_roles text[]
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT EXISTS (
    SELECT 1
    FROM company_members AS member
    JOIN users AS account
      ON account.id = member.user_id
     AND account.disabled_at IS NULL
     AND account.deleted_at IS NULL
    JOIN companies AS company
      ON company.id = member.company_id
     AND company.deleting_at IS NULL
    JOIN projects AS project
      ON project.company_id = member.company_id
     AND project.id = $2
    WHERE member.company_id = $1
      AND member.user_id = $3
      AND member.status = 'active'
      AND (
          EXISTS (
              SELECT 1
              FROM project_grants AS grant_row
              WHERE grant_row.company_id = $1
                AND grant_row.project_id = $2
                AND grant_row.user_id = $3
                AND grant_row.permission = $4
                AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
          )
          OR (project.visibility = 'company' AND $4 = ANY($5))
          OR ($4 = ANY($6) AND member.role = ANY($7))
      )
)
$$;

-- 5. Who can read what. A row means read access; every view admits only
--    active members with enabled accounts in a Company that is not being
--    deleted.

-- project_access: a row is project.read. via says why: 'grant' when the
-- person holds a fenced project.read row (the row whose permission equals its
-- read_permission, the same row project_permission_held checks for read),
-- else 'company' for a Company-visible Project. The projects.manage bridge
-- grants no row: management is not content.
CREATE VIEW project_access AS
SELECT project.company_id, project.id AS project_id, member.user_id,
       CASE WHEN read_row.user_id IS NOT NULL THEN 'grant' ELSE 'company' END::text AS via
FROM projects AS project
JOIN companies AS company
  ON company.id = project.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = project.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
LEFT JOIN project_grants AS read_row
  ON read_row.company_id = project.company_id
 AND read_row.project_id = project.id
 AND read_row.user_id = member.user_id
 AND read_row.permission = read_row.read_permission
 AND read_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
WHERE read_row.user_id IS NOT NULL OR project.visibility = 'company';

-- task_access: a Task is readable exactly where its Project is.
CREATE VIEW task_access AS
SELECT task.company_id, task.id AS task_id, task.project_id, task.conversation_id, access.user_id
FROM tasks AS task
JOIN project_access AS access
  ON access.company_id = task.company_id AND access.project_id = task.project_id;

-- conversation_readers: who reads a chat, by kind. One row per chat and person.
--
-- attachment_access and conversation_readers are UNION ALL views whose arms
-- Postgres plans on their own. A join clause from the outer query is not
-- pushed into the arms, so a plain JOIN or EXISTS against either view builds
-- the caller's whole readable set first. A query that checks a list of rows
-- must reach them through a correlated lookup that names both the row and
-- the person, for example:
--   CROSS JOIN LATERAL (SELECT 1 FROM attachment_access AS access
--       WHERE access.company_id = link.company_id
--         AND access.attachment_id = link.attachment_id
--         AND access.user_id = @actor LIMIT 1) AS readable
-- (or a scalar subquery of the same shape). The lookup then uses each arm's
-- indexes. A check of one row with constant ids is fine either way.
CREATE VIEW conversation_readers AS
-- Task chats follow their Project.
SELECT conversation.company_id, conversation.id AS conversation_id, access.user_id
FROM conversations AS conversation
JOIN project_access AS access
  ON access.company_id = conversation.company_id AND access.project_id = conversation.project_id
WHERE conversation.kind = 'task'
UNION ALL
-- Task chats whose Task was deleted before this migration recorded no
-- Project; they stay readable by every active member (a known pocket, ADR
-- 0017). conversations_task_project_check admits a NULL Project only on such a
-- chat, and task_id IS NULL repeats that here so a live Task's chat never
-- falls into this arm.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'task' AND conversation.project_id IS NULL AND conversation.task_id IS NULL
UNION ALL
-- Channels, human direct messages and Huddle chats follow their active
-- participants, as ListDiscussionAccess does.
SELECT conversation.company_id, conversation.id, participant.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN conversation_members AS participant
  ON participant.company_id = conversation.company_id
 AND participant.conversation_id = conversation.id
 AND participant.status = 'active'
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = participant.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('channel', 'human_direct', 'huddle')
UNION ALL
-- A channel Agent's execution chat is read by the callers of its live
-- invocations: the live-caller arm of LockHumanInputOriginConversation. The
-- member who created the channel Agent chat is deliberately not a reader
-- through that role alone (ADR 0017).
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'channel_agent'
  AND EXISTS (
      SELECT 1
      FROM channel_invocations AS invocation
      WHERE invocation.company_id = conversation.company_id
        AND invocation.conversation_id = conversation.id
        AND invocation.caller_user_id = member.user_id
        AND invocation.status IN ('active', 'waiting_input')
  )
UNION ALL
-- Direct and Schedule chats are their creator's alone.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = conversation.created_by_user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('direct', 'schedule');

-- attachment_access: who may read a file. A file with an active project_files
-- row follows the readers of those Projects and nobody else, wherever it was
-- shared. Otherwise a file attached to a message follows the chat's readers,
-- and a file in neither follows its uploader. An Artifact file the person
-- cannot open through output_access is excluded, as AuthorizeAttachmentDownload
-- always did. The view says who, not whether the bytes are ready: callers keep
-- their own attachments.status check. List queries reach it through a
-- correlated lookup (see the note above conversation_readers).
--
-- The chat arm follows the same rule inside the view: each member of the
-- file's Company is a candidate, checked by a LATERAL ... LIMIT 1 lookup that
-- names both the chat and the person, so each conversation_readers arm answers
-- from its own indexes. Joined on the chat instead, Postgres would not push
-- the chat id into the UNION ALL arms, and every file lookup would build the
-- person's whole readable set of chats (every Task chat of every Project they
-- read, every channel they are in) before matching the one chat. A lookup for
-- one person, the usual case, narrows the candidates to that person's
-- membership row first. One row per file and reader.
CREATE VIEW attachment_access AS
SELECT arm.company_id, arm.attachment_id, arm.user_id
FROM (
    SELECT DISTINCT file.company_id, file.attachment_id, access.user_id
    FROM project_files AS file
    JOIN project_access AS access
      ON access.company_id = file.company_id AND access.project_id = file.project_id
    WHERE file.removed_at IS NULL
    UNION ALL
    SELECT link.company_id, link.attachment_id, candidate.user_id
    FROM message_attachments AS link
    JOIN conversation_items AS item
      ON item.company_id = link.company_id
     AND item.id = link.message_id
     AND item.deleted_at IS NULL
    JOIN company_members AS candidate
      ON candidate.company_id = link.company_id
    CROSS JOIN LATERAL (
        SELECT 1
        FROM conversation_readers AS reader
        WHERE reader.company_id = item.company_id
          AND reader.conversation_id = item.conversation_id
          AND reader.user_id = candidate.user_id
        LIMIT 1
    ) AS reads_chat
    WHERE NOT EXISTS (
        SELECT 1 FROM project_files AS file
        WHERE file.company_id = link.company_id
          AND file.attachment_id = link.attachment_id
          AND file.removed_at IS NULL
    )
    UNION ALL
    SELECT attachment.company_id, attachment.id, member.user_id
    FROM attachments AS attachment
    JOIN companies AS company
      ON company.id = attachment.company_id AND company.deleting_at IS NULL
    JOIN company_members AS member
      ON member.company_id = attachment.company_id
     AND member.user_id = attachment.uploaded_by_user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE NOT EXISTS (
        SELECT 1 FROM message_attachments AS link
        WHERE link.company_id = attachment.company_id AND link.attachment_id = attachment.id
    )
      AND NOT EXISTS (
        SELECT 1 FROM project_files AS file
        WHERE file.company_id = attachment.company_id
          AND file.attachment_id = attachment.id
          AND file.removed_at IS NULL
    )
) AS arm
WHERE NOT EXISTS (
    SELECT 1
    FROM asset_versions AS artifact_version
    JOIN output_items AS artifact_item
      ON artifact_item.asset_version_id = artifact_version.id
     AND artifact_item.company_id = artifact_version.company_id
    WHERE artifact_version.attachment_id = arm.attachment_id
      AND artifact_version.company_id = arm.company_id
      AND NOT EXISTS (
          SELECT 1 FROM output_access AS access
          WHERE access.company_id = artifact_item.company_id
            AND access.output_id = artifact_item.output_id
            AND access.user_id = arm.user_id
      )
);

-- 6. output_access (000036) with one change: the Company-visibility arm only
--    reaches readers of the source Project when the Artifact came from a
--    restricted Project's Task chat. The Project is joined inline, and the
--    grant is an EXISTS on the fenced project.read row, the same row
--    project_access checks, so the result stays one row per person.
CREATE OR REPLACE VIEW output_access AS
SELECT output.company_id, output.id AS output_id, member.user_id,
       CASE WHEN output.owner_user_id = member.user_id OR grant_row.permission = 'manage'
            THEN 3 ELSE 1 END::integer AS access_level
FROM outputs AS output
JOIN company_members AS member
  ON member.company_id = output.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
JOIN companies AS company ON company.id = output.company_id AND company.deleting_at IS NULL
LEFT JOIN output_grants AS grant_row
  ON grant_row.company_id = output.company_id AND grant_row.output_id = output.id
 AND grant_row.user_id = member.user_id
 AND grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
LEFT JOIN runs AS source_run
  ON source_run.company_id = output.company_id AND source_run.id = output.run_id
LEFT JOIN conversations AS source_conversation
  ON source_conversation.company_id = source_run.company_id
 AND source_conversation.id = source_run.conversation_id
LEFT JOIN projects AS source_project
  ON source_project.company_id = source_conversation.company_id
 AND source_project.id = source_conversation.project_id
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR (output.visibility = 'company' AND (
       source_conversation.project_id IS NULL
       OR source_project.visibility = 'company'
       OR EXISTS (
           SELECT 1 FROM project_grants AS project_grant
           WHERE project_grant.company_id = source_project.company_id
             AND project_grant.project_id = source_project.id
             AND project_grant.user_id = member.user_id
             AND project_grant.permission = project_grant.read_permission
             AND project_grant.updated_at >= COALESCE(member.reactivated_at, member.created_at)
       )
   ))
   OR EXISTS (
       SELECT 1 FROM output_conversation_grants AS conversation_grant
       JOIN conversation_members AS participant
         ON participant.company_id = conversation_grant.company_id
        AND participant.conversation_id = conversation_grant.conversation_id
        AND participant.user_id = member.user_id AND participant.status = 'active'
       JOIN conversations AS conversation
         ON conversation.id = conversation_grant.conversation_id
        AND conversation.company_id = conversation_grant.company_id
        AND conversation.kind IN ('channel', 'human_direct')
       WHERE conversation_grant.company_id = output.company_id
         AND conversation_grant.output_id = output.id
   );

-- 7. The audience of a group chat (ADR 0017 decision 10). A Run tagged in a
--    channel or group DM answers into the chat, where every active member
--    reads the answer, so it may read a Project only when every one of them
--    may: an active participant with an enabled account and an active
--    membership who has no project_access row on the Project makes it false.
--    A Huddle's voice reads are bounded the same way by its chat, whose
--    members hear the answer. Only a channel, human direct or Huddle chat
--    with at least one active participant has this audience; any other id
--    is false, so a wrong argument or an empty chat never opens anything.
--    The caller's own access is checked separately; this narrows it.
--
--    Like project_permission_held it is not inlined (its body has an
--    EXISTS): one call per candidate Project or file, never inside a list
--    over a Company's Tasks.
CREATE FUNCTION conversation_audience_reads(
    company_id uuid,
    conversation_id uuid,
    project_id uuid
) RETURNS boolean
LANGUAGE sql STABLE
AS $$
SELECT EXISTS (
    SELECT 1
    FROM conversations AS conversation
    WHERE conversation.company_id = $1
      AND conversation.id = $2
      AND conversation.kind IN ('channel', 'human_direct', 'huddle')
) AND EXISTS (
    SELECT 1
    FROM conversation_members AS participant
    WHERE participant.company_id = $1
      AND participant.conversation_id = $2
      AND participant.status = 'active'
) AND NOT EXISTS (
    SELECT 1
    FROM conversation_members AS participant
    JOIN company_members AS member
      ON member.company_id = participant.company_id
     AND member.user_id = participant.user_id
     AND member.status = 'active'
    JOIN users AS account
      ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
    WHERE participant.company_id = $1
      AND participant.conversation_id = $2
      AND participant.status = 'active'
      AND NOT EXISTS (
          SELECT 1 FROM project_access AS access
          WHERE access.company_id = $1
            AND access.project_id = $3
            AND access.user_id = participant.user_id
      )
)
$$;

-- 8. Cutover for work already in flight (the 000059 pattern). The backfill
--    above left every Project restricted with only the active owners
--    holding grants, so work that other members had started must not keep
--    going where they can no longer see it.
--
--    Inside this migration every Project is restricted and
--    project.tasks.execute and project.approvals.resolve are never
--    management permissions, so the default set and the management set
--    cannot add any of them here: the calls below pass them empty, and
--    only a fenced grant row counts. project.tasks.write is not a
--    management permission either. These three statements are the other
--    place the migration names permissions.

--    a) A Run that has not ended, in a Task chat whose Project its
--       authorizer may no longer run work in, is cancelled with the reason
--       the application writes when access is lost (project_access_revoked,
--       runs.error like CancelRun). The claim fence would never admit it
--       again, and a live one must not go on. A Task chat with no Project
--       (the legacy pocket) admits no Run, so its Runs end too, and so does
--       a Run with no authorizer. The Run's tokens are deleted. A tagged
--       Run (000042) always runs in a channel-Agent chat, never in a Task
--       chat, so no channel tag is held by these Runs and none needs
--       closing, unlike 000059. The Task each Run was working on takes the
--       terminal state task.ApplyRunTerminalInTx gives a cancelled Run
--       (UpdateTaskForRunTerminal): only while that Run still owns the
--       Task's execution generation and executing Agent and the Task is in
--       progress. The migration stages no frames and writes no Activity;
--       clients load fresh state after the stop-the-world deploy.
WITH cancelled AS (
    UPDATE runs
    SET status = 'cancelled',
        error = 'project_access_revoked',
        wait_reason = NULL,
        prepare_lease_expires_at = NULL,
        fire_at = NULL,
        updated_at = now()
    FROM conversations AS conversation
    WHERE conversation.company_id = runs.company_id
      AND conversation.id = runs.conversation_id
      AND conversation.kind = 'task'
      AND runs.status IN ('queued', 'deferred', 'dispatched', 'waiting_local_directory', 'running')
      AND (
          conversation.project_id IS NULL
          OR runs.execution_authorized_by_user_id IS NULL
          OR NOT project_permission_held(
              runs.company_id, conversation.project_id, runs.execution_authorized_by_user_id,
              'project.tasks.execute', '{}'::text[], '{}'::text[], '{}'::text[])
      )
    RETURNING runs.id, runs.company_id, runs.task_id, runs.agent_id, runs.task_execution_generation
),
revoked_tokens AS (
    DELETE FROM run_tokens
    USING cancelled
    WHERE run_tokens.run_id = cancelled.id
)
UPDATE tasks
SET status = 'cancelled',
    completed_at = COALESCE(tasks.completed_at, clock_timestamp()),
    updated_at = clock_timestamp(),
    version = tasks.version + 1
FROM cancelled
WHERE cancelled.company_id = tasks.company_id
  AND cancelled.task_id = tasks.id
  AND cancelled.task_execution_generation = tasks.execution_generation
  AND tasks.executed_by_agent_id = cancelled.agent_id
  AND tasks.status = 'in_progress';

--    b) A pending Approval or Question raised in a Task chat with a Project
--       is decided by that Project's readers holding
--       project.approvals.resolve (ADR 0017). Its Inbox items were addressed
--       under the old rule (the Company role, or every member for a
--       Question), so they are addressed again: every decider gets the item
--       the application would have written (title, body and link as
--       createApprovalInboxItems and createQuestionInboxItems build them,
--       the same dedupe key, so an item a decider already has is kept as
--       it is), and every other copy still open is closed the way a
--       resolved request closes its items (read and archived; it stays in
--       the archived view). Here the deciders are exactly the active owners
--       with enabled accounts, who also hold project.tasks.write and read
--       the Project, so a Task-chat Company Plan Approval (whose approvers
--       also need project.tasks.write) and a Commerce publication Approval
--       (Company approvals.resolve holders who read the Project) reach the
--       same people. When a request has no decider at all, every active
--       owner gets one item, so it is never left with nobody: an owner who
--       cannot read the Project sees it after Join.
WITH request AS (
    SELECT approval.company_id, approval.id, conversation.project_id, approval.conversation_id,
        'approval'::text AS entity_type, 'approval_request'::text AS kind, approval.title,
        CASE WHEN approval.type = 'commerce_manifest_publish' AND approval.commerce_manifest_id IS NOT NULL
            THEN 'Review and publish the exact payment setup proposed by the agent.'
            ELSE 'An agent needs approval to apply the Company Plan.'
        END AS body,
        '/app?agentId=' || approval.requested_by_agent_id::text
            || '&conversationId=' || approval.conversation_id::text
            || '&conversationKind=task&taskId=' || approval.task_id::text
            || '&approvalId=' || approval.id::text AS deep_link,
        'approval:' || approval.id::text || ':requested' AS dedupe_key
    FROM approvals AS approval
    JOIN conversations AS conversation
      ON conversation.company_id = approval.company_id
     AND conversation.id = approval.conversation_id
     AND conversation.kind = 'task'
     AND conversation.project_id IS NOT NULL
    WHERE approval.status = 'pending'
      AND approval.task_id IS NOT NULL
    UNION ALL
    SELECT question.company_id, question.id, conversation.project_id, question.conversation_id,
        'question', 'question_request', question.title,
        'An agent needs your answer before it can continue.',
        '/app?agentId=' || question.asked_by_agent_id::text
            || '&conversationId=' || question.conversation_id::text
            || '&conversationKind=task&taskId=' || question.task_id::text
            || '&questionId=' || question.id::text,
        'question:' || question.id::text || ':asked'
    FROM questions AS question
    JOIN conversations AS conversation
      ON conversation.company_id = question.company_id
     AND conversation.id = question.conversation_id
     AND conversation.kind = 'task'
     AND conversation.project_id IS NOT NULL
    WHERE question.status = 'pending'
      AND question.task_id IS NOT NULL
),
decider AS (
    SELECT request.*, member.user_id
    FROM request
    JOIN company_members AS member ON member.company_id = request.company_id
    WHERE project_permission_held(
        request.company_id, request.project_id, member.user_id,
        'project.approvals.resolve', '{}'::text[], '{}'::text[], '{}'::text[])
),
recipient AS (
    SELECT * FROM decider
    UNION ALL
    SELECT request.*, owner.user_id
    FROM request
    JOIN company_members AS owner
      ON owner.company_id = request.company_id
     AND owner.role = 'owner'
     AND owner.status = 'active'
    WHERE NOT EXISTS (
        SELECT 1 FROM decider
        WHERE decider.entity_type = request.entity_type AND decider.id = request.id
    )
),
closed AS (
    UPDATE inbox_items AS item
    SET read_at = COALESCE(item.read_at, statement_timestamp()),
        archived_at = COALESCE(item.archived_at, statement_timestamp()),
        updated_at = statement_timestamp(),
        version = item.version + 1
    FROM request
    WHERE item.company_id = request.company_id
      AND item.entity_type = request.entity_type
      AND item.entity_id = request.id::text
      AND item.kind = request.kind
      AND (item.read_at IS NULL OR item.archived_at IS NULL)
      AND NOT EXISTS (
          SELECT 1 FROM recipient
          WHERE recipient.entity_type = request.entity_type
            AND recipient.id = request.id
            AND recipient.user_id = item.recipient_user_id
      )
)
INSERT INTO inbox_items (
    company_id, recipient_user_id, conversation_id, project_id, kind, entity_type, entity_id,
    title, body, deep_link, dedupe_key
)
SELECT company_id, user_id, conversation_id, project_id, kind, entity_type, id::text,
    title, body, deep_link, dedupe_key
FROM recipient
ON CONFLICT ON CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key DO NOTHING;

--    c) A deferred prompt or saved Task Message queued in a Task chat by
--       someone who may no longer post there (project.tasks.write, which
--       every grant row ties to project.read) is removed with the reason
--       the application writes when that access is lost
--       (RemoveProjectRunQueueItemsForAccessRevocationInTx). Nothing would
--       ever carry or promote it: a Run skips the Messages of people who
--       cannot read the chat, and only the assignee's prompts are promoted.
--       Left queued, it would hold one of the chat's queue places for good.
--       A chat with no Project admits no poster, so its items go too. The
--       removed rows stay as history, their file reservations are released
--       (ReleaseRunQueueItemAttachmentsBulk), and every Inbox item about the
--       saved Message (the assignee's task_run_requested nudge) is read and
--       archived, as CloseInboxEntityItems closes it.
WITH removal_clock AS MATERIALIZED (
    SELECT clock_timestamp() AS now_at
),
removed AS (
    UPDATE conversation_run_queue_items AS item
    SET state = 'removed',
        removed_at = removal_clock.now_at,
        removal_reason = 'project_access_revoked',
        updated_at = removal_clock.now_at,
        version = item.version + 1
    FROM removal_clock, conversations AS conversation
    WHERE conversation.company_id = item.company_id
      AND conversation.id = item.conversation_id
      AND conversation.kind = 'task'
      AND item.state = 'queued'
      AND (
          conversation.project_id IS NULL
          OR NOT project_permission_held(
              item.company_id, conversation.project_id, item.initiated_by_user_id,
              'project.tasks.write', '{}'::text[], '{}'::text[], '{}'::text[])
      )
    RETURNING item.id, item.company_id, item.message_id
),
released AS (
    UPDATE conversation_run_queue_attachments AS hold
    SET released_at = removal_clock.now_at,
        updated_at = removal_clock.now_at
    FROM removal_clock, removed
    WHERE hold.queue_item_id = removed.id
      AND hold.released_at IS NULL
)
UPDATE inbox_items AS nudge
SET read_at = COALESCE(nudge.read_at, statement_timestamp()),
    archived_at = COALESCE(nudge.archived_at, statement_timestamp()),
    updated_at = statement_timestamp(),
    version = nudge.version + 1
FROM removed
WHERE nudge.company_id = removed.company_id
  AND nudge.entity_type = 'message'
  AND nudge.entity_id = removed.message_id::text
  AND (nudge.read_at IS NULL OR nudge.archived_at IS NULL);
