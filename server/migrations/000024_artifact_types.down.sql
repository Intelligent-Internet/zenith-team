ALTER TABLE outputs
    DROP CONSTRAINT outputs_type_check;

UPDATE outputs
SET type = CASE type
    WHEN 'app' THEN 'webapp'
    ELSE 'files'
END;

ALTER TABLE outputs
    ALTER COLUMN type SET DEFAULT 'files',
    ADD CONSTRAINT outputs_type_check CHECK (type IN ('files', 'webapp'));
