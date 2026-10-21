-- Reverses 000007_mail_and_social.up.sql.
DROP TABLE IF EXISTS social_posts CASCADE;
DROP TABLE IF EXISTS social_oauth_flows CASCADE;
DROP TABLE IF EXISTS social_targets CASCADE;
DROP TABLE IF EXISTS social_connections CASCADE;
DROP TABLE IF EXISTS mailbox_draft_deliveries CASCADE;
DROP TABLE IF EXISTS mailbox_drafts CASCADE;
DROP TABLE IF EXISTS mailbox_draft_groups CASCADE;
DROP TABLE IF EXISTS mailbox_received_receipts CASCADE;
DROP TABLE IF EXISTS mailbox_message_attachments CASCADE;
DROP TABLE IF EXISTS mailbox_messages CASCADE;
DROP TABLE IF EXISTS company_mailboxes CASCADE;
