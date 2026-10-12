BEGIN;
DROP INDEX outputs_company_visible_created_idx;
CREATE INDEX outputs_owner_idx ON outputs (company_id, owner_user_id)
    WHERE owner_user_id IS NOT NULL;
DROP INDEX outputs_owner_created_idx;
COMMIT;
