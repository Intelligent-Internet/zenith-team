DROP TABLE IF EXISTS commerce_webhook_receipts;
DROP TABLE IF EXISTS commerce_webhook_account_leases;
DROP TABLE IF EXISTS commerce_entitlements;
DROP TABLE IF EXISTS commerce_payments;
DROP TABLE IF EXISTS commerce_subscriptions;
DROP TABLE IF EXISTS commerce_gateway_operations;
DROP TABLE IF EXISTS commerce_rate_limits;
DROP TABLE IF EXISTS commerce_customers;
DROP TABLE IF EXISTS commerce_offers;
ALTER TABLE IF EXISTS approvals
    DROP COLUMN IF EXISTS commerce_manifest_id;
ALTER TABLE IF EXISTS commerce_sites
    DROP CONSTRAINT IF EXISTS commerce_sites_active_manifest_fkey;
DROP TABLE IF EXISTS commerce_manifests;
DROP TABLE IF EXISTS commerce_site_challenges;
DROP TABLE IF EXISTS commerce_site_signing_keys;
DROP TABLE IF EXISTS commerce_sites;

-- Remove provider projections before acceptance
-- receipts. Stripe-side objects are intentionally not deleted by rollback.
DROP TABLE IF EXISTS commerce_operations;
DROP TABLE IF EXISTS commerce_accounts;
DROP TABLE IF EXISTS commerce_terms_acceptances;
DROP TABLE IF EXISTS commerce_terms_catalog;

DROP TABLE mailbox_draft_deliveries;
DROP TABLE mailbox_drafts;
DROP TABLE mailbox_draft_groups;
DROP TABLE mailbox_received_receipts;
DROP TABLE mailbox_message_attachments;
DROP TABLE mailbox_messages;
DROP TABLE company_mailboxes;
DROP TABLE crash_reports;
DROP TABLE inbox_items;
DROP TABLE activity_events;
