-- Freeze the membership grant that received each discussion notification.
-- Older notifications have no proven grant and stay hidden.
ALTER TABLE inbox_items ADD COLUMN discussion_grant_generation bigint;
