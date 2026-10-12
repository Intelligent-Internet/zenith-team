ALTER TABLE conversations ADD COLUMN document_thread_id uuid,
 ADD CONSTRAINT conversations_document_thread_fkey FOREIGN KEY (company_id, document_thread_id) REFERENCES review_threads(company_id,id) ON DELETE CASCADE,
 ADD CONSTRAINT conversations_document_thread_check CHECK (document_thread_id IS NULL OR (kind='direct' AND project_id IS NOT NULL));
CREATE UNIQUE INDEX conversations_document_agent_unique ON conversations(document_thread_id,agent_id,created_by_user_id) WHERE document_thread_id IS NOT NULL;
ALTER TABLE review_comments ADD COLUMN author_agent_id uuid,
 ADD COLUMN agent_run_id uuid REFERENCES runs(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX review_comments_agent_run_unique ON review_comments(agent_run_id) WHERE agent_run_id IS NOT NULL;

CREATE OR REPLACE VIEW conversation_readers AS
-- Task chats follow their Project.
SELECT conversation.company_id, conversation.id AS conversation_id, access.user_id
FROM conversations AS conversation
JOIN project_access AS access
  ON access.company_id = conversation.company_id AND access.project_id = conversation.project_id
WHERE conversation.kind = 'task'
UNION ALL
-- Task chats whose Task was deleted before this migration recorded no
-- Project; they stay readable by every active member (a known pocket, ADR
-- 0017). conversations_task_project_check admits a NULL Project only on such a
-- chat, and task_id IS NULL repeats that here so a live Task's chat never
-- falls into this arm.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'task' AND conversation.project_id IS NULL AND conversation.task_id IS NULL
UNION ALL
-- Channels, human direct messages and Huddle chats follow their active
-- participants, as ListDiscussionAccess does.
SELECT conversation.company_id, conversation.id, participant.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN conversation_members AS participant
  ON participant.company_id = conversation.company_id
 AND participant.conversation_id = conversation.id
 AND participant.status = 'active'
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = participant.user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('channel', 'human_direct', 'huddle')
UNION ALL
-- A channel Agent's execution chat is read by the callers of its live
-- invocations: the live-caller arm of LockHumanInputOriginConversation. The
-- member who created the channel Agent chat is deliberately not a reader
-- through that role alone (ADR 0017).
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind = 'channel_agent'
  AND EXISTS (
      SELECT 1
      FROM channel_invocations AS invocation
      WHERE invocation.company_id = conversation.company_id
        AND invocation.conversation_id = conversation.id
        AND invocation.caller_user_id = member.user_id
        AND invocation.status IN ('active', 'waiting_input')
  )
UNION ALL
-- Direct and Schedule chats are their creator's alone.
SELECT conversation.company_id, conversation.id, member.user_id
FROM conversations AS conversation
JOIN companies AS company
  ON company.id = conversation.company_id AND company.deleting_at IS NULL
JOIN company_members AS member
  ON member.company_id = conversation.company_id
 AND member.user_id = conversation.created_by_user_id
 AND member.status = 'active'
JOIN users AS account
  ON account.id = member.user_id AND account.disabled_at IS NULL AND account.deleted_at IS NULL
WHERE conversation.kind IN ('direct', 'schedule')
 AND (conversation.document_thread_id IS NULL OR EXISTS (SELECT 1 FROM project_access access WHERE access.company_id=conversation.company_id AND access.project_id=conversation.project_id AND access.user_id=member.user_id));

-- Freeze the passage at request time; the provider returns text, never mutations.
CREATE TABLE document_writing_requests (
 company_id uuid NOT NULL,
 run_id uuid PRIMARY KEY,
 page_id uuid NOT NULL,
 request_key uuid NOT NULL,
 target jsonb NOT NULL,
 before_content jsonb NOT NULL,
 sequence bigint NOT NULL CHECK(sequence >= 0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(company_id,run_id) REFERENCES runs(company_id,id) ON DELETE CASCADE,
 FOREIGN KEY(company_id,page_id) REFERENCES wiki_pages(company_id,id) ON DELETE CASCADE
);
