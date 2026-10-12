BEGIN;

-- The Company-wide Artifact list seeks each source of a viewer's access
-- newest first instead of scanning every Output in the Company. The owner
-- index keeps the (company_id, owner_user_id) prefix it replaces.
CREATE INDEX outputs_owner_created_idx
    ON outputs (company_id, owner_user_id, created_at DESC, id DESC)
    WHERE owner_user_id IS NOT NULL;
DROP INDEX outputs_owner_idx;
CREATE INDEX outputs_company_visible_created_idx
    ON outputs (company_id, created_at DESC, id DESC)
    WHERE visibility = 'company';

COMMIT;
