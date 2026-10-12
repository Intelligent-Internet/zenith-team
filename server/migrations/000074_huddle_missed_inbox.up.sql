ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal',
        'calendar_reminder','calendar_update','discussion_mention',
        'task_run_requested','task_handoff','analytics_budget','huddle_missed'));
