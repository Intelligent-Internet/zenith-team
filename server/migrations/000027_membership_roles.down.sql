-- Rolling back forgets which role an invitation or link grants; a build
-- without the column treats every pending invitation and live link as a
-- Member invitation, which is what the column defaulted to.
ALTER TABLE company_share_links
    DROP CONSTRAINT company_share_links_role_check,
    DROP COLUMN role;

ALTER TABLE company_invitations
    DROP CONSTRAINT company_invitations_role_check,
    DROP COLUMN role;
