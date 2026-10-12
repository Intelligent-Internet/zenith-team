-- Rolling back forgets who created each Agent; configuration returns to the
-- agents.manage holders alone.
ALTER TABLE agents
    DROP COLUMN created_by_user_id;
