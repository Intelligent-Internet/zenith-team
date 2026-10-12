ALTER TABLE companies
    ADD COLUMN artifact_public_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN artifact_policy_version bigint NOT NULL DEFAULT 1;
