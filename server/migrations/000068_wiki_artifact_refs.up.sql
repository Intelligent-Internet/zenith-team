BEGIN;

-- The Artifacts each Wiki page currently cites with `[[artifact:<id>]]` or
-- `[[artifact:<id>#<ordinal>]]`. Like wiki_links this is a current-state index,
-- rebuilt on every page write; the immutable revision body is the history. A
-- row grants nothing: every read re-checks output_access for the viewer. A NULL
-- ordinal cites the whole Artifact. Deleting the Run cascades the index row
-- away while the body keeps its token, which then resolves as unavailable.
CREATE TABLE wiki_artifact_refs (
    company_id uuid NOT NULL,
    page_id uuid NOT NULL,
    output_id uuid NOT NULL,
    ordinal integer,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT wiki_artifact_refs_key
        UNIQUE NULLS NOT DISTINCT (company_id, page_id, output_id, ordinal),
    CONSTRAINT wiki_artifact_refs_page_fkey FOREIGN KEY (company_id, page_id)
        REFERENCES wiki_pages(company_id, id) ON DELETE CASCADE,
    CONSTRAINT wiki_artifact_refs_output_fkey FOREIGN KEY (company_id, output_id)
        REFERENCES outputs(company_id, id) ON DELETE CASCADE,
    -- MATCH SIMPLE skips this edge when ordinal IS NULL.
    CONSTRAINT wiki_artifact_refs_item_fkey FOREIGN KEY (output_id, ordinal)
        REFERENCES output_items(output_id, ordinal) ON DELETE CASCADE
);
-- Serves both cascades from outputs and output_items, and the per-Artifact
-- lookup of the pages that cite it.
CREATE INDEX wiki_artifact_refs_output_idx
    ON wiki_artifact_refs (output_id, ordinal, page_id);

COMMIT;
