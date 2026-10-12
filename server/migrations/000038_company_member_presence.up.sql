ALTER TABLE company_members
    ADD COLUMN presence_active_until timestamptz,
    ADD COLUMN presence_manual_away boolean NOT NULL DEFAULT false;
