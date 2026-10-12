BEGIN;

-- Evidence of what an Agent's Run searched for and read in the Company Wiki.
-- Append-only. A read row pins the exact revision whose text was returned, so
-- a member can open the version the Agent saw even after the page changed;
-- the page and revision edges cascade, so purging a page purges its trail.
-- agent_id is attribution only, like the other Run-scoped fact rows: no FK.
CREATE TABLE run_wiki_reads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    company_id uuid NOT NULL,
    run_id uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL,
    kind text NOT NULL,
    -- Search rows only: the credential-redacted query and its match count.
    query text NOT NULL DEFAULT '',
    result_count integer,
    -- Read rows only: the page, the revision returned, and the title then.
    page_id uuid,
    revision_id uuid REFERENCES wiki_revisions(id) ON DELETE CASCADE,
    revision_number integer,
    title text NOT NULL DEFAULT '',
    char_offset integer NOT NULL DEFAULT 0,
    chars_returned integer NOT NULL DEFAULT 0,
    truncated boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT run_wiki_reads_pkey PRIMARY KEY (id),
    CONSTRAINT run_wiki_reads_page_fkey FOREIGN KEY (company_id, page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT run_wiki_reads_kind_check CHECK (kind IN ('search', 'read')),
    CONSTRAINT run_wiki_reads_shape_check CHECK (
        (kind = 'read' AND page_id IS NOT NULL AND revision_id IS NOT NULL
            AND revision_number IS NOT NULL AND result_count IS NULL)
        OR (kind = 'search' AND page_id IS NULL AND revision_id IS NULL
            AND revision_number IS NULL AND result_count IS NOT NULL)
    )
);

CREATE INDEX run_wiki_reads_run_idx ON run_wiki_reads (run_id, created_at, id);
CREATE INDEX run_wiki_reads_page_idx ON run_wiki_reads (company_id, page_id, created_at DESC)
    WHERE kind = 'read';
CREATE INDEX run_wiki_reads_revision_idx ON run_wiki_reads (revision_id)
    WHERE revision_id IS NOT NULL;

COMMIT;
