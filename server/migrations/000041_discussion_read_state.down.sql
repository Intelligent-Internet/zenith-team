DROP INDEX conversation_items_discussion_unread_idx;
ALTER TABLE conversation_thread_states DROP COLUMN unread_count, DROP COLUMN manual_unread_seq;
ALTER TABLE conversation_user_states DROP COLUMN unread_count, DROP COLUMN manual_unread_seq, DROP COLUMN main_read_seq;
