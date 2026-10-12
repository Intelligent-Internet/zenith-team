DROP TABLE company_invitation_tokens;
DROP TABLE company_invitations;
DELETE FROM inbox_items WHERE kind='invitation_renewal';
ALTER TABLE inbox_items DROP CONSTRAINT inbox_items_kind_check,
ADD CONSTRAINT inbox_items_kind_check CHECK (kind IN ('task_assigned','blocker','run_completed','run_failed','approval_request','question_request','runtime_offline'));
