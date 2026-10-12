-- Replace transport-shaped Output types with the four user-facing artifact
-- classes rendered beside a Conversation.
ALTER TABLE outputs
    DROP CONSTRAINT outputs_type_check;

UPDATE outputs
SET type = CASE type
    WHEN 'webapp' THEN 'app'
    ELSE 'file'
END;

ALTER TABLE outputs
    ALTER COLUMN type SET DEFAULT 'file',
    ADD CONSTRAINT outputs_type_check CHECK (type IN ('app', 'media', 'text', 'file'));
