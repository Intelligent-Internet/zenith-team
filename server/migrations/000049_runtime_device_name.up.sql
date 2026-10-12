-- The daemon's device name labels its Company resources. SQL reads it, so it
-- is a column rather than a key inside the registration metadata document.
-- NULL means the daemon announced no name.
ALTER TABLE runtimes ADD COLUMN device_name text;

-- The one read of the old key, which leaves the document in the same write.
UPDATE runtimes
SET device_name = NULLIF(metadata->>'deviceName', ''), metadata = metadata - 'deviceName';
