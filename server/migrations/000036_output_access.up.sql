-- An Output is the access boundary for all of its immutable items.
-- Attribution is deliberately retained if a user later leaves the Company.
ALTER TABLE outputs
    ADD COLUMN owner_user_id uuid,
    ADD COLUMN access_version bigint NOT NULL DEFAULT 1;

-- Local app preview origins have no server-side artifact authorization.
UPDATE outputs SET preview_url = NULL WHERE preview_url IS NOT NULL;

UPDATE outputs AS output
SET owner_user_id = CASE
    WHEN conversation.kind = 'task' THEN task.creator_user_id
    ELSE conversation.created_by_user_id
END
FROM runs AS run
JOIN conversations AS conversation
  ON conversation.id = run.conversation_id
 AND conversation.company_id = run.company_id
LEFT JOIN tasks AS task
  ON task.id = run.task_id
 AND task.company_id = run.company_id
WHERE output.run_id = run.id
  AND output.company_id = run.company_id;

CREATE INDEX outputs_owner_idx ON outputs (company_id, owner_user_id)
    WHERE owner_user_id IS NOT NULL;

CREATE TABLE output_grants (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    user_id uuid NOT NULL,
    permission text NOT NULL,
    granted_by_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (company_id, output_id, user_id),
    FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, user_id) REFERENCES company_members(company_id, user_id) ON DELETE CASCADE,
    CONSTRAINT output_grants_permission_check CHECK (permission IN ('view', 'manage'))
);

CREATE INDEX output_grants_recipient_idx ON output_grants (company_id, user_id, output_id);

ALTER TABLE companies
    ADD COLUMN artifact_public_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN artifact_policy_version bigint NOT NULL DEFAULT 1;
ALTER TABLE outputs
    ADD COLUMN visibility text NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'shared', 'company', 'public')),
    ADD COLUMN public_token uuid UNIQUE;
UPDATE outputs SET visibility = 'shared'
WHERE EXISTS (SELECT 1 FROM output_grants WHERE output_id = outputs.id);

CREATE TABLE output_conversation_grants (
    company_id uuid NOT NULL,
    output_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    granted_by_user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (company_id, output_id, conversation_id),
    FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, conversation_id) REFERENCES conversations(company_id, id) ON DELETE CASCADE
);
CREATE INDEX output_conversation_grants_conversation_idx
    ON output_conversation_grants(company_id, conversation_id, output_id);

-- The union of current grants is evaluated on every protected read. A public
-- token is a separate anonymous entry point, not company-wide discovery.
CREATE VIEW output_access AS
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
WHERE output.owner_user_id = member.user_id OR grant_row.permission IS NOT NULL
   OR output.visibility = 'company'
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

CREATE VIEW deliverable_inbox_access AS
SELECT deliverable.company_id, deliverable.id::text AS entity_id, access.user_id
FROM deliverables AS deliverable
JOIN output_access AS access
  ON access.company_id = deliverable.company_id
 AND access.output_id = deliverable.output_id;

CREATE TABLE discussion_artifacts (
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    company_id uuid NOT NULL,
    message_id uuid NOT NULL,
    output_id uuid NOT NULL,
    PRIMARY KEY (company_id, message_id, output_id),
    FOREIGN KEY (company_id, message_id) REFERENCES conversation_items(company_id, id) ON DELETE CASCADE,
    FOREIGN KEY (company_id, output_id) REFERENCES outputs(company_id, id) ON DELETE CASCADE
);

-- Existing public provider origins cannot honor the new ACL. Queue their
-- durable deletion before the private-artifact API begins serving reads.
UPDATE output_publications
SET status = 'unpublishing', next_attempt_at = statement_timestamp(),
    lease_id = NULL, lease_expires_at = NULL,
    updated_at = statement_timestamp(), version = version + 1
WHERE status NOT IN ('unpublished', 'unpublishing');
