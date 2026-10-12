DROP TABLE discussion_artifacts;
DROP VIEW deliverable_inbox_access;
DROP VIEW output_access;
DROP TABLE output_conversation_grants;
DROP TABLE output_grants;
ALTER TABLE outputs
    DROP COLUMN owner_user_id, DROP COLUMN access_version,
    DROP COLUMN visibility, DROP COLUMN public_token;
ALTER TABLE companies DROP COLUMN artifact_public_enabled, DROP COLUMN artifact_policy_version;
