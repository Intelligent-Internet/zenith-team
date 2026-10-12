-- Restores the (company, daemon, provider) identity. Rows of several people
-- on one machine cannot share it, and deleting one would cascade into its Run
-- history, so every row but the most recently seen one per machine and
-- provider keeps its data under a daemon id of its own. The previous release
-- then reads those rows as another machine that never comes back online.
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY company_id, daemon_id, provider
               ORDER BY last_seen_at DESC NULLS LAST, updated_at DESC, id
           ) AS position
    FROM runtimes
)
UPDATE runtimes
SET daemon_id = runtimes.daemon_id || ':' || runtimes.id::text
FROM ranked
WHERE ranked.id = runtimes.id
  AND ranked.position > 1;

ALTER TABLE runtimes DROP COLUMN handed_over_at;

ALTER TABLE runtimes
    DROP CONSTRAINT runtimes_company_daemon_provider_owner_key,
    ADD CONSTRAINT runtimes_company_daemon_provider_key
        UNIQUE (company_id, daemon_id, provider);
