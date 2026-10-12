-- Invitations and share links carry the role the newcomer joins with.
--
-- role: 'admin' or 'member'. Nobody is invited as owner — ownership only
-- moves through the role-change and transfer-ownership routes, which keep
-- the Company's last-owner rule in one place. Existing rows default to
-- 'member', which is what every invitation and link granted before this
-- migration.
ALTER TABLE company_invitations
    ADD COLUMN role text DEFAULT 'member' NOT NULL,
    ADD CONSTRAINT company_invitations_role_check CHECK (role IN ('admin', 'member'));

ALTER TABLE company_share_links
    ADD COLUMN role text DEFAULT 'member' NOT NULL,
    ADD CONSTRAINT company_share_links_role_check CHECK (role IN ('admin', 'member'));
