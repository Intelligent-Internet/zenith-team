-- 000013_runtime_offline_inbox: the `runtime_offline` Inbox kind.
--
-- The stale runtime sweep raises one item per recipient when it takes a
-- machine offline that still held in-flight runs — the first Inbox item
-- whose subject is a machine rather than a piece of work. `entity_type`
-- already admits 'runtime' (000009); only the kind vocabulary has to grow,
-- and it grows under the same constraint name so the schema pin keeps
-- addressing one constraint.

ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned', 'blocker', 'run_completed', 'run_failed',
        'approval_request', 'question_request', 'runtime_offline'
    ));
