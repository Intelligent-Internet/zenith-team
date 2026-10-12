ALTER TABLE outputs
    ADD COLUMN type text NOT NULL DEFAULT 'files',
    ADD CONSTRAINT outputs_type_check CHECK (type IN ('files', 'webapp'));
