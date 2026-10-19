-- Browse rows have no place in the 000064 vocabulary; they are deleted.
DELETE FROM run_wiki_reads WHERE kind = 'list';

ALTER TABLE run_wiki_reads
    DROP CONSTRAINT run_wiki_reads_kind_check,
    DROP CONSTRAINT run_wiki_reads_shape_check,
    ADD CONSTRAINT run_wiki_reads_kind_check CHECK (kind IN ('search', 'read')),
    ADD CONSTRAINT run_wiki_reads_shape_check CHECK (
        (kind = 'read' AND page_id IS NOT NULL AND revision_id IS NOT NULL
            AND revision_number IS NOT NULL AND result_count IS NULL)
        OR (kind = 'search' AND page_id IS NULL AND revision_id IS NULL
            AND revision_number IS NULL AND result_count IS NOT NULL)
    );

UPDATE wiki_pages p
SET search_vector = to_tsvector('simple', r.title || ' ' || r.body)
FROM wiki_revisions r
WHERE r.company_id = p.company_id AND r.page_id = p.id AND r.id = p.current_revision_id;
