-- The DELETE is not optional: rows of the new kind exist on any database that
-- ran a sweep, and ADD CONSTRAINT validates the whole table. Rolling back
-- drops business data by policy (see README), and these notices are the least
-- of it — the machine they name is still offline.

DELETE FROM inbox_items WHERE kind = 'runtime_offline';

ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned', 'blocker', 'run_completed', 'run_failed',
        'approval_request', 'question_request'
    ));
