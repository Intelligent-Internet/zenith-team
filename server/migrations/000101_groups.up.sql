-- Groups (D31, Access TECH Part D). A Group is a named set of Team members
-- in one Company: flat (no Group inside a Group) and people only (never an
-- Agent). A Group holds Project grants like a person does, and every member
-- holds them too. A person's access is the union of their own grant, each of
-- their Groups, the Company-visible floor and the management bridge, so
-- leaving a Group never takes away what the person's own grant gives.
--
-- The rule stays where 000086 put it: project_permission_held, project_access
-- and output_access each gain a Group arm, and every other view, the delegate
-- rule (delegate_permission_held, run_permission_held) and the Run claim
-- fence follow through them unchanged. Like project_grants, the two new grant
-- tables name permissions only in their own definition (the CHECK and the
-- generated read_permission column); TestProjectGrantsCheckMatchesCatalog
-- pins both CHECKs against auth.ProjectPermissions().

-- 1. The Group. version is the revision a Group editor sends back.
CREATE TABLE company_groups (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    company_id uuid NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name text NOT NULL
        CONSTRAINT company_groups_name_check
        CHECK (name = btrim(name) AND char_length(name) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT ''
        CONSTRAINT company_groups_description_check CHECK (char_length(description) <= 500),
    created_by_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1
        CONSTRAINT company_groups_version_check CHECK (version BETWEEN 1 AND 9007199254740991),
    PRIMARY KEY (id),
    UNIQUE (company_id, id)
);

-- A name is unique in its Company, ignoring case.
CREATE UNIQUE INDEX company_groups_name_key ON company_groups (company_id, lower(name));

-- 2. Who is in a Group. A member must be a member of the Company: the row
--    goes with the Company membership. Like a grant row, a membership written
--    before the person's latest (re)join counts for nothing (the rejoin
--    fence below), so a rejoin restores no Group. created_at is when the
--    person was added (the row is never updated: a change of members adds or
--    deletes rows); it uses clock_timestamp(), the clock company_members
--    uses, so a membership written in the same statement as a (re)join
--    passes the fence.
--    added_by_user_id is attribution only and has no foreign key.
CREATE TABLE company_group_members (
    company_id uuid NOT NULL,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    added_by_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (company_id, group_id, user_id),
    FOREIGN KEY (company_id, group_id) REFERENCES company_groups(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE
);

-- "Which Groups is this person in" starts from the person: every access read
-- probes it with the Company and the person.
CREATE INDEX company_group_members_user_idx ON company_group_members (company_id, user_id, group_id);

-- 3. One row per Group per permission on a Project, the shape of
--    project_grants with a Group in place of a person. Every row needs the
--    Group's project.read row on the same Project (the deferred self
--    reference, as project_grants_read_fkey), so a Group never holds a
--    content permission without read.
CREATE TABLE project_group_grants (
    company_id uuid NOT NULL,
    project_id uuid NOT NULL,
    group_id uuid NOT NULL,
    permission text NOT NULL
        CONSTRAINT project_group_grants_permission_check CHECK (permission IN (
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
    PRIMARY KEY (company_id, project_id, group_id, permission),
    FOREIGN KEY (company_id, project_id) REFERENCES projects(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, group_id) REFERENCES company_groups(company_id, id) ON DELETE CASCADE,
    CONSTRAINT project_group_grants_read_fkey
        FOREIGN KEY (company_id, project_id, group_id, read_permission)
        REFERENCES project_group_grants(company_id, project_id, group_id, permission)
        DEFERRABLE INITIALLY DEFERRED
);

-- "Which Projects does this Group reach": a Group change and its preview.
-- The access reads probe the primary key with the Company, the Project and
-- the Group.
CREATE INDEX project_group_grants_group_idx ON project_group_grants (company_id, group_id, project_id);

-- 4. project_permission_held (000086) with a fourth arm: the person is in a
--    Group holding the permission there, through a membership written since
--    their latest (re)join. Same signature and arguments; Go binds every set.
CREATE OR REPLACE FUNCTION project_permission_held(
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
          OR EXISTS (
              SELECT 1
              FROM company_group_members AS group_member
              JOIN project_group_grants AS group_grant
                ON group_grant.company_id = group_member.company_id
               AND group_grant.group_id = group_member.group_id
              WHERE group_member.company_id = $1
                AND group_member.user_id = $3
                AND group_grant.project_id = $2
                AND group_grant.permission = $4
                AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
          )
          OR (project.visibility = 'company' AND $4 = ANY($5))
          OR ($4 = ANY($6) AND member.role = ANY($7))
      )
)
$$;

-- 5. project_access (000086) with the same columns. via says why the person
--    reads: 'grant' when they hold a fenced project.read row of their own,
--    else 'group' when one of their Groups holds project.read there through a
--    fenced membership, else 'company' for a Company-visible Project. The
--    Group path is found by a LATERAL ... LIMIT 1 lookup naming the Project
--    and the person, so the view stays one row per person and Project however
--    many Groups lead there, and each lookup probes
--    company_group_members_user_idx and the project_group_grants key.
--    task_access, conversation_readers, attachment_access, the Inbox and
--    Activity reads and every frame audience read this view, so they follow.
CREATE OR REPLACE VIEW project_access AS
SELECT project.company_id, project.id AS project_id, member.user_id,
       CASE WHEN read_row.user_id IS NOT NULL THEN 'grant'
            WHEN group_read.found THEN 'group'
            ELSE 'company' END::text AS via
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
LEFT JOIN LATERAL (
    SELECT true AS found
    FROM company_group_members AS group_member
    JOIN project_group_grants AS group_grant
      ON group_grant.company_id = group_member.company_id
     AND group_grant.project_id = project.id
     AND group_grant.group_id = group_member.group_id
     AND group_grant.permission = group_grant.read_permission
    WHERE group_member.company_id = member.company_id
      AND group_member.user_id = member.user_id
      AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
    LIMIT 1
) AS group_read ON true
WHERE read_row.user_id IS NOT NULL OR group_read.found OR project.visibility = 'company';

-- 6. output_access (000096) with one change: the Company-visibility arm also
--    reaches a reader of the source Project through a Group, the same fenced
--    Group read path project_access checks. The rest of 000096's body (an
--    Editor grant reads at level 2, and an aggregate export keeps each
--    source's access) is unchanged.
CREATE OR REPLACE VIEW output_access AS
WITH direct_access AS NOT MATERIALIZED (
SELECT output.company_id, output.id AS output_id, member.user_id,
       CASE WHEN output.owner_user_id = member.user_id OR grant_row.permission = 'manage'
            THEN 3 WHEN grant_row.permission = 'edit' THEN 2 ELSE 1 END::integer AS access_level
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
       OR EXISTS (
           SELECT 1 FROM company_group_members AS group_member
           JOIN project_group_grants AS group_grant
             ON group_grant.company_id = group_member.company_id
            AND group_grant.project_id = source_project.id
            AND group_grant.group_id = group_member.group_id
            AND group_grant.permission = group_grant.read_permission
           WHERE group_member.company_id = source_project.company_id
             AND group_member.user_id = member.user_id
             AND group_member.created_at >= COALESCE(member.reactivated_at, member.created_at)
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
   )
)
SELECT access.company_id,access.output_id,access.user_id,access.access_level
FROM direct_access access
WHERE NOT EXISTS (
 SELECT 1 FROM project_presentations presentation
 JOIN project_presentation_sources source ON source.presentation_id=presentation.id
 WHERE presentation.company_id=access.company_id AND presentation.output_id=access.output_id
 -- Sharing the result authorizes its primary snapshot, not other retained Pages.
 AND source.version_id <> presentation.page_version_id
 AND ((source.output_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM direct_access evidence
     WHERE evidence.company_id=source.company_id AND evidence.output_id=source.output_id AND evidence.user_id=access.user_id))
 OR (source.output_id IS NULL AND NOT EXISTS(SELECT 1 FROM project_access reader
     WHERE reader.company_id=source.company_id AND reader.project_id=source.project_id AND reader.user_id=access.user_id)))
);

-- 7. project_access_paths: every stored path by which a person holds a
--    permission on a Project, one row per path and permission: their own
--    fenced grant rows (via 'grant', no Group) and each fenced Group
--    membership's Group rows (via 'group', the Group). Only active members
--    with enabled accounts in a Company that is not being deleted. The
--    Company-visible floor and the management bridge are not stored, so they
--    are not here; Go adds them. A UNION ALL view: reach it with a correlated
--    lookup that names the Project and the person (see the note above
--    conversation_readers in 000086).
CREATE VIEW project_access_paths AS
SELECT grant_row.company_id, grant_row.project_id, grant_row.user_id, grant_row.permission,
       'grant'::text AS via, NULL::uuid AS group_id
FROM project_grants AS grant_row
JOIN companies AS company
  ON company.id = grant_row.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = grant_row.company_id
 AND member.user_id = grant_row.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE grant_row.updated_at >= COALESCE(member.reactivated_at, member.created_at)
UNION ALL
SELECT group_grant.company_id, group_grant.project_id, group_member.user_id, group_grant.permission,
       'group'::text AS via, group_grant.group_id
FROM company_group_members AS group_member
JOIN project_group_grants AS group_grant
  ON group_grant.company_id = group_member.company_id
 AND group_grant.group_id = group_member.group_id
JOIN companies AS company
  ON company.id = group_member.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = group_member.company_id
 AND member.user_id = group_member.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE group_member.created_at >= COALESCE(member.reactivated_at, member.created_at);

-- 8. Inbox: the grant managers of a Project are told when a Group membership
--    opens it to someone (project_access_granted), and the item names the
--    Project itself (entity project).
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal',
        'calendar_reminder','calendar_update','discussion_mention',
        'task_run_requested','task_handoff','analytics_budget','huddle_missed',
        'project_access_granted')),
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company','user','agent','goal','task','run','conversation','message',
        'inbox','resource','runtime','approval','question','deliverable','calendar',
        'project'));
