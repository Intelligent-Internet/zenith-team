-- Drain old Studio launchers before upgrading: v1 reserve/bind is no longer safe.
ALTER TABLE conversation_studio_projects
    ADD COLUMN api_origin text,
    ADD COLUMN binding_revision bigint NOT NULL DEFAULT 1 CHECK (binding_revision > 0),
    ADD COLUMN creation_protocol text NOT NULL DEFAULT 'legacy_marker' CHECK (creation_protocol IN ('legacy_marker','receipt_v1')),
    ADD COLUMN creation_account_id uuid,
    ADD COLUMN creation_namespace uuid,
    ADD COLUMN creation_request_hash text,
    ADD COLUMN created_by_user_id uuid;
UPDATE conversation_studio_projects SET api_origin = 'https://api-agent.ii.inc';
ALTER TABLE conversation_studio_projects ALTER COLUMN api_origin SET NOT NULL;
ALTER TABLE conversation_studio_projects DROP CONSTRAINT conversation_studio_projects_project_id_key;
CREATE UNIQUE INDEX conversation_studio_projects_origin_project_key
    ON conversation_studio_projects(api_origin, project_id) WHERE project_id IS NOT NULL;
ALTER TABLE conversation_studio_projects ADD CONSTRAINT studio_receipt_identity CHECK (
    creation_protocol = 'legacy_marker' OR (
        creation_account_id IS NOT NULL AND creation_namespace IS NOT NULL AND
        creation_request_hash IS NOT NULL AND creation_request_hash ~ '^[0-9a-f]{64}$' AND
        created_by_user_id IS NOT NULL AND length(description) <= 2000
    )
);
