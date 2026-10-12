-- created_by_user_id: the member who created the Agent (ADR 0013). Any
-- active member may create Agents on a runtime they may use; the creator,
-- and the holders of agents.manage (Company owners and admins), may
-- configure them afterwards. Attribution, no foreign key, like every other
-- actor column. NULL for Agents created before this migration, which
-- therefore stay configurable by agents.manage holders alone: nothing
-- stamps a creator after the insert — a rebind rewrites the binder, never
-- the creator — so only recreating such an Agent gives it one.
--
-- Its own migration, not an edit of 000026: that file has already run on
-- deployed databases, and a column added to a migration the ladder has
-- passed would never reach them. IF NOT EXISTS because development databases
-- that ran an earlier build of this branch received the column from an
-- edited 000026 and sit at version 27 with it already present.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS created_by_user_id uuid;
