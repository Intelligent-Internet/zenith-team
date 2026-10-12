-- Run identity cutover: stop old writers/claims and drain active executions first.
-- This forward migration preserves the old-to-new map for the offline export
-- needed to reconcile retained daemon files. Runtime code never uses numeric IDs.
BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM runs WHERE status IN ('dispatched', 'running', 'waiting_local_directory')) THEN
        RAISE EXCEPTION 'drain active Runs before the UUID identity migration';
    END IF;
    IF EXISTS (SELECT 1 FROM task_merge_jobs WHERE status = 'executing') THEN
        RAISE EXCEPTION 'finish and acknowledge executing Task merges before the UUID identity migration';
    END IF;
END $$;

CREATE TABLE run_identity_mappings (
    legacy_id bigint PRIMARY KEY,
    run_id uuid DEFAULT gen_random_uuid() NOT NULL UNIQUE,
    created_at timestamptz DEFAULT now() NOT NULL
);

INSERT INTO run_identity_mappings (legacy_id)
SELECT run_id AS legacy_id FROM conversation_items WHERE run_id IS NOT NULL
UNION
SELECT id AS legacy_id FROM runs WHERE id IS NOT NULL
UNION
SELECT parent_run_id AS legacy_id FROM runs WHERE parent_run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM task_merge_jobs WHERE run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM run_tokens WHERE run_id IS NOT NULL
UNION
SELECT origin_run_id AS legacy_id FROM approvals WHERE origin_run_id IS NOT NULL
UNION
SELECT continuation_run_id AS legacy_id FROM approvals WHERE continuation_run_id IS NOT NULL
UNION
SELECT origin_run_id AS legacy_id FROM questions WHERE origin_run_id IS NOT NULL
UNION
SELECT continuation_run_id AS legacy_id FROM questions WHERE continuation_run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM schedule_firings WHERE run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM task_report_batches WHERE run_id IS NOT NULL
UNION
SELECT source_run_id AS legacy_id FROM task_report_events WHERE source_run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM activity_events WHERE run_id IS NOT NULL
UNION
SELECT source_run_id AS legacy_id FROM mailbox_draft_groups WHERE source_run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM commerce_manifests WHERE run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM run_usage WHERE run_id IS NOT NULL
UNION
SELECT source_run_id AS legacy_id FROM asset_versions WHERE source_run_id IS NOT NULL
UNION
SELECT run_id AS legacy_id FROM outputs WHERE run_id IS NOT NULL
UNION
SELECT legacy_id FROM (
    SELECT CASE WHEN candidate ~ '^[1-9][0-9]{0,18}$'
        THEN CASE WHEN candidate::numeric <= 9223372036854775807
            THEN candidate::bigint END END AS legacy_id
    FROM (
        SELECT entity_id AS candidate FROM activity_events WHERE entity_type = 'run'
        UNION SELECT entity_id FROM inbox_items WHERE entity_type = 'run'
        UNION SELECT substring(deep_link FROM '^/runs/([1-9][0-9]*)$') FROM inbox_items
        UNION SELECT substring(deep_link FROM '[?&]runId=([1-9][0-9]*)(?:&|$)')
            FROM inbox_items WHERE deep_link LIKE '/app?%'
        UNION SELECT substring(dedupe_key FROM '^run:([1-9][0-9]*):(?:completed|failed)$') FROM inbox_items
    ) AS owned_references
) AS parsed_references WHERE legacy_id IS NOT NULL;

-- Keep ownership, tenancy and deferrability byte-for-byte while both sides
-- change type. Attribution columns deliberately have no FK to restore.
DO $$ BEGIN
    CREATE TEMP TABLE run_foreign_keys ON COMMIT DROP AS
    SELECT conrelid::regclass::text AS table_name, conname AS constraint_name,
           pg_get_constraintdef(oid) AS definition
    FROM pg_constraint WHERE contype = 'f' AND confrelid = 'runs'::regclass;
END $$;

DO $$
DECLARE edge record;
BEGIN
    FOR edge IN SELECT * FROM run_foreign_keys LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', edge.table_name, edge.constraint_name);
    END LOOP;
END $$;

CREATE FUNCTION pg_temp.mapped_run_id(old_id bigint) RETURNS uuid
LANGUAGE sql STABLE STRICT AS $$
    SELECT run_id FROM run_identity_mappings WHERE legacy_id = old_id;
$$;

ALTER TABLE runs ALTER COLUMN id DROP DEFAULT;
ALTER TABLE conversation_items ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE runs ALTER COLUMN id TYPE uuid USING pg_temp.mapped_run_id(id);
ALTER TABLE runs ALTER COLUMN parent_run_id TYPE uuid USING pg_temp.mapped_run_id(parent_run_id);
ALTER TABLE task_merge_jobs ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE run_tokens ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE approvals ALTER COLUMN origin_run_id TYPE uuid USING pg_temp.mapped_run_id(origin_run_id);
ALTER TABLE approvals ALTER COLUMN continuation_run_id TYPE uuid USING pg_temp.mapped_run_id(continuation_run_id);
ALTER TABLE questions ALTER COLUMN origin_run_id TYPE uuid USING pg_temp.mapped_run_id(origin_run_id);
ALTER TABLE questions ALTER COLUMN continuation_run_id TYPE uuid USING pg_temp.mapped_run_id(continuation_run_id);
ALTER TABLE schedule_firings ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE task_report_batches ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE task_report_events ALTER COLUMN source_run_id TYPE uuid USING pg_temp.mapped_run_id(source_run_id);
ALTER TABLE activity_events ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE mailbox_draft_groups ALTER COLUMN source_run_id TYPE uuid USING pg_temp.mapped_run_id(source_run_id);
ALTER TABLE commerce_manifests ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE run_usage ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE asset_versions ALTER COLUMN source_run_id TYPE uuid USING pg_temp.mapped_run_id(source_run_id);
ALTER TABLE outputs ALTER COLUMN run_id TYPE uuid USING pg_temp.mapped_run_id(run_id);
ALTER TABLE runs ALTER COLUMN id SET DEFAULT gen_random_uuid();
DROP SEQUENCE runs_id_seq;

DO $$
DECLARE edge record;
BEGIN
    FOR edge IN SELECT * FROM run_foreign_keys LOOP
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s', edge.table_name, edge.constraint_name, edge.definition);
    END LOOP;
END $$;

UPDATE activity_events AS activity
SET entity_id = mapping.run_id::text
FROM run_identity_mappings AS mapping
WHERE activity.entity_type = 'run' AND activity.entity_id = mapping.legacy_id::text;

-- Rewrite only application-owned references, leaving notification text intact.
UPDATE inbox_items AS item SET entity_id = mapping.run_id::text
FROM run_identity_mappings AS mapping
WHERE item.entity_type = 'run' AND item.entity_id = mapping.legacy_id::text;
UPDATE inbox_items AS item SET deep_link = '/runs/' || mapping.run_id::text
FROM run_identity_mappings AS mapping
WHERE item.deep_link = '/runs/' || mapping.legacy_id::text;
UPDATE inbox_items AS item
SET deep_link = regexp_replace(item.deep_link,
    '([?&]runId=)' || mapping.legacy_id::text || '(&|$)',
    '\1' || mapping.run_id::text || '\2')
FROM run_identity_mappings AS mapping
WHERE item.deep_link LIKE '/app?%'
  AND item.deep_link ~ ('[?&]runId=' || mapping.legacy_id::text || '(&|$)');
UPDATE inbox_items AS item
SET dedupe_key = 'run:' || mapping.run_id::text || ':' || split_part(item.dedupe_key, ':', 3)
FROM run_identity_mappings AS mapping
WHERE item.dedupe_key IN ('run:' || mapping.legacy_id::text || ':completed',
                         'run:' || mapping.legacy_id::text || ':failed');

-- Labels and old input revisions cannot be inferred from today's source state.
ALTER TABLE runs ADD COLUMN run_type text;
ALTER TABLE runs ADD COLUMN revision bigint;
UPDATE runs SET run_type = CASE conversations.kind
    WHEN 'direct' THEN 'direct_chat' ELSE conversations.kind END
FROM conversations WHERE conversations.id = runs.conversation_id;
ALTER TABLE runs ALTER COLUMN run_type SET NOT NULL;
ALTER TABLE runs ADD CONSTRAINT runs_run_type_check
    CHECK (run_type IN ('task', 'direct_chat', 'schedule'));
ALTER TABLE runs ADD CONSTRAINT runs_revision_check
    CHECK (revision IS NULL OR revision BETWEEN 1 AND 9007199254740991);

COMMIT;
