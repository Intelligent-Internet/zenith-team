ALTER TABLE conversation_user_states
    ADD COLUMN main_read_seq bigint NOT NULL DEFAULT 0 CHECK(main_read_seq BETWEEN 0 AND 9007199254740991),
    ADD COLUMN manual_unread_seq bigint CHECK(manual_unread_seq BETWEEN 1 AND 9007199254740991);
ALTER TABLE conversation_user_states ADD COLUMN unread_count bigint NOT NULL DEFAULT 0 CHECK(unread_count>=0);
ALTER TABLE conversation_thread_states ADD COLUMN unread_count bigint NOT NULL DEFAULT 0 CHECK(unread_count>=0);
ALTER TABLE conversation_thread_states
    ADD COLUMN manual_unread_seq bigint CHECK(manual_unread_seq BETWEEN 1 AND 9007199254740991);
UPDATE conversation_user_states s SET main_read_seq=m.joined_seq
FROM conversation_members m WHERE m.conversation_id=s.conversation_id AND m.user_id=s.user_id;
CREATE INDEX conversation_items_discussion_unread_idx
    ON conversation_items(conversation_id,thread_root_id,seq) INCLUDE(author_id)
    WHERE run_id IS NULL AND type='user_message' AND deleted_at IS NULL;

UPDATE conversation_user_states s SET unread_count=(SELECT count(*) FROM conversation_items i WHERE i.conversation_id=s.conversation_id AND i.thread_root_id IS NULL AND i.run_id IS NULL AND i.type='user_message' AND i.deleted_at IS NULL AND i.author_id<>s.user_id AND i.seq>s.main_read_seq) WHERE s.grant_generation IS NOT NULL;
INSERT INTO conversation_thread_states(company_id,conversation_id,root_id,user_id,grant_generation,follow,read_seq)
SELECT m.company_id,m.conversation_id,i.id,m.user_id,m.generation,'auto',m.joined_seq
FROM conversation_members m JOIN conversations c ON c.id=m.conversation_id AND c.kind='human_direct'
JOIN conversation_items i ON i.conversation_id=c.id AND i.thread_root_id IS NULL AND i.type='user_message' AND i.run_id IS NULL
WHERE m.status='active' AND EXISTS(SELECT 1 FROM conversation_items r WHERE r.thread_root_id=i.id)
ON CONFLICT(root_id,user_id) DO NOTHING;
UPDATE conversation_thread_states s SET unread_count=(SELECT count(*) FROM conversation_items i WHERE i.thread_root_id=s.root_id AND i.run_id IS NULL AND i.type='user_message' AND i.deleted_at IS NULL AND i.author_id<>s.user_id AND i.seq>s.read_seq) WHERE (s.follow<>'unfollowed' OR EXISTS(SELECT 1 FROM conversations c WHERE c.id=s.conversation_id AND c.kind='human_direct')) AND EXISTS(SELECT 1 FROM conversation_members m WHERE m.conversation_id=s.conversation_id AND m.user_id=s.user_id AND m.generation=s.grant_generation AND m.status='active');
