-- Pages an onboarding-research Run wrote into the Company Wiki. The Run is the
-- only writer of these rows, and its key is the only handle it has on a page:
-- an upsert finds the page this Run created under the key and never anything
-- else, so the Run cannot edit or archive pages people (or other Runs) wrote.
-- source_markdown is the Run's own markdown with its [[key]] placeholders, kept
-- so a later call can resolve a link to a page that did not exist yet.
-- last_revision_id is the published revision this Run last wrote: when the
-- page's current revision differs, a person edited it and the Run leaves it.
-- A retry child Run (parent_run_id) reaches the rows of its whole lineage.
-- The page edge cascades, so purging a page forgets the write.
CREATE TABLE run_wiki_writes (
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    company_id uuid NOT NULL,
    key text NOT NULL,
    page_id uuid NOT NULL,
    source_markdown text NOT NULL DEFAULT '',
    last_revision_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT run_wiki_writes_pkey PRIMARY KEY (run_id, key),
    CONSTRAINT run_wiki_writes_page_key UNIQUE (page_id),
    CONSTRAINT run_wiki_writes_page_fkey FOREIGN KEY (company_id, page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE
);
