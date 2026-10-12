DROP TABLE output_publications;
DROP TABLE deliverable_review_actions;
DROP TABLE deliverables;
DROP TABLE output_items;
DROP TABLE outputs;
DROP TABLE asset_versions;
DROP TABLE assets;

ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_company_id_id_key,
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company', 'user', 'agent', 'goal', 'task', 'run', 'conversation',
        'message', 'inbox', 'resource', 'runtime', 'approval', 'question'
    ));
