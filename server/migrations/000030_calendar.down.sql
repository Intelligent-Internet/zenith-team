DELETE FROM inbox_items WHERE kind IN ('calendar_reminder','calendar_update');
DROP INDEX inbox_items_account_created_idx;
DROP INDEX inbox_items_account_unread_idx;
ALTER TABLE inbox_items
    DROP CONSTRAINT inbox_items_scope_check,
    DROP CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key,
    ADD CONSTRAINT inbox_items_company_id_recipient_user_id_dedupe_key_key
        UNIQUE (company_id,recipient_user_id,dedupe_key),
    DROP CONSTRAINT inbox_items_kind_check,
    ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN (
        'task_assigned','blocker','run_completed','run_failed','approval_request',
        'question_request','runtime_offline','invitation_renewal')),
    DROP CONSTRAINT inbox_items_entity_type_check,
    ADD CONSTRAINT inbox_items_entity_type_check CHECK (entity_type IN (
        'company','user','agent','goal','task','run','conversation','message',
        'inbox','resource','runtime','approval','question','deliverable'));
ALTER TABLE inbox_items ALTER COLUMN company_id SET NOT NULL;

DROP TABLE calendar_tool_receipts,calendar_reminder_states,calendar_attendees,calendar_occurrence_states;
DROP TABLE calendar_events;
DROP TABLE calendars;
