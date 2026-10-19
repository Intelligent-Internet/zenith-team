-- Weighted Wiki search vectors and the Agent browse evidence kind.
--
-- wiki_pages.search_vector held the current revision's title and body at one
-- weight, and the summary was not indexed. It is now the 'simple' lexemes of
-- title (weight A), summary (B) and body (C), so ts_rank can put a title hit
-- above a body-only hit. The 'simple' configuration stays: the content is
-- multilingual (Vietnamese included) and a language stemmer would mangle it.
--
-- The body's page links `[[<uuid>|label]]` and Artifact citations
-- `[[artifact:<uuid>#n]]` are reduced to their label before tokenizing: the id
-- is not text anyone searches for, and its hex fragments and digit runs
-- would match unrelated words and numbers. SetWikiPageCurrentRevision
-- (wiki.sql) writes the same expression on every publish.
UPDATE wiki_pages p
SET search_vector = setweight(to_tsvector('simple', r.title), 'A')
    || setweight(to_tsvector('simple', r.summary), 'B')
    || setweight(to_tsvector('simple', regexp_replace(r.body,
        '\[\[(?:artifact:)?[0-9a-fA-F-]{36}(?:#[0-9]{1,2})?(?:\|([^]]*))?\]\]', '\1', 'g')), 'C')
FROM wiki_revisions r
WHERE r.company_id = p.company_id AND r.page_id = p.id AND r.id = p.current_revision_id;

-- wiki_list records one 'list' row per browse call, shaped like a search row:
-- no page or revision, result_count is the number of pages the browse covered,
-- and query holds the name of the collection browsed ('' for every page).
ALTER TABLE run_wiki_reads
    DROP CONSTRAINT run_wiki_reads_kind_check,
    DROP CONSTRAINT run_wiki_reads_shape_check,
    ADD CONSTRAINT run_wiki_reads_kind_check CHECK (kind IN ('search', 'read', 'list')),
    ADD CONSTRAINT run_wiki_reads_shape_check CHECK (
        (kind = 'read' AND page_id IS NOT NULL AND revision_id IS NOT NULL
            AND revision_number IS NOT NULL AND result_count IS NULL)
        OR (kind IN ('search', 'list') AND page_id IS NULL AND revision_id IS NULL
            AND revision_number IS NULL AND result_count IS NOT NULL)
    );
