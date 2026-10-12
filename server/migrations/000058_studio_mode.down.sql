DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM conversation_studio_projects WHERE
        api_origin <> 'https://api-agent.ii.inc' OR creation_protocol = 'receipt_v1') THEN
        RAISE EXCEPTION 'Studio receipt/origin identity must be retained';
    END IF;
END $$;
DROP INDEX conversation_studio_projects_origin_project_key;
ALTER TABLE conversation_studio_projects ADD UNIQUE(project_id);
ALTER TABLE conversation_studio_projects
    DROP CONSTRAINT studio_receipt_identity,
    DROP COLUMN api_origin, DROP COLUMN binding_revision, DROP COLUMN creation_protocol,
    DROP COLUMN creation_account_id, DROP COLUMN creation_namespace,
    DROP COLUMN creation_request_hash, DROP COLUMN created_by_user_id;
