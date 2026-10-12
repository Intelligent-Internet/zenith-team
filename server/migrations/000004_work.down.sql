-- Reverse of 000004_work: break the circular task <-> conversation edge,
-- then drop children-first.
ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_task_id_fkey;
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_proactive_mode_conversation_fkey;
DROP TABLE company_milestone_attestations;
DROP TABLE task_dependencies;
DROP TABLE tasks;
DROP TABLE conversation_run_queue_attachments;
DROP TABLE conversation_run_queue_items;
DROP TABLE company_reference_documents;
DROP TABLE message_attachments;
DROP TABLE conversation_items;
DROP TABLE conversation_user_states;
DROP TABLE conversations;
DROP TABLE attachments;
