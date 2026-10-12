-- A runtime is one person's use of one machine (D33). Until now a row was
-- keyed by (company, daemon, provider) and its owner followed whoever
-- registered last, so a second person signing in on the same computer took
-- the row over and the first person's bindings on it were deleted. Now the
-- owner is part of the identity: each person who signs in on a computer gets
-- their own rows, and nothing of anyone else's moves.
--
-- A NULL owner (its account was deleted) never collides: such rows are
-- orphans the offline GC removes, and registration always names an owner.
ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_company_daemon_provider_key,
    ADD CONSTRAINT runtimes_company_daemon_provider_owner_key
        UNIQUE (company_id, daemon_id, provider, owner_user_id);

-- One person at a time: when someone else registers on the machine, the
-- previous person's rows go offline and record when. Their own next
-- registration clears it. A row handed over once is not handed over again,
-- so its owner hears about the handover once.
ALTER TABLE runtimes ADD COLUMN handed_over_at timestamptz;
