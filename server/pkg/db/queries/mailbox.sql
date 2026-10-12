-- name: GetMailboxStateForMember :one
SELECT
    company_members.role,
    company_mailboxes.id AS mailbox_id,
    company_mailboxes.local_part,
    company_mailboxes.created_at,
    company_mailboxes.version AS mailbox_version,
    (
        SELECT count(*)::bigint
        FROM mailbox_messages
        WHERE mailbox_messages.company_id = company_members.company_id
          AND mailbox_messages.mailbox_id = company_mailboxes.id
          AND mailbox_messages.read_at IS NULL
          AND mailbox_messages.expires_at > clock_timestamp()
    ) AS unread_count
FROM company_members
JOIN companies
  ON companies.id = company_members.company_id
 AND companies.deleting_at IS NULL
LEFT JOIN company_mailboxes
  ON company_mailboxes.company_id = company_members.company_id
WHERE company_members.company_id = sqlc.arg(company_id)::uuid
  AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
  AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[]);

-- name: InsertCompanyMailbox :one
INSERT INTO company_mailboxes (
    company_id, local_part, created_by_user_id
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(local_part)::text,
    sqlc.arg(created_by_user_id)::uuid
)
RETURNING *;

-- name: ListMailboxMessagesForMember :many
SELECT mailbox_messages.*
FROM mailbox_messages
JOIN companies
  ON companies.id = mailbox_messages.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = mailbox_messages.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE mailbox_messages.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_messages.expires_at > clock_timestamp()
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (mailbox_messages.received_at, mailbox_messages.id) < (
          sqlc.arg(before_received_at)::timestamptz,
          sqlc.arg(before_id)::uuid
      )
  )
ORDER BY mailbox_messages.received_at DESC, mailbox_messages.id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: GetMailboxMessageForMember :one
SELECT mailbox_messages.*
FROM mailbox_messages
JOIN companies
  ON companies.id = mailbox_messages.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = mailbox_messages.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE mailbox_messages.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_messages.id = sqlc.arg(message_id)::uuid;

-- name: ListMailboxMessageAttachmentsForMember :many
SELECT mailbox_message_attachments.*
FROM mailbox_message_attachments
JOIN mailbox_messages
  ON mailbox_messages.id = mailbox_message_attachments.message_id
 AND mailbox_messages.company_id = mailbox_message_attachments.company_id
JOIN companies
  ON companies.id = mailbox_messages.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = mailbox_messages.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE mailbox_message_attachments.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_message_attachments.message_id = sqlc.arg(message_id)::uuid
ORDER BY mailbox_message_attachments.created_at, mailbox_message_attachments.id;

-- name: MarkMailboxMessageRead :one
UPDATE mailbox_messages
SET read_at = COALESCE(mailbox_messages.read_at, statement_timestamp()),
    updated_at = CASE
        WHEN mailbox_messages.read_at IS NULL THEN statement_timestamp()
        ELSE mailbox_messages.updated_at
    END,
    version = CASE
        WHEN mailbox_messages.read_at IS NULL THEN mailbox_messages.version + 1
        ELSE mailbox_messages.version
    END
FROM company_members, companies
WHERE mailbox_messages.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_messages.id = sqlc.arg(message_id)::uuid
  AND company_members.company_id = mailbox_messages.company_id
  AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
  AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
  AND companies.id = mailbox_messages.company_id
  AND companies.deleting_at IS NULL
  AND mailbox_messages.version < 9007199254740991
RETURNING mailbox_messages.*;

-- name: GetMailboxAttachmentForMember :one
SELECT
    mailbox_messages.provider_email_id,
    mailbox_messages.expires_at AS message_expires_at,
    mailbox_message_attachments.*
FROM mailbox_message_attachments
JOIN mailbox_messages
  ON mailbox_messages.id = mailbox_message_attachments.message_id
 AND mailbox_messages.company_id = mailbox_message_attachments.company_id
JOIN companies
  ON companies.id = mailbox_messages.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = mailbox_messages.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE mailbox_message_attachments.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_message_attachments.message_id = sqlc.arg(message_id)::uuid
  AND mailbox_message_attachments.id = sqlc.arg(attachment_id)::uuid;

-- name: ResolveMailboxesByLocalParts :many
SELECT company_mailboxes.*
FROM company_mailboxes
JOIN companies
  ON companies.id = company_mailboxes.company_id
 AND companies.deleting_at IS NULL
WHERE company_mailboxes.local_part = ANY(sqlc.arg(local_parts)::text[])
ORDER BY company_mailboxes.id;

-- name: InsertMailboxReceivedReceipt :one
INSERT INTO mailbox_received_receipts (
    provider_event_id, provider_email_id, outcome, received_at, expires_at
)
VALUES (
    sqlc.arg(provider_event_id)::text,
    sqlc.arg(provider_email_id)::text,
    sqlc.arg(outcome)::text,
    sqlc.arg(received_at)::timestamptz,
    sqlc.arg(expires_at)::timestamptz
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: InsertMailboxMessage :one
INSERT INTO mailbox_messages (
    company_id, mailbox_id, provider_email_id, sender, subject,
    recipients, received_at, expires_at, has_attachments
)
VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(mailbox_id)::uuid,
    sqlc.arg(provider_email_id)::text,
    sqlc.arg(sender)::text,
    sqlc.arg(subject)::text,
    sqlc.arg(recipients)::text[],
    sqlc.arg(received_at)::timestamptz,
    sqlc.arg(expires_at)::timestamptz,
    sqlc.arg(has_attachments)::boolean
)
ON CONFLICT (mailbox_id, provider_email_id) DO NOTHING
RETURNING *;

-- name: InsertMailboxMessageAttachment :one
INSERT INTO mailbox_message_attachments (
    message_id, company_id, provider_attachment_id, filename,
    mime_type, size_bytes, disposition, content_id
)
VALUES (
    sqlc.arg(message_id)::uuid,
    sqlc.arg(company_id)::uuid,
    sqlc.arg(provider_attachment_id)::text,
    sqlc.arg(filename)::text,
    sqlc.arg(mime_type)::text,
    sqlc.narg(size_bytes)::bigint,
    sqlc.narg(disposition)::text,
    sqlc.narg(content_id)::text
)
ON CONFLICT (message_id, provider_attachment_id) DO NOTHING
RETURNING *;

-- name: DeleteMailboxAttachmentsByMessageIDs :execrows
DELETE FROM mailbox_message_attachments
WHERE message_id = ANY(sqlc.arg(message_ids)::uuid[]);

-- name: DeleteMailboxMessagesByIDs :execrows
DELETE FROM mailbox_messages
WHERE id = ANY(sqlc.arg(message_ids)::uuid[]);

-- name: ListMailboxMessageRefsByProviderEmailID :many
SELECT id, company_id
FROM mailbox_messages
WHERE provider_email_id = sqlc.arg(provider_email_id)::text
ORDER BY company_id, id
FOR UPDATE;

-- name: ClaimExpiredMailboxMessageRefs :many
SELECT id, company_id
FROM mailbox_messages
WHERE expires_at <= sqlc.arg(now_at)::timestamptz
ORDER BY expires_at, id
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: ClaimExpiredMailboxReceiptIDs :many
SELECT provider_event_id
FROM mailbox_received_receipts
WHERE expires_at <= sqlc.arg(now_at)::timestamptz
ORDER BY expires_at, provider_event_id
LIMIT sqlc.arg(batch_size)::integer
FOR UPDATE SKIP LOCKED;

-- name: DeleteMailboxReceiptsByEventIDs :execrows
DELETE FROM mailbox_received_receipts
WHERE provider_event_id = ANY(sqlc.arg(provider_event_ids)::text[]);

-- Every user-authored Agent Mail mutation takes the Company and active-member
-- locks in Go before this canonical Conversation lock. Creation then locks the
-- exact live Run; draft edits lock their draft rows. Company Email is
-- left-joined so draft creation can distinguish missing configuration from an
-- invalid route without taking the quota row ahead of the Conversation.
-- Drafts require mailbox.manage as well as the chat's readership: a Task
-- chat's Project readers (ADR 0017), any other chat's creator.
-- name: LockAgentMailConversationForMutation :one
-- A Task chat's drafts are changed and sent by readers who may post there:
-- the caller checks project.tasks.write on project_id under this lock
-- (ADR 0017).
SELECT
    conversations.kind AS conversation_kind,
    conversations.project_id,
    conversations.created_by_user_id,
    companies.name AS company_name,
    company_mailboxes.id AS mailbox_id,
    company_mailboxes.local_part
FROM conversations
JOIN companies
  ON companies.id = conversations.company_id
 AND companies.deleting_at IS NULL
LEFT JOIN company_mailboxes
  ON company_mailboxes.company_id = conversations.company_id
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.id = sqlc.arg(conversation_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind <> 'task' AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
FOR UPDATE OF conversations;

-- name: LockAgentMailRunForCreate :one
SELECT runs.id AS run_id, runs.status
FROM runs
WHERE runs.id = sqlc.arg(run_id)::uuid
  AND runs.company_id = sqlc.arg(company_id)::uuid
  AND runs.conversation_id = sqlc.arg(conversation_id)::uuid
  AND runs.agent_id = sqlc.arg(agent_id)::uuid
FOR UPDATE OF runs;
-- name: GetMailboxDraftGroupByRunForUpdate :one
SELECT *
FROM mailbox_draft_groups
WHERE company_id = sqlc.arg(company_id)::uuid
  AND source_run_id = sqlc.arg(source_run_id)::uuid
FOR UPDATE;

-- name: InsertMailboxDraftGroup :one
INSERT INTO mailbox_draft_groups (
    company_id, mailbox_id, conversation_id, source_run_id, request_key,
    agent_id, created_by_user_id, payload_sha256
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(mailbox_id)::uuid,
    sqlc.arg(conversation_id)::uuid,
    sqlc.arg(source_run_id)::uuid,
    sqlc.arg(request_key)::uuid,
    sqlc.arg(agent_id)::uuid,
    sqlc.arg(created_by_user_id)::uuid,
    sqlc.arg(payload_sha256)::bytea
)
RETURNING *;

-- name: InsertMailboxDraft :one
INSERT INTO mailbox_drafts (
    company_id, group_id, ordinal, subject, body_text,
    from_name, from_address
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(group_id)::uuid,
    sqlc.arg(ordinal)::integer,
    sqlc.arg(subject)::text,
    sqlc.arg(body_text)::text,
    sqlc.arg(from_name)::text,
    sqlc.arg(from_address)::text
)
RETURNING *;

-- name: InsertMailboxDraftDelivery :one
INSERT INTO mailbox_draft_deliveries (
    company_id, draft_id, ordinal, to_addresses, cc_addresses
) VALUES (
    sqlc.arg(company_id)::uuid,
    sqlc.arg(draft_id)::uuid,
    sqlc.arg(ordinal)::integer,
    sqlc.arg(to_addresses)::text[],
    sqlc.arg(cc_addresses)::text[]
)
RETURNING *;

-- name: AuthorizeAgentMailConversationForMember :one
SELECT conversations.id, conversations.kind
FROM conversations
JOIN companies
  ON companies.id = conversations.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = conversations.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE conversations.company_id = sqlc.arg(company_id)::uuid
  AND conversations.id = sqlc.arg(conversation_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind <> 'task' AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  );

-- name: ListMailboxDraftGroupsForConversation :many
SELECT *
FROM mailbox_draft_groups
WHERE company_id = sqlc.arg(company_id)::uuid
  AND conversation_id = sqlc.arg(conversation_id)::uuid
  AND source_run_id = ANY(sqlc.arg(source_run_ids)::uuid[])
ORDER BY created_at, id
LIMIT 100;

-- name: GetMailboxDraftGroup :one
SELECT *
FROM mailbox_draft_groups
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(group_id)::uuid;

-- name: GetMailboxDraftGroupWithConversationRoute :one
SELECT
    sqlc.embed(mailbox_draft_groups),
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id
FROM mailbox_draft_groups
JOIN conversations
  ON conversations.id = mailbox_draft_groups.conversation_id
 AND conversations.company_id = mailbox_draft_groups.company_id
WHERE mailbox_draft_groups.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_draft_groups.id = sqlc.arg(group_id)::uuid;

-- name: ListMailboxDraftsForGroups :many
SELECT *
FROM mailbox_drafts
WHERE company_id = sqlc.arg(company_id)::uuid
  AND group_id = ANY(sqlc.arg(group_ids)::uuid[])
ORDER BY group_id, ordinal, id;

-- name: ListMailboxDraftDeliveriesForDrafts :many
SELECT *
FROM mailbox_draft_deliveries
WHERE company_id = sqlc.arg(company_id)::uuid
  AND draft_id = ANY(sqlc.arg(draft_ids)::uuid[])
ORDER BY draft_id, ordinal, id;

-- name: LockMailboxDraftForMember :one
SELECT
    sqlc.embed(mailbox_drafts),
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id
FROM mailbox_drafts
JOIN mailbox_draft_groups
  ON mailbox_draft_groups.id = mailbox_drafts.group_id
 AND mailbox_draft_groups.company_id = mailbox_drafts.company_id
JOIN conversations
  ON conversations.id = mailbox_draft_groups.conversation_id
 AND conversations.company_id = mailbox_drafts.company_id
JOIN companies
  ON companies.id = mailbox_drafts.company_id
 AND companies.deleting_at IS NULL
JOIN company_members
 ON company_members.company_id = mailbox_drafts.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
WHERE mailbox_drafts.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_drafts.id = sqlc.arg(draft_id)::uuid
  AND mailbox_draft_groups.conversation_id = sqlc.arg(conversation_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind <> 'task' AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
FOR UPDATE OF mailbox_draft_groups, mailbox_drafts;

-- name: UpdateMailboxDraftContent :one
UPDATE mailbox_drafts
SET subject = sqlc.arg(subject)::text,
    body_text = sqlc.arg(body_text)::text,
    status = 'draft',
    failed_at = NULL,
    failure_reason = NULL,
    updated_at = clock_timestamp(),
    version = mailbox_drafts.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND status IN ('draft', 'failed')
  AND version < 9007199254740991
RETURNING *;

-- name: DeleteMailboxDraftDeliveries :execrows
DELETE FROM mailbox_draft_deliveries
WHERE company_id = sqlc.arg(company_id)::uuid
  AND draft_id = sqlc.arg(draft_id)::uuid;

-- name: GetMailboxDraftGroupSize :one
SELECT
    count(*)::bigint AS delivery_count,
    COALESCE(sum(
        cardinality(mailbox_draft_deliveries.to_addresses) +
        cardinality(mailbox_draft_deliveries.cc_addresses)
    ), 0)::bigint AS recipient_count
FROM mailbox_drafts
JOIN mailbox_draft_deliveries
  ON mailbox_draft_deliveries.draft_id = mailbox_drafts.id
 AND mailbox_draft_deliveries.company_id = mailbox_drafts.company_id
WHERE mailbox_drafts.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_drafts.group_id = sqlc.arg(group_id)::uuid;

-- name: DeleteMailboxDraft :execrows
DELETE FROM mailbox_drafts
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND status IN ('draft', 'failed');

-- name: LockCompanyMailboxForAgentMailSend :one
SELECT company_mailboxes.id
FROM company_mailboxes
WHERE company_mailboxes.company_id = sqlc.arg(company_id)::uuid
FOR UPDATE OF company_mailboxes;

-- name: CountAgentMailRecipientUnitsSince :one
SELECT COALESCE(sum(
    cardinality(mailbox_draft_deliveries.to_addresses) +
    cardinality(mailbox_draft_deliveries.cc_addresses)
), 0)::bigint
FROM mailbox_drafts
JOIN mailbox_draft_deliveries
  ON mailbox_draft_deliveries.draft_id = mailbox_drafts.id
 AND mailbox_draft_deliveries.company_id = mailbox_drafts.company_id
WHERE mailbox_drafts.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_drafts.status IN ('sending', 'sent')
  AND mailbox_drafts.send_requested_at >= sqlc.arg(since_at)::timestamptz;

-- name: QueueMailboxDraft :one
UPDATE mailbox_drafts
SET status = 'sending',
    send_generation = mailbox_drafts.send_generation + 1,
    send_requested_at = sqlc.arg(now_at)::timestamptz,
    send_lease_id = NULL,
    send_lease_expires_at = NULL,
    send_attempts = 0,
    next_attempt_at = sqlc.arg(now_at)::timestamptz,
    sent_by_user_id = sqlc.arg(sent_by_user_id)::uuid,
    sent_at = NULL,
    failed_at = NULL,
    failure_reason = NULL,
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = mailbox_drafts.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND version = sqlc.arg(expected_version)::bigint
  AND status IN ('draft', 'failed')
  AND version < 9007199254740991
RETURNING *;

-- name: ResetMailboxDraftDeliveryAcceptance :execrows
UPDATE mailbox_draft_deliveries
SET provider_email_id = NULL,
    accepted_at = NULL
WHERE company_id = sqlc.arg(company_id)::uuid
  AND draft_id = sqlc.arg(draft_id)::uuid;

-- name: ListMailboxGroupDraftsForMemberUpdate :many
SELECT
    sqlc.embed(mailbox_drafts),
    conversations.kind AS conversation_kind,
    conversations.created_by_user_id
FROM mailbox_drafts
JOIN mailbox_draft_groups
  ON mailbox_draft_groups.id = mailbox_drafts.group_id
 AND mailbox_draft_groups.company_id = mailbox_drafts.company_id
JOIN conversations
  ON conversations.id = mailbox_draft_groups.conversation_id
 AND conversations.company_id = mailbox_drafts.company_id
JOIN company_members
 ON company_members.company_id = mailbox_drafts.company_id
 AND company_members.user_id = sqlc.arg(actor_user_id)::uuid
 AND company_members.status = 'active'
  AND company_members.role = ANY(sqlc.arg(actor_roles)::text[])
JOIN companies
  ON companies.id = mailbox_drafts.company_id
 AND companies.deleting_at IS NULL
WHERE mailbox_drafts.company_id = sqlc.arg(company_id)::uuid
  AND mailbox_draft_groups.id = sqlc.arg(group_id)::uuid
  AND mailbox_draft_groups.conversation_id = sqlc.arg(conversation_id)::uuid
  AND (
      (conversations.kind = 'task' AND EXISTS (
          SELECT 1 FROM conversation_readers AS reader
          WHERE reader.company_id = conversations.company_id
            AND reader.conversation_id = conversations.id
            AND reader.user_id = sqlc.arg(actor_user_id)::uuid
      ))
      OR (conversations.kind <> 'task' AND conversations.created_by_user_id = sqlc.arg(actor_user_id)::uuid)
  )
ORDER BY mailbox_drafts.ordinal, mailbox_drafts.id
FOR UPDATE OF mailbox_drafts;

-- name: ClaimNextMailboxDraftSend :one
WITH candidate AS MATERIALIZED (
    SELECT drafts.id
    FROM mailbox_drafts AS drafts
    JOIN mailbox_draft_groups AS groups
      ON groups.company_id = drafts.company_id
     AND groups.id = drafts.group_id
    JOIN conversations
      ON conversations.company_id = groups.company_id
     AND conversations.id = groups.conversation_id
    JOIN companies
      ON companies.id = drafts.company_id
     AND companies.deleting_at IS NULL
    WHERE drafts.status = 'sending'
      AND drafts.next_attempt_at <= sqlc.arg(now_at)::timestamptz
      AND (
          drafts.send_lease_expires_at IS NULL
          OR drafts.send_lease_expires_at <= sqlc.arg(now_at)::timestamptz
      )
    ORDER BY drafts.next_attempt_at, drafts.created_at, drafts.id
    LIMIT 1
    FOR UPDATE OF drafts SKIP LOCKED
)
UPDATE mailbox_drafts
SET send_lease_id = sqlc.arg(lease_id)::uuid,
    send_lease_expires_at = sqlc.arg(lease_expires_at)::timestamptz,
    send_attempts = mailbox_drafts.send_attempts + 1,
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = mailbox_drafts.version + 1
FROM candidate
WHERE mailbox_drafts.id = candidate.id
  AND mailbox_drafts.version < 9007199254740991
RETURNING mailbox_drafts.*;

-- name: LockMailboxDraftSendLease :one
SELECT *
FROM mailbox_drafts
WHERE id = sqlc.arg(draft_id)::uuid
  AND company_id = sqlc.arg(company_id)::uuid
  AND status = 'sending'
  AND send_generation = sqlc.arg(send_generation)::bigint
  AND send_lease_id = sqlc.arg(lease_id)::uuid
FOR UPDATE;

-- name: AcceptMailboxDraftDelivery :one
UPDATE mailbox_draft_deliveries
SET provider_email_id = sqlc.arg(provider_email_id)::text,
    accepted_at = sqlc.arg(accepted_at)::timestamptz
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(delivery_id)::uuid
  AND draft_id = sqlc.arg(draft_id)::uuid
RETURNING *;

-- name: MarkMailboxDraftSent :one
UPDATE mailbox_drafts
SET status = 'sent',
    sent_at = sqlc.arg(sent_at)::timestamptz,
    send_lease_id = NULL,
    send_lease_expires_at = NULL,
    next_attempt_at = NULL,
    failure_reason = NULL,
    updated_at = sqlc.arg(sent_at)::timestamptz,
    version = mailbox_drafts.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND status = 'sending'
  AND send_generation = sqlc.arg(send_generation)::bigint
  AND send_lease_id = sqlc.arg(lease_id)::uuid
  AND version < 9007199254740991
RETURNING *;

-- name: RescheduleMailboxDraftSend :one
UPDATE mailbox_drafts
SET send_lease_id = NULL,
    send_lease_expires_at = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
    failure_reason = sqlc.arg(failure_reason)::text,
    updated_at = sqlc.arg(now_at)::timestamptz,
    version = mailbox_drafts.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND status = 'sending'
  AND send_generation = sqlc.arg(send_generation)::bigint
  AND send_lease_id = sqlc.arg(lease_id)::uuid
  AND version < 9007199254740991
RETURNING *;

-- name: MarkMailboxDraftFailed :one
UPDATE mailbox_drafts
SET status = 'failed',
    failed_at = sqlc.arg(failed_at)::timestamptz,
    send_lease_id = NULL,
    send_lease_expires_at = NULL,
    next_attempt_at = NULL,
    failure_reason = sqlc.arg(failure_reason)::text,
    updated_at = sqlc.arg(failed_at)::timestamptz,
    version = mailbox_drafts.version + 1
WHERE company_id = sqlc.arg(company_id)::uuid
  AND id = sqlc.arg(draft_id)::uuid
  AND status = 'sending'
  AND send_generation = sqlc.arg(send_generation)::bigint
  AND send_lease_id = sqlc.arg(lease_id)::uuid
  AND version < 9007199254740991
RETURNING *;
