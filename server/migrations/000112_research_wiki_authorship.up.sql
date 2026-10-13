-- The onboarding research Run's Wiki pages name the Agent that wrote them and
-- stay out of a person's unpublished edits.
-- wiki_revisions.author_agent_id is the Agent that wrote a revision on behalf of
-- author_user_id; it is an attribution column, so it carries no foreign key.
-- run_wiki_writes.last_draft_seq is the page's draft sequence right after the
-- Run's last publish: a later draft sequence means a person has edited the
-- page since, published or not, and the Run leaves it alone.
ALTER TABLE wiki_revisions ADD COLUMN author_agent_id uuid;
ALTER TABLE run_wiki_writes ADD COLUMN last_draft_seq bigint;
