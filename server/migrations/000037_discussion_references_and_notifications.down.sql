DELETE FROM inbox_items WHERE kind='discussion_mention';
DROP INDEX conversation_items_discussion_reference_idx;
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal','calendar_reminder',
        'calendar_update'));
