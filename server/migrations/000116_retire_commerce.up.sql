-- Commerce is no longer served. Preserve its records and past decisions, but
-- release human-input waits and prevent queued continuations from restarting
-- a workflow whose tools and approval action no longer exist.
UPDATE approvals
SET status = CASE WHEN status = 'pending' THEN 'cancelled' ELSE status END,
    decision_note = CASE WHEN status = 'pending'
        THEN 'Commerce is no longer available.' ELSE decision_note END,
    continuation_status = 'abandoned',
    continuation_abandon_reason = 'feature_removed',
    continuation_next_attempt_at = NULL,
    updated_at = statement_timestamp(),
    version = version + 1
WHERE type = 'commerce_manifest_publish'
  AND (status = 'pending' OR continuation_status = 'pending');

-- Retired approvals have no review surface. Close their Inbox requests while
-- retaining each notification in the archived history.
UPDATE inbox_items
SET read_at = COALESCE(inbox_items.read_at, statement_timestamp()),
    archived_at = COALESCE(inbox_items.archived_at, statement_timestamp()),
    updated_at = statement_timestamp(),
    version = inbox_items.version + 1
FROM approvals
WHERE approvals.type = 'commerce_manifest_publish'
  AND inbox_items.company_id = approvals.company_id
  AND inbox_items.entity_type = 'approval'
  AND inbox_items.entity_id = approvals.id::text
  AND (inbox_items.read_at IS NULL OR inbox_items.archived_at IS NULL);
